package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"termlinks/backend/internal/auth"
	"termlinks/backend/internal/passkey"
)

const (
	// passkeyBeginBodyLimit bounds the small JSON that starts a ceremony.
	passkeyBeginBodyLimit = 4 << 10
	// passkeyFinishBodyLimit bounds an authenticator response, which carries an
	// attestation object and is still far smaller than this.
	passkeyFinishBodyLimit = 32 << 10

	// Starting a ceremony proves nothing, so its budget is generous and separate:
	// it exists to stop a flood of open challenges, never to deny a sign-in.
	passkeyCeremonyMaximum = 30
	passkeyCeremonyWindow  = time.Minute

	// A failed passkey login is charged here, never against the portal token.
	passkeyFailureMaximum = 5
	passkeyFailureWindow  = time.Minute
)

// clientIdentity is the bucket a client's failures are charged to. Behind a
// reverse proxy every request carries the proxy's address, so without the
// configured header one attacker's failures would lock out everyone; the header
// is read only on the public origin the proxy is known to front.
func (s *Server) clientIdentity(r *http.Request) string {
	if s.trustedClientIPHeader == "" || !s.onPublicOrigin(r) {
		return remoteIP(r)
	}
	value := r.Header.Get(s.trustedClientIPHeader)
	if index := strings.IndexByte(value, ','); index >= 0 {
		value = value[:index]
	}
	if address := net.ParseIP(strings.TrimSpace(value)); address != nil {
		return address.String()
	}
	return remoteIP(r)
}

// setSessionCookie is the single place a portal session cookie is created, so
// token and passkey logins are indistinguishable to the browser.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, sessionID string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    sessionID,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(auth.SessionDuration.Seconds()),
		HttpOnly: true,
		Secure:   s.secureCookie(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secureCookie(r),
		SameSite: http.SameSiteStrictMode,
	})
}

// secureCookie marks the cookie Secure on the configured public hostname even
// though Cloudflare terminates TLS before the request reaches the daemon.
func (s *Server) secureCookie(r *http.Request) bool {
	return r.TLS != nil || s.onPublicOrigin(r)
}

// onPublicOrigin reports whether the request arrived on the configured public
// hostname. The authority comes from the daemon's own settings, never from a
// forwarded header.
func (s *Server) onPublicOrigin(r *http.Request) bool {
	if s.publicAuthority == "" {
		return false
	}
	host := r.Host
	if strings.EqualFold(host, s.publicAuthority) {
		return true
	}
	// A default https port may or may not survive the proxy hop.
	return strings.EqualFold(strings.TrimSuffix(host, ":443"), s.publicAuthority)
}

func (s *Server) authCapabilities(w http.ResponseWriter, r *http.Request) {
	response := map[string]any{
		"configured": s.passkeys != nil,
		"supported":  s.passkeys != nil && s.onPublicOrigin(r),
		"enrolled":   false,
		"origin":     s.publicOrigin,
	}
	if s.passkeys != nil {
		count, err := s.passkeys.Count(r.Context())
		if err != nil {
			s.logger.Error("could not count passkeys", "error", err)
			writeError(w, http.StatusInternalServerError, "could not read passkey settings")
			return
		}
		response["enrolled"] = count > 0
		response["count"] = count
	}
	writeJSON(w, http.StatusOK, response)
}

// requirePasskeys rejects a request when passkeys are unconfigured, when it did
// not arrive on the configured HTTPS origin, or when it is cross-origin.
func (s *Server) requirePasskeys(w http.ResponseWriter, r *http.Request) bool {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return false
	}
	if s.passkeys == nil {
		writeError(w, http.StatusServiceUnavailable, "passkeys are not configured; run: termlinks auth configure --origin https://<hostname>")
		return false
	}
	if !s.onPublicOrigin(r) {
		writeError(w, http.StatusForbidden, "passkeys are only available on "+s.publicOrigin)
		return false
	}
	return true
}

// sessionBinding derives an opaque, non-reversible handle for the browser session
// a registration ceremony belongs to.
func sessionBinding(r *http.Request) string {
	cookie, err := r.Cookie(cookieName)
	if err != nil || cookie.Value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte("termlinks-passkey-binding:" + cookie.Value))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (s *Server) beginPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	if !s.requirePasskeys(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, passkeyBeginBodyLimit)
	defer r.Body.Close()
	var input struct {
		Label string `json:"label"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid passkey request")
		return
	}
	creation, err := s.passkeys.BeginRegistration(r.Context(), input.Label, sessionBinding(r))
	if errors.Is(err, passkey.ErrLimitReached) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if errors.Is(err, passkey.ErrBusy) {
		writeError(w, http.StatusTooManyRequests, "too many passkey attempts in progress")
		return
	}
	if err != nil {
		s.logger.Error("could not begin passkey registration", "error", err)
		writeError(w, http.StatusInternalServerError, "could not start passkey enrollment")
		return
	}
	writeJSON(w, http.StatusOK, creation)
}

func (s *Server) finishPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	if !s.requirePasskeys(w, r) {
		return
	}
	body, ok := readLimitedBody(w, r, passkeyFinishBodyLimit)
	if !ok {
		return
	}
	record, err := s.passkeys.FinishRegistration(r.Context(), body, sessionBinding(r))
	switch {
	case errors.Is(err, passkey.ErrDuplicate):
		writeError(w, http.StatusConflict, "this passkey is already enrolled")
		return
	case errors.Is(err, passkey.ErrLimitReached):
		writeError(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, passkey.ErrCeremonyUnknown), errors.Is(err, passkey.ErrVerification):
		writeError(w, http.StatusBadRequest, "passkey enrollment failed")
		return
	case err != nil:
		s.logger.Error("could not finish passkey registration", "error", err)
		writeError(w, http.StatusInternalServerError, "could not finish passkey enrollment")
		return
	}
	writeJSON(w, http.StatusCreated, record)
}

func (s *Server) beginPasskeyLogin(w http.ResponseWriter, r *http.Request) {
	if !s.requirePasskeys(w, r) {
		return
	}
	if !s.passkeyCeremonies.Allow(s.clientIdentity(r)) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many passkey attempts in progress")
		return
	}
	assertion, err := s.passkeys.BeginLogin(r.Context())
	if errors.Is(err, passkey.ErrVerification) {
		writeError(w, http.StatusConflict, "no passkeys are enrolled")
		return
	}
	if errors.Is(err, passkey.ErrBusy) {
		writeError(w, http.StatusTooManyRequests, "too many passkey attempts in progress")
		return
	}
	if err != nil {
		s.logger.Error("could not begin passkey login", "error", err)
		writeError(w, http.StatusInternalServerError, "could not start passkey login")
		return
	}
	writeJSON(w, http.StatusOK, assertion)
}

func (s *Server) finishPasskeyLogin(w http.ResponseWriter, r *http.Request) {
	if !s.requirePasskeys(w, r) {
		return
	}
	// The attempt is charged on admission and released only by a passkey that
	// verifies, so parallel guesses cannot outrun the budget.
	attempt, allowed := s.passkeyFailures.Acquire(s.clientIdentity(r))
	if !allowed {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many passkey attempts")
		return
	}
	defer attempt.Commit()
	body, ok := readLimitedBody(w, r, passkeyFinishBodyLimit)
	if !ok {
		return
	}
	// Verifying the assertion and issuing the session happen under one lock that
	// removal also takes, so a passkey deleted mid-ceremony cannot leave a live
	// session behind. Serializing logins also keeps two of them from validating
	// against the same stored signature counter.
	s.passkeyMu.Lock()
	defer s.passkeyMu.Unlock()
	record, err := s.passkeys.FinishLogin(r.Context(), body)
	if err != nil {
		switch {
		case errors.Is(err, passkey.ErrClonedAuthenticator):
			s.logger.Warn("rejected a passkey login whose signature counter did not advance", "credential", record.ID)
		case errors.Is(err, passkey.ErrCeremonyUnknown), errors.Is(err, passkey.ErrVerification), errors.Is(err, passkey.ErrNotFound):
		default:
			s.logger.Error("could not finish passkey login", "error", err)
		}
		writeError(w, http.StatusUnauthorized, "passkey authentication failed")
		return
	}
	if s.afterPasskeyVerify != nil {
		s.afterPasskeyVerify()
	}
	sessionID, expires, err := s.auth.Create(auth.Source{Kind: auth.SourcePasskey, CredentialID: record.ID})
	if err != nil {
		s.logger.Error("could not create passkey session", "error", err)
		writeError(w, http.StatusInternalServerError, "could not start your session")
		return
	}
	attempt.Release()
	s.setSessionCookie(w, r, sessionID, expires)
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (s *Server) listPasskeys(w http.ResponseWriter, r *http.Request) {
	if s.passkeys == nil {
		writeError(w, http.StatusServiceUnavailable, "passkeys are not configured")
		return
	}
	records, err := s.passkeys.List(r.Context())
	if err != nil {
		s.logger.Error("could not list passkeys", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list passkeys")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"passkeys": records, "maxPasskeys": passkey.MaxCredentials})
}

func (s *Server) deletePasskey(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	if s.passkeys == nil {
		writeError(w, http.StatusServiceUnavailable, "passkeys are not configured")
		return
	}
	credentialID := r.PathValue("credentialID")
	if !passkey.ValidCredentialID(credentialID) {
		writeError(w, http.StatusNotFound, "passkey not found")
		return
	}
	s.passkeyMu.Lock()
	err := s.passkeys.Delete(r.Context(), credentialID)
	if err == nil {
		s.auth.RevokeCredential(credentialID)
	}
	s.passkeyMu.Unlock()
	if errors.Is(err, passkey.ErrNotFound) {
		writeError(w, http.StatusNotFound, "passkey not found")
		return
	}
	if err != nil {
		s.logger.Error("could not remove passkey", "error", err)
		writeError(w, http.StatusInternalServerError, "could not remove passkey")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func readLimitedBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid passkey request")
		return nil, false
	}
	return body, true
}
