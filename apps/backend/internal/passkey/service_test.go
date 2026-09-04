package passkey

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"termlinks/backend/internal/passkey/passkeytest"
)

const (
	testOrigin = "https://local.example.com"
	testRPID   = "local.example.com"
)

func newTestService(t *testing.T) (*Service, *Store) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service, err := NewService(store, testOrigin, testRPID)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return service, store
}

// enroll runs a complete, valid registration ceremony.
func enroll(t *testing.T, service *Service, authenticator *passkeytest.Authenticator, label string) Record {
	t.Helper()
	creation, err := service.BeginRegistration(context.Background(), label, "browser-session")
	if err != nil {
		t.Fatalf("begin registration: %v", err)
	}
	body := authenticator.Register(t, creation.Response.Challenge.String(), passkeytest.RegistrationOptions(testOrigin, testRPID))
	record, err := service.FinishRegistration(context.Background(), body, "browser-session")
	if err != nil {
		t.Fatalf("finish registration: %v", err)
	}
	return record
}

// login runs a complete, valid discoverable login ceremony.
func login(t *testing.T, service *Service, store *Store, authenticator *passkeytest.Authenticator) Record {
	t.Helper()
	assertion, err := service.BeginLogin(context.Background())
	if err != nil {
		t.Fatalf("begin login: %v", err)
	}
	owner, err := store.OwnerID(context.Background())
	if err != nil {
		t.Fatalf("owner id: %v", err)
	}
	body := authenticator.Assert(t, assertion.Response.Challenge.String(), owner, passkeytest.AssertionOptions(testOrigin, testRPID))
	record, err := service.FinishLogin(context.Background(), body)
	if err != nil {
		t.Fatalf("finish login: %v", err)
	}
	return record
}

func TestRegistrationAndLogin(t *testing.T) {
	service, store := newTestService(t)
	authenticator := passkeytest.New(t)

	record := enroll(t, service, authenticator, "  iPhone  ")
	if record.Label != "iPhone" {
		t.Fatalf("label = %q", record.Label)
	}
	if record.ID != EncodeCredentialID(authenticator.CredentialID) {
		t.Fatalf("credential ID = %q", record.ID)
	}

	logged := login(t, service, store, authenticator)
	if logged.ID != record.ID {
		t.Fatalf("login returned credential %q, want %q", logged.ID, record.ID)
	}
	records, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].LastUsedAt == nil {
		t.Fatalf("store did not record the login: %+v", records)
	}
	if records[0].Credential.Authenticator.SignCount != authenticator.SignCount {
		t.Fatalf("signature counter = %d, want %d", records[0].Credential.Authenticator.SignCount, authenticator.SignCount)
	}
}

func TestRegistrationRejectsForeignCeremonies(t *testing.T) {
	tests := []struct {
		name    string
		options func() passkeytest.Options
	}{
		{"wrong origin", func() passkeytest.Options {
			return passkeytest.RegistrationOptions("https://attacker.example.com", testRPID)
		}},
		{"wrong relying party", func() passkeytest.Options {
			return passkeytest.RegistrationOptions(testOrigin, "attacker.example.com")
		}},
		{"missing user verification", func() passkeytest.Options {
			options := passkeytest.RegistrationOptions(testOrigin, testRPID)
			options.Flags &^= passkeytest.FlagUserVerified
			return options
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _ := newTestService(t)
			authenticator := passkeytest.New(t)
			creation, err := service.BeginRegistration(context.Background(), "Phone", "browser-session")
			if err != nil {
				t.Fatal(err)
			}
			body := authenticator.Register(t, creation.Response.Challenge.String(), test.options())
			if _, err := service.FinishRegistration(context.Background(), body, "browser-session"); !errors.Is(err, ErrVerification) {
				t.Fatalf("error = %v, want ErrVerification", err)
			}
		})
	}
}

func TestRegistrationRequiresTheSameBrowserSession(t *testing.T) {
	service, _ := newTestService(t)
	authenticator := passkeytest.New(t)
	creation, err := service.BeginRegistration(context.Background(), "Phone", "browser-session")
	if err != nil {
		t.Fatal(err)
	}
	body := authenticator.Register(t, creation.Response.Challenge.String(), passkeytest.RegistrationOptions(testOrigin, testRPID))
	if _, err := service.FinishRegistration(context.Background(), body, "other-session"); !errors.Is(err, ErrVerification) {
		t.Fatalf("error = %v, want ErrVerification", err)
	}
}

func TestRegistrationRejectsMalformedAndDuplicatePayloads(t *testing.T) {
	service, _ := newTestService(t)
	authenticator := passkeytest.New(t)

	if _, err := service.FinishRegistration(context.Background(), []byte("{not json"), "browser-session"); !errors.Is(err, ErrVerification) {
		t.Fatalf("malformed payload error = %v", err)
	}
	enroll(t, service, authenticator, "Phone")

	creation, err := service.BeginRegistration(context.Background(), "Phone again", "browser-session")
	if err != nil {
		t.Fatal(err)
	}
	body := authenticator.Register(t, creation.Response.Challenge.String(), passkeytest.RegistrationOptions(testOrigin, testRPID))
	if _, err := service.FinishRegistration(context.Background(), body, "browser-session"); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate credential error = %v, want ErrDuplicate", err)
	}
}

func TestChallengesExpireAndCannotBeReplayed(t *testing.T) {
	service, store := newTestService(t)
	authenticator := passkeytest.New(t)
	enroll(t, service, authenticator, "Phone")
	owner, err := store.OwnerID(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	assertion, err := service.BeginLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	body := authenticator.Assert(t, assertion.Response.Challenge.String(), owner, passkeytest.AssertionOptions(testOrigin, testRPID))
	if _, err := service.FinishLogin(context.Background(), body); err != nil {
		t.Fatalf("first login: %v", err)
	}
	if _, err := service.FinishLogin(context.Background(), body); !errors.Is(err, ErrCeremonyUnknown) {
		t.Fatalf("replayed login error = %v, want ErrCeremonyUnknown", err)
	}

	clock := time.Now()
	service.now = func() time.Time { return clock }
	assertion, err = service.BeginLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expired := authenticator.Assert(t, assertion.Response.Challenge.String(), owner, passkeytest.AssertionOptions(testOrigin, testRPID))
	clock = clock.Add(ChallengeTTL + time.Second)
	if _, err := service.FinishLogin(context.Background(), expired); !errors.Is(err, ErrCeremonyUnknown) {
		t.Fatalf("expired login error = %v, want ErrCeremonyUnknown", err)
	}
}

func TestLoginRejectsInvalidAssertions(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*passkeytest.Options)
		handle  func(owner []byte) []byte
		wantErr error
	}{
		{"invalid signature", func(options *passkeytest.Options) { options.TamperSignature = true }, nil, ErrVerification},
		{"wrong origin", func(options *passkeytest.Options) { options.Origin = "https://attacker.example.com" }, nil, ErrVerification},
		{"wrong relying party", func(options *passkeytest.Options) { options.RelyingPartyID = "attacker.example.com" }, nil, ErrVerification},
		{"missing user verification", func(options *passkeytest.Options) { options.Flags &^= passkeytest.FlagUserVerified }, nil, ErrVerification},
		{"foreign user handle", nil, func([]byte) []byte { return []byte("someone-else") }, ErrVerification},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, store := newTestService(t)
			authenticator := passkeytest.New(t)
			enroll(t, service, authenticator, "Phone")
			owner, err := store.OwnerID(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			assertion, err := service.BeginLogin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			options := passkeytest.AssertionOptions(testOrigin, testRPID)
			if test.mutate != nil {
				test.mutate(&options)
			}
			if test.handle != nil {
				options.UserHandle = test.handle(owner)
			}
			body := authenticator.Assert(t, assertion.Response.Challenge.String(), owner, options)
			if _, err := service.FinishLogin(context.Background(), body); !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			records, err := store.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if records[0].LastUsedAt != nil {
				t.Fatal("a rejected login must not update the credential")
			}
		})
	}
}

func TestLoginRejectsMalformedPayloadAndUnknownCredential(t *testing.T) {
	service, store := newTestService(t)
	enrolled := passkeytest.New(t)
	enroll(t, service, enrolled, "Phone")
	owner, err := store.OwnerID(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.FinishLogin(context.Background(), []byte("<html>")); !errors.Is(err, ErrVerification) {
		t.Fatalf("malformed payload error = %v", err)
	}
	assertion, err := service.BeginLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stranger := passkeytest.New(t)
	body := stranger.Assert(t, assertion.Response.Challenge.String(), owner, passkeytest.AssertionOptions(testOrigin, testRPID))
	if _, err := service.FinishLogin(context.Background(), body); !errors.Is(err, ErrVerification) {
		t.Fatalf("unknown credential error = %v, want ErrVerification", err)
	}
}

func TestCredentialsAndCountersSurviveRestart(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "auth.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, testOrigin, testRPID)
	if err != nil {
		t.Fatal(err)
	}
	authenticator := passkeytest.New(t)
	record := enroll(t, service, authenticator, "Phone")
	login(t, service, store, authenticator)
	owner, err := store.OwnerID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	restarted, err := NewService(reopened, testOrigin, testRPID)
	if err != nil {
		t.Fatal(err)
	}
	reopenedOwner, err := reopened.OwnerID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(reopenedOwner) != string(owner) {
		t.Fatal("owner identifier changed across restart")
	}
	records, err := reopened.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != record.ID {
		t.Fatalf("credential did not survive restart: %+v", records)
	}
	if records[0].Credential.Authenticator.SignCount != authenticator.SignCount {
		t.Fatalf("signature counter = %d, want %d", records[0].Credential.Authenticator.SignCount, authenticator.SignCount)
	}
	// A replayed counter after restart must still be rejected.
	assertion, err := restarted.BeginLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	authenticator.SignCount = 0
	stale := authenticator.Assert(t, assertion.Response.Challenge.String(), owner, passkeytest.AssertionOptions(testOrigin, testRPID))
	if _, err := restarted.FinishLogin(context.Background(), stale); !errors.Is(err, ErrClonedAuthenticator) {
		t.Fatalf("cloned authenticator error = %v, want ErrClonedAuthenticator", err)
	}
}

func TestMarkUsedNeverMovesTheCounterOrTimestampBackwards(t *testing.T) {
	service, store := newTestService(t)
	authenticator := passkeytest.New(t)
	enroll(t, service, authenticator, "Phone")
	login(t, service, store, authenticator)

	records, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	current := records[0]
	advanced := current.Credential.Authenticator.SignCount
	if current.LastUsedAt == nil {
		t.Fatal("the login should have recorded a timestamp")
	}

	// A write that lost a race carries an older counter and an older timestamp.
	stale := current.Credential
	stale.Authenticator.SignCount = advanced - 1
	if err := store.MarkUsed(context.Background(), stale, current.LastUsedAt.Add(-time.Hour)); err != nil {
		t.Fatalf("stale write: %v", err)
	}
	records, err = store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if records[0].Credential.Authenticator.SignCount != advanced {
		t.Fatalf("signature counter regressed to %d, want %d", records[0].Credential.Authenticator.SignCount, advanced)
	}
	if records[0].LastUsedAt.Before(*current.LastUsedAt) {
		t.Fatalf("last used moved backwards to %v", records[0].LastUsedAt)
	}

	// A counter of zero is a regression too, and must not replace a real one.
	zeroed := current.Credential
	zeroed.Authenticator.SignCount = 0
	if err := store.MarkUsed(context.Background(), zeroed, *current.LastUsedAt); err != nil {
		t.Fatalf("zero-counter write: %v", err)
	}
	records, err = store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if records[0].Credential.Authenticator.SignCount != advanced {
		t.Fatalf("a zero counter replaced the stored one: %d", records[0].Credential.Authenticator.SignCount)
	}

	// A genuinely newer counter is still recorded.
	newer := current.Credential
	newer.Authenticator.SignCount = advanced + 5
	if err := store.MarkUsed(context.Background(), newer, current.LastUsedAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	records, err = store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if records[0].Credential.Authenticator.SignCount != advanced+5 {
		t.Fatalf("signature counter = %d, want %d", records[0].Credential.Authenticator.SignCount, advanced+5)
	}

	if err := store.MarkUsed(context.Background(), webauthnCredentialWithID([]byte("missing")), time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown credential error = %v, want ErrNotFound", err)
	}
}

func TestDeletedCredentialCannotFinishAnOpenCeremony(t *testing.T) {
	service, store := newTestService(t)
	authenticator := passkeytest.New(t)
	record := enroll(t, service, authenticator, "Phone")
	owner, err := store.OwnerID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertion, err := service.BeginLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	body := authenticator.Assert(t, assertion.Response.Challenge.String(), owner, passkeytest.AssertionOptions(testOrigin, testRPID))
	if err := service.Delete(context.Background(), record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.FinishLogin(context.Background(), body); !errors.Is(err, ErrVerification) {
		t.Fatalf("login with a removed credential error = %v, want ErrVerification", err)
	}
}

func TestDeleteRemovesCredential(t *testing.T) {
	service, store := newTestService(t)
	authenticator := passkeytest.New(t)
	record := enroll(t, service, authenticator, "Phone")
	if err := service.Delete(context.Background(), record.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(context.Background(), record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete error = %v, want ErrNotFound", err)
	}
	count, err := store.Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("count = %d after removal", count)
	}
	if _, err := service.BeginLogin(context.Background()); !errors.Is(err, ErrVerification) {
		t.Fatalf("login with no credentials error = %v", err)
	}
}

func TestNormalizeLabel(t *testing.T) {
	tests := map[string]string{
		"":                      "Passkey",
		"   ":                   "Passkey",
		" Zahid's iPhone ":      "Zahid's iPhone",
		"line\nbreak":           "linebreak",
		string(make([]byte, 0)): "Passkey",
	}
	for input, want := range tests {
		if got := NormalizeLabel(input); got != want {
			t.Fatalf("NormalizeLabel(%q) = %q, want %q", input, got, want)
		}
	}
	long := make([]rune, MaxLabelRunes+40)
	for index := range long {
		long[index] = 'a'
	}
	if got := NormalizeLabel(string(long)); len([]rune(got)) != MaxLabelRunes {
		t.Fatalf("long label kept %d runes", len([]rune(got)))
	}
}

func TestValidCredentialID(t *testing.T) {
	if !ValidCredentialID(EncodeCredentialID([]byte("credential"))) {
		t.Fatal("a base64url credential ID should be valid")
	}
	for _, invalid := range []string{"", "not base64!!", "cGFk===="} {
		if ValidCredentialID(invalid) {
			t.Fatalf("%q should be rejected", invalid)
		}
	}
}

// webauthnCredentialWithID builds the smallest credential that names an ID.
func webauthnCredentialWithID(id []byte) webauthn.Credential {
	return webauthn.Credential{ID: id}
}
