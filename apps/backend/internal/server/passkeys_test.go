package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"termlinks/backend/internal/auth"
	"termlinks/backend/internal/passkey"
	"termlinks/backend/internal/passkey/passkeytest"
	"termlinks/backend/internal/session"
)

const (
	publicOrigin = "https://local.example.com"
	publicHost   = "local.example.com"
	portalToken  = "private-token"
)

type passkeyHarness struct {
	handler *Server
	server  *httptest.Server
	auth    *auth.Manager
	service *passkey.Service
	client  *http.Client
	// owner is the WebAuthn user handle, read from the creation options the way
	// a browser receives it.
	owner []byte
}

func newPasskeyHarness(t *testing.T, withPasskeys bool) *passkeyHarness {
	t.Helper()
	authManager := auth.New(portalToken)
	handler, err := New(session.NewManager(), authManager, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	harness := &passkeyHarness{handler: handler, auth: authManager, client: &http.Client{}}
	if withPasskeys {
		store, err := passkey.Open(filepath.Join(t.TempDir(), "auth.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		service, err := passkey.NewService(store, publicOrigin, publicHost)
		if err != nil {
			t.Fatal(err)
		}
		handler.SetPasskeys(service)
		harness.service = service
	}
	harness.server = httptest.NewServer(handler.WebHandler())
	t.Cleanup(harness.server.Close)
	return harness
}

// do issues a request that looks like it arrived on the configured public origin.
func (h *passkeyHarness) do(t *testing.T, method, path string, body []byte, sessionID string) *http.Response {
	t.Helper()
	return h.doOn(t, method, path, body, sessionID, publicHost, publicOrigin)
}

// doWith issues a public-origin request carrying extra headers, such as the one
// a trusted proxy uses to report the real client address.
func (h *passkeyHarness) doWith(t *testing.T, method, path string, body []byte, sessionID string, headers map[string]string) *http.Response {
	t.Helper()
	return h.doOn(t, method, path, body, sessionID, publicHost, publicOrigin, headers)
}

func (h *passkeyHarness) doOn(t *testing.T, method, path string, body []byte, sessionID, host, origin string, extra ...map[string]string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequest(method, h.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = host
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	request.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		request.AddCookie(&http.Cookie{Name: cookieName, Value: sessionID})
	}
	for _, headers := range extra {
		for name, value := range headers {
			request.Header.Set(name, value)
		}
	}
	response, err := h.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeBody(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer response.Body.Close()
	if target == nil {
		_, _ = io.Copy(io.Discard, response.Body)
		return
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

// tokenLogin authenticates with the portal token and returns the session cookie.
func (h *passkeyHarness) tokenLogin(t *testing.T) (string, *http.Cookie) {
	t.Helper()
	response := h.do(t, http.MethodPost, "/api/login", []byte(`{"token":"`+portalToken+`"}`), "")
	decodeBody(t, response, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("token login status = %d", response.StatusCode)
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == cookieName {
			return cookie.Value, cookie
		}
	}
	t.Fatal("token login did not set a session cookie")
	return "", nil
}

// enrollPasskey runs a complete registration ceremony over HTTP.
func (h *passkeyHarness) enrollPasskey(t *testing.T, sessionID, label string, authenticator *passkeytest.Authenticator) string {
	t.Helper()
	response := h.do(t, http.MethodPost, "/api/auth/passkeys/register/begin", []byte(`{"label":"`+label+`"}`), sessionID)
	var creation struct {
		Response struct {
			Challenge string `json:"challenge"`
			User      struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	decodeBody(t, response, &creation)
	if response.StatusCode != http.StatusOK || creation.Response.Challenge == "" {
		t.Fatalf("register begin status = %d", response.StatusCode)
	}
	owner, err := base64.RawURLEncoding.DecodeString(creation.Response.User.ID)
	if err != nil || len(owner) == 0 {
		t.Fatalf("owner handle = %q (%v)", creation.Response.User.ID, err)
	}
	h.owner = owner
	body := authenticator.Register(t, creation.Response.Challenge, passkeytest.RegistrationOptions(publicOrigin, publicHost))
	response = h.do(t, http.MethodPost, "/api/auth/passkeys/register/finish", body, sessionID)
	var record struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	decodeBody(t, response, &record)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("register finish status = %d", response.StatusCode)
	}
	if record.Label != label {
		t.Fatalf("stored label = %q, want %q", record.Label, label)
	}
	return record.ID
}

// passkeyLogin runs a complete discoverable login ceremony over HTTP.
func (h *passkeyHarness) passkeyLogin(t *testing.T, authenticator *passkeytest.Authenticator) (string, *http.Cookie) {
	t.Helper()
	response := h.do(t, http.MethodPost, "/api/auth/passkeys/login/begin", []byte(`{}`), "")
	var assertion struct {
		Response struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	decodeBody(t, response, &assertion)
	if response.StatusCode != http.StatusOK || assertion.Response.Challenge == "" {
		t.Fatalf("login begin status = %d", response.StatusCode)
	}
	body := authenticator.Assert(t, assertion.Response.Challenge, h.owner, passkeytest.AssertionOptions(publicOrigin, publicHost))
	response = h.do(t, http.MethodPost, "/api/auth/passkeys/login/finish", body, "")
	decodeBody(t, response, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login finish status = %d", response.StatusCode)
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == cookieName {
			return cookie.Value, cookie
		}
	}
	t.Fatal("passkey login did not set a session cookie")
	return "", nil
}

func TestAuthCapabilities(t *testing.T) {
	unconfigured := newPasskeyHarness(t, false)
	var capabilities map[string]any
	decodeBody(t, unconfigured.do(t, http.MethodGet, "/api/auth/capabilities", nil, ""), &capabilities)
	if capabilities["configured"] != false || capabilities["supported"] != false || capabilities["enrolled"] != false {
		t.Fatalf("unconfigured capabilities = %v", capabilities)
	}

	harness := newPasskeyHarness(t, true)
	decodeBody(t, harness.do(t, http.MethodGet, "/api/auth/capabilities", nil, ""), &capabilities)
	if capabilities["configured"] != true || capabilities["supported"] != true || capabilities["enrolled"] != false {
		t.Fatalf("configured capabilities = %v", capabilities)
	}
	if capabilities["origin"] != publicOrigin {
		t.Fatalf("origin = %v", capabilities["origin"])
	}

	// Reached over raw localhost the same daemon offers token login only.
	decodeBody(t, harness.doOn(t, http.MethodGet, "/api/auth/capabilities", nil, "", "127.0.0.1:57321", ""), &capabilities)
	if capabilities["configured"] != true || capabilities["supported"] != false {
		t.Fatalf("localhost capabilities = %v", capabilities)
	}

	sessionID, _ := harness.tokenLogin(t)
	harness.enrollPasskey(t, sessionID, "Phone", passkeytest.New(t))
	decodeBody(t, harness.do(t, http.MethodGet, "/api/auth/capabilities", nil, ""), &capabilities)
	if capabilities["enrolled"] != true || capabilities["count"] != float64(1) {
		t.Fatalf("enrolled capabilities = %v", capabilities)
	}
}

func TestPasskeyLoginIssuesTheSameSessionCookie(t *testing.T) {
	harness := newPasskeyHarness(t, true)
	sessionID, tokenCookie := harness.tokenLogin(t)
	authenticator := passkeytest.New(t)
	harness.enrollPasskey(t, sessionID, "Phone", authenticator)

	response := harness.do(t, http.MethodPost, "/api/logout", nil, sessionID)
	decodeBody(t, response, nil)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status = %d", response.StatusCode)
	}

	passkeySessionID, passkeyCookie := harness.passkeyLogin(t, authenticator)
	for name, cookie := range map[string]*http.Cookie{"token": tokenCookie, "passkey": passkeyCookie} {
		if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
			t.Fatalf("%s cookie = %+v", name, cookie)
		}
		if cookie.MaxAge != int(auth.SessionDuration.Seconds()) {
			t.Fatalf("%s cookie MaxAge = %d", name, cookie.MaxAge)
		}
	}

	response = harness.do(t, http.MethodGet, "/api/sessions", nil, passkeySessionID)
	decodeBody(t, response, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("passkey session status = %d", response.StatusCode)
	}
}

func TestTokenLoginStaysAvailableOnLocalhost(t *testing.T) {
	harness := newPasskeyHarness(t, true)
	response := harness.doOn(t, http.MethodPost, "/api/login", []byte(`{"token":"`+portalToken+`"}`), "", "127.0.0.1:57321", "http://127.0.0.1:57321")
	decodeBody(t, response, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("localhost token login status = %d", response.StatusCode)
	}
	var cookie *http.Cookie
	for _, candidate := range response.Cookies() {
		if candidate.Name == cookieName {
			cookie = candidate
		}
	}
	if cookie == nil {
		t.Fatal("localhost token login did not set a session cookie")
	}
	// Plain http on the loopback address must not claim to be a secure cookie.
	if cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("localhost cookie = %+v", cookie)
	}
	response = harness.doOn(t, http.MethodGet, "/api/sessions", nil, cookie.Value, "127.0.0.1:57321", "")
	decodeBody(t, response, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("localhost session status = %d", response.StatusCode)
	}
	// Passkey ceremonies stay refused off the configured origin.
	response = harness.doOn(t, http.MethodPost, "/api/auth/passkeys/login/begin", []byte(`{}`), "", "127.0.0.1:57321", "http://127.0.0.1:57321")
	decodeBody(t, response, nil)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("localhost passkey login status = %d", response.StatusCode)
	}
}

func TestRemovingAPasskeyInvalidatesOnlyItsSessions(t *testing.T) {
	harness := newPasskeyHarness(t, true)
	tokenSessionID, _ := harness.tokenLogin(t)
	first := passkeytest.New(t)
	second := passkeytest.New(t)
	firstID := harness.enrollPasskey(t, tokenSessionID, "Phone", first)
	harness.enrollPasskey(t, tokenSessionID, "Laptop", second)

	firstSessionID, _ := harness.passkeyLogin(t, first)
	secondSessionID, _ := harness.passkeyLogin(t, second)

	response := harness.do(t, http.MethodDelete, "/api/auth/passkeys/"+firstID, nil, tokenSessionID)
	decodeBody(t, response, nil)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", response.StatusCode)
	}
	if harness.auth.Valid(firstSessionID) {
		t.Fatal("the removed passkey's session is still valid")
	}
	if !harness.auth.Valid(secondSessionID) || !harness.auth.Valid(tokenSessionID) {
		t.Fatal("removing one passkey invalidated unrelated sessions")
	}

	var listed struct {
		Passkeys []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"passkeys"`
	}
	decodeBody(t, harness.do(t, http.MethodGet, "/api/auth/passkeys", nil, tokenSessionID), &listed)
	if len(listed.Passkeys) != 1 || listed.Passkeys[0].Label != "Laptop" {
		t.Fatalf("remaining passkeys = %+v", listed.Passkeys)
	}

	response = harness.do(t, http.MethodDelete, "/api/auth/passkeys/"+firstID, nil, tokenSessionID)
	decodeBody(t, response, nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("repeat delete status = %d", response.StatusCode)
	}
}

func TestPasskeyEndpointsRejectCrossOriginRequests(t *testing.T) {
	harness := newPasskeyHarness(t, true)
	sessionID, _ := harness.tokenLogin(t)
	authenticator := passkeytest.New(t)
	credentialID := harness.enrollPasskey(t, sessionID, "Phone", authenticator)

	requests := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/auth/passkeys/register/begin"},
		{http.MethodPost, "/api/auth/passkeys/register/finish"},
		{http.MethodPost, "/api/auth/passkeys/login/begin"},
		{http.MethodPost, "/api/auth/passkeys/login/finish"},
		{http.MethodDelete, "/api/auth/passkeys/" + credentialID},
	}
	for _, attacker := range []string{"", "https://evil.example.com"} {
		for _, request := range requests {
			response := harness.doOn(t, request.method, request.path, []byte(`{}`), sessionID, publicHost, attacker)
			decodeBody(t, response, nil)
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("%s %s with Origin %q status = %d", request.method, request.path, attacker, response.StatusCode)
			}
		}
	}
}

func TestPasskeyEndpointsRequireAuthentication(t *testing.T) {
	harness := newPasskeyHarness(t, true)
	for _, path := range []string{"/api/auth/passkeys/register/begin", "/api/auth/passkeys/register/finish"} {
		response := harness.do(t, http.MethodPost, path, []byte(`{}`), "")
		decodeBody(t, response, nil)
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s without a session status = %d", path, response.StatusCode)
		}
	}
	response := harness.do(t, http.MethodGet, "/api/auth/passkeys", nil, "")
	decodeBody(t, response, nil)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("list without a session status = %d", response.StatusCode)
	}
}

func TestPasskeyEndpointsCapBodySizeAndReturnGenericErrors(t *testing.T) {
	harness := newPasskeyHarness(t, true)
	sessionID, _ := harness.tokenLogin(t)
	harness.enrollPasskey(t, sessionID, "Phone", passkeytest.New(t))

	oversized := bytes.Repeat([]byte("a"), (64<<10)+1)
	for _, path := range []string{"/api/auth/passkeys/register/finish", "/api/auth/passkeys/login/finish"} {
		response := harness.do(t, http.MethodPost, path, oversized, sessionID)
		decodeBody(t, response, nil)
		if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusCreated {
			t.Fatalf("%s accepted an oversized body: status = %d", path, response.StatusCode)
		}
	}

	var failure map[string]string
	response := harness.do(t, http.MethodPost, "/api/auth/passkeys/login/finish", []byte(`{"id":"nope"}`), "")
	decodeBody(t, response, &failure)
	if response.StatusCode != http.StatusUnauthorized || failure["error"] != "passkey authentication failed" {
		t.Fatalf("login failure status = %d body = %v", response.StatusCode, failure)
	}
	response = harness.do(t, http.MethodPost, "/api/auth/passkeys/register/finish", []byte(`{"id":"nope"}`), sessionID)
	decodeBody(t, response, &failure)
	if response.StatusCode != http.StatusBadRequest || failure["error"] != "passkey enrollment failed" {
		t.Fatalf("enrollment failure status = %d body = %v", response.StatusCode, failure)
	}
}

func TestPasskeyFailuresNeverCloseTokenRecovery(t *testing.T) {
	harness := newPasskeyHarness(t, true)
	sessionID, _ := harness.tokenLogin(t)
	harness.enrollPasskey(t, sessionID, "Phone", passkeytest.New(t))

	limited := false
	for attempt := 0; attempt < 12 && !limited; attempt++ {
		response := harness.do(t, http.MethodPost, "/api/auth/passkeys/login/finish", []byte(`{"id":"nope"}`), "")
		decodeBody(t, response, nil)
		if response.StatusCode == http.StatusTooManyRequests {
			if response.Header.Get("Retry-After") == "" {
				t.Fatal("rate-limited response is missing Retry-After")
			}
			limited = true
		}
	}
	if !limited {
		t.Fatal("repeated passkey login failures were never rate-limited")
	}

	// Starting ceremonies proves nothing, so it must not spend the login budget
	// either: an unauthenticated flood cannot lock the owner out.
	for attempt := 0; attempt < 10; attempt++ {
		decodeBody(t, harness.do(t, http.MethodPost, "/api/auth/passkeys/login/begin", []byte(`{}`), ""), nil)
	}

	response := harness.do(t, http.MethodPost, "/api/login", []byte(`{"token":"`+portalToken+`"}`), "")
	decodeBody(t, response, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("token recovery after passkey rate limiting status = %d", response.StatusCode)
	}
}

func TestFailedTokenLoginsDoNotBlockPasskeyLogin(t *testing.T) {
	harness := newPasskeyHarness(t, true)
	sessionID, _ := harness.tokenLogin(t)
	authenticator := passkeytest.New(t)
	harness.enrollPasskey(t, sessionID, "Phone", authenticator)

	for attempt := 0; attempt < 6; attempt++ {
		decodeBody(t, harness.do(t, http.MethodPost, "/api/login", []byte(`{"token":"wrong"}`), ""), nil)
	}
	response := harness.do(t, http.MethodPost, "/api/login", []byte(`{"token":"`+portalToken+`"}`), "")
	decodeBody(t, response, nil)
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("token login should be throttled after repeated guesses: status = %d", response.StatusCode)
	}
	// The owner's passkey still works while the token bucket is cooling down.
	harness.passkeyLogin(t, authenticator)
}

func TestTrustedClientIPHeaderSeparatesRateLimitBuckets(t *testing.T) {
	harness := newPasskeyHarness(t, true)
	harness.handler.SetTrustedClientIPHeader("Cf-Connecting-Ip")
	sessionID, _ := harness.tokenLogin(t)
	harness.enrollPasskey(t, sessionID, "Phone", passkeytest.New(t))

	// One client burns its own budget.
	attacker := map[string]string{"Cf-Connecting-Ip": "203.0.113.10"}
	limited := false
	for attempt := 0; attempt < 12 && !limited; attempt++ {
		response := harness.doWith(t, http.MethodPost, "/api/auth/passkeys/login/finish", []byte(`{"id":"nope"}`), "", attacker)
		decodeBody(t, response, nil)
		limited = response.StatusCode == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("the forwarded client was never rate-limited")
	}
	// A different client behind the same proxy is unaffected.
	response := harness.doWith(t, http.MethodPost, "/api/auth/passkeys/login/begin", []byte(`{}`), "", map[string]string{"Cf-Connecting-Ip": "198.51.100.7"})
	decodeBody(t, response, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("second client status = %d", response.StatusCode)
	}
	response = harness.doWith(t, http.MethodPost, "/api/login", []byte(`{"token":"`+portalToken+`"}`), "", map[string]string{"Cf-Connecting-Ip": "198.51.100.7"})
	decodeBody(t, response, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("second client token login status = %d", response.StatusCode)
	}
}

func TestRemovingAPasskeyRacesLoginWithoutLeavingALiveSession(t *testing.T) {
	harness := newPasskeyHarness(t, true)
	// Give each round its own rate-limit bucket so a lost race never throttles
	// the next round and hides the interleaving under test.
	harness.handler.SetTrustedClientIPHeader("Cf-Connecting-Ip")
	tokenSessionID, _ := harness.tokenLogin(t)

	// Fire the removal and an otherwise valid sign-in at the same moment, many
	// times, so the interleaving lands on both sides of session issuance. The
	// invariant is the same either way: once the passkey is gone, no session it
	// created may still authenticate.
	for round := 0; round < 25; round++ {
		authenticator := passkeytest.New(t)
		credentialID := harness.enrollPasskey(t, tokenSessionID, "Phone", authenticator)
		response := harness.do(t, http.MethodPost, "/api/auth/passkeys/login/begin", []byte(`{}`), "")
		var assertion struct {
			Response struct {
				Challenge string `json:"challenge"`
			} `json:"publicKey"`
		}
		decodeBody(t, response, &assertion)
		body := authenticator.Assert(t, assertion.Response.Challenge, harness.owner, passkeytest.AssertionOptions(publicOrigin, publicHost))

		client := map[string]string{"Cf-Connecting-Ip": fmt.Sprintf("203.0.113.%d", round+1)}
		var (
			start        = make(chan struct{})
			wait         sync.WaitGroup
			issued       string
			loginStatus  int
			deleteStatus int
		)
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			loginResponse := harness.doWith(t, http.MethodPost, "/api/auth/passkeys/login/finish", body, "", client)
			decodeBody(t, loginResponse, nil)
			loginStatus = loginResponse.StatusCode
			for _, cookie := range loginResponse.Cookies() {
				if cookie.Name == cookieName && cookie.Value != "" {
					issued = cookie.Value
				}
			}
		}()
		go func() {
			defer wait.Done()
			<-start
			deleteResponse := harness.do(t, http.MethodDelete, "/api/auth/passkeys/"+credentialID, nil, tokenSessionID)
			decodeBody(t, deleteResponse, nil)
			deleteStatus = deleteResponse.StatusCode
		}()
		close(start)
		wait.Wait()

		if deleteStatus != http.StatusNoContent {
			t.Fatalf("round %d: delete status = %d", round, deleteStatus)
		}
		if loginStatus != http.StatusOK && loginStatus != http.StatusUnauthorized {
			t.Fatalf("round %d: unexpected login status %d", round, loginStatus)
		}
		if issued != "" && harness.auth.Valid(issued) {
			t.Fatalf("round %d: a removed passkey left a live session behind", round)
		}
		// The owner's token session is never collateral damage.
		if !harness.auth.Valid(tokenSessionID) {
			t.Fatalf("round %d: the token session was revoked", round)
		}
	}
}

func TestRemovalDuringSessionIssuanceLeavesNoLiveSession(t *testing.T) {
	harness := newPasskeyHarness(t, true)
	tokenSessionID, _ := harness.tokenLogin(t)
	authenticator := passkeytest.New(t)
	credentialID := harness.enrollPasskey(t, tokenSessionID, "Phone", authenticator)

	response := harness.do(t, http.MethodPost, "/api/auth/passkeys/login/begin", []byte(`{}`), "")
	var assertion struct {
		Response struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	decodeBody(t, response, &assertion)
	body := authenticator.Assert(t, assertion.Response.Challenge, harness.owner, passkeytest.AssertionOptions(publicOrigin, publicHost))

	// Remove the passkey at the one instant that matters: after its assertion has
	// verified but before the session exists. Removal blocks on the same lock the
	// sign-in holds, so it takes effect once the session has been created.
	removed := make(chan int, 1)
	finished := make(chan struct{})
	harness.handler.afterPasskeyVerify = func() {
		go func() {
			defer close(finished)
			deleteResponse := harness.do(t, http.MethodDelete, "/api/auth/passkeys/"+credentialID, nil, tokenSessionID)
			decodeBody(t, deleteResponse, nil)
			removed <- deleteResponse.StatusCode
		}()
		select {
		case <-finished:
			t.Error("removal completed while a verified sign-in was still issuing its session")
		case <-time.After(150 * time.Millisecond):
		}
	}

	response = harness.do(t, http.MethodPost, "/api/auth/passkeys/login/finish", body, "")
	decodeBody(t, response, nil)
	issued := ""
	for _, cookie := range response.Cookies() {
		if cookie.Name == cookieName && cookie.Value != "" {
			issued = cookie.Value
		}
	}
	select {
	case status := <-removed:
		if status != http.StatusNoContent {
			t.Fatalf("delete status = %d", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("removal never completed after the sign-in released its lock")
	}
	if issued != "" && harness.auth.Valid(issued) {
		t.Fatal("a passkey removed during sign-in left a live session behind")
	}
	if !harness.auth.Valid(tokenSessionID) {
		t.Fatal("the token session was revoked")
	}
}

func TestConcurrentPasskeyGuessesCannotOutrunTheBudget(t *testing.T) {
	harness := newPasskeyHarness(t, true)
	sessionID, _ := harness.tokenLogin(t)
	harness.enrollPasskey(t, sessionID, "Phone", passkeytest.New(t))

	var (
		start   = make(chan struct{})
		wait    sync.WaitGroup
		mu      sync.Mutex
		refused int
		limited int
	)
	const guesses = 40
	for attempt := 0; attempt < guesses; attempt++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			response := harness.do(t, http.MethodPost, "/api/auth/passkeys/login/finish", []byte(`{"id":"nope"}`), "")
			decodeBody(t, response, nil)
			mu.Lock()
			defer mu.Unlock()
			switch response.StatusCode {
			case http.StatusUnauthorized:
				refused++
			case http.StatusTooManyRequests:
				limited++
			default:
				t.Errorf("unexpected status %d", response.StatusCode)
			}
		}()
	}
	close(start)
	wait.Wait()
	if refused > passkeyFailureMaximum {
		t.Fatalf("%d parallel guesses were verified, want at most %d", refused, passkeyFailureMaximum)
	}
	if refused+limited != guesses {
		t.Fatalf("%d refused + %d limited != %d", refused, limited, guesses)
	}
	// Portal token recovery is still open after the burst.
	response := harness.do(t, http.MethodPost, "/api/login", []byte(`{"token":"`+portalToken+`"}`), "")
	decodeBody(t, response, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("token recovery after a passkey burst status = %d", response.StatusCode)
	}
}

func TestPasskeyEndpointsAreUnavailableWhenUnconfigured(t *testing.T) {
	harness := newPasskeyHarness(t, false)
	sessionID, cookie := harness.tokenLogin(t)
	if cookie.Secure {
		t.Fatal("an unconfigured daemon must not mark plain http cookies Secure")
	}
	for _, path := range []string{"/api/auth/passkeys/register/begin", "/api/auth/passkeys/login/begin"} {
		response := harness.do(t, http.MethodPost, path, []byte(`{}`), sessionID)
		decodeBody(t, response, nil)
		if response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d", path, response.StatusCode)
		}
	}
	response := harness.do(t, http.MethodGet, "/api/auth/passkeys", nil, sessionID)
	decodeBody(t, response, nil)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("list status = %d", response.StatusCode)
	}
}

func TestNewEndpointsAreNotCached(t *testing.T) {
	harness := newPasskeyHarness(t, true)
	response := harness.do(t, http.MethodGet, "/api/auth/capabilities", nil, "")
	decodeBody(t, response, nil)
	if !strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
		t.Fatalf("Cache-Control = %q", response.Header.Get("Cache-Control"))
	}
}
