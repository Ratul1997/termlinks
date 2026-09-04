package passkey

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const (
	// ChallengeTTL bounds how long a registration or login ceremony may stay open.
	ChallengeTTL = 2 * time.Minute
	// maxOpenCeremonies bounds the in-memory challenge table.
	maxOpenCeremonies = 64
	// relyingPartyName is shown by the platform passkey prompt.
	relyingPartyName = "Termlinks"
)

var (
	// ErrCeremonyUnknown covers an expired, replayed, or never-issued challenge.
	ErrCeremonyUnknown = errors.New("passkey challenge is no longer valid")
	// ErrVerification is the single generic failure returned for every rejected
	// ceremony so a caller cannot learn which check failed.
	ErrVerification = errors.New("passkey verification failed")
	// ErrBusy is returned when too many ceremonies are already open.
	ErrBusy = errors.New("too many passkey attempts in progress")
	// ErrClonedAuthenticator marks a cryptographically valid assertion whose
	// signature counter did not advance. The daemon logs it as a security event
	// and answers the browser with the same generic failure as everything else.
	ErrClonedAuthenticator = errors.New("passkey signature counter did not advance")
)

type ceremonyKind int

const (
	ceremonyRegistration ceremonyKind = iota
	ceremonyLogin
)

type ceremony struct {
	kind    ceremonyKind
	session webauthn.SessionData
	expires time.Time
	// binding ties a registration challenge to the browser session that began it.
	binding string
	label   string
}

// Service runs WebAuthn ceremonies against one configured HTTPS origin.
type Service struct {
	web    *webauthn.WebAuthn
	store  *Store
	origin string
	rpID   string

	mu         sync.Mutex
	ceremonies map[string]*ceremony
	now        func() time.Time
}

// NewService binds a passkey service to the exact origin and relying party ID the
// daemon was configured with. Neither is ever inferred from a request header.
func NewService(store *Store, origin, relyingPartyID string) (*Service, error) {
	if store == nil {
		return nil, errors.New("passkey store is required")
	}
	web, err := webauthn.New(&webauthn.Config{
		RPID:          relyingPartyID,
		RPDisplayName: relyingPartyName,
		RPOrigins:     []string{origin},
	})
	if err != nil {
		return nil, fmt.Errorf("configure passkey verification: %w", err)
	}
	return &Service{
		web:        web,
		store:      store,
		origin:     origin,
		rpID:       relyingPartyID,
		ceremonies: make(map[string]*ceremony),
		now:        time.Now,
	}, nil
}

func (s *Service) Origin() string         { return s.origin }
func (s *Service) RelyingPartyID() string { return s.rpID }

// List returns the enrolled passkeys.
func (s *Service) List(ctx context.Context) ([]Record, error) { return s.store.List(ctx) }

// Count reports how many passkeys are enrolled.
func (s *Service) Count(ctx context.Context) (int, error) { return s.store.Count(ctx) }

// Delete removes one enrolled passkey.
func (s *Service) Delete(ctx context.Context, credentialID string) error {
	return s.store.Delete(ctx, credentialID)
}

// BeginRegistration issues a creation challenge bound to the browser session that
// requested it. The passkey is stored under label once the ceremony finishes.
func (s *Service) BeginRegistration(ctx context.Context, label, binding string) (*protocol.CredentialCreation, error) {
	if binding == "" {
		return nil, ErrVerification
	}
	owner, err := s.owner(ctx)
	if err != nil {
		return nil, err
	}
	if len(owner.credentials) >= MaxCredentials {
		return nil, ErrLimitReached
	}
	creation, session, err := s.web.BeginRegistration(owner,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		}),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
		webauthn.WithExclusions(webauthn.Credentials(owner.credentials).CredentialDescriptors()),
	)
	if err != nil {
		return nil, fmt.Errorf("begin passkey registration: %w", err)
	}
	if err := s.remember(&ceremony{
		kind:    ceremonyRegistration,
		session: *session,
		binding: binding,
		label:   NormalizeLabel(label),
	}); err != nil {
		return nil, err
	}
	return creation, nil
}

// FinishRegistration verifies a creation response and enrolls the credential.
func (s *Service) FinishRegistration(ctx context.Context, body []byte, binding string) (Record, error) {
	parsed, err := protocol.ParseCredentialCreationResponseBytes(body)
	if err != nil {
		return Record{}, ErrVerification
	}
	pending, err := s.claim(parsed.Response.CollectedClientData.Challenge, ceremonyRegistration)
	if err != nil {
		return Record{}, err
	}
	if binding == "" || pending.binding != binding {
		return Record{}, ErrVerification
	}
	owner, err := s.owner(ctx)
	if err != nil {
		return Record{}, err
	}
	credential, err := s.web.CreateCredential(owner, pending.session, parsed)
	if err != nil {
		return Record{}, ErrVerification
	}
	if !credential.Flags.UserVerified {
		return Record{}, ErrVerification
	}
	return s.store.Insert(ctx, pending.label, *credential, s.now())
}

// BeginLogin issues a usernameless discoverable-credential challenge. The browser
// offers local passkeys or a cross-device QR from this one request.
func (s *Service) BeginLogin(ctx context.Context) (*protocol.CredentialAssertion, error) {
	count, err := s.Count(ctx)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrVerification
	}
	assertion, session, err := s.web.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationRequired),
	)
	if err != nil {
		return nil, fmt.Errorf("begin passkey login: %w", err)
	}
	if err := s.remember(&ceremony{kind: ceremonyLogin, session: *session}); err != nil {
		return nil, err
	}
	return assertion, nil
}

// FinishLogin verifies an assertion and returns the passkey that produced it,
// with its signature counter persisted.
func (s *Service) FinishLogin(ctx context.Context, body []byte) (Record, error) {
	parsed, err := protocol.ParseCredentialRequestResponseBytes(body)
	if err != nil {
		return Record{}, ErrVerification
	}
	pending, err := s.claim(parsed.Response.CollectedClientData.Challenge, ceremonyLogin)
	if err != nil {
		return Record{}, err
	}
	credential, err := s.web.ValidateDiscoverableLogin(s.discoverableUser(ctx), pending.session, parsed)
	if err != nil {
		return Record{}, ErrVerification
	}
	if !credential.Flags.UserVerified {
		return Record{}, ErrVerification
	}
	// A signature counter that did not advance signals a cloned authenticator.
	// One owner has no reason to accept that, so the login is refused and the
	// stored counter is left untouched. A restored or migrated authenticator can
	// trip this too, which is why portal token login always stays available to
	// remove the credential and enroll it again.
	if credential.Authenticator.CloneWarning {
		return Record{ID: EncodeCredentialID(credential.ID)}, ErrClonedAuthenticator
	}
	usedAt := s.now()
	if err := s.store.MarkUsed(ctx, *credential, usedAt); err != nil {
		return Record{}, err
	}
	return Record{ID: EncodeCredentialID(credential.ID), LastUsedAt: &usedAt, Credential: *credential}, nil
}

func (s *Service) discoverableUser(ctx context.Context) webauthn.DiscoverableUserHandler {
	return func(rawID, userHandle []byte) (webauthn.User, error) {
		owner, err := s.owner(ctx)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(userHandle, owner.id) {
			return nil, ErrVerification
		}
		return owner, nil
	}
}

func (s *Service) owner(ctx context.Context) (*ownerUser, error) {
	id, err := s.store.OwnerID(ctx)
	if err != nil {
		return nil, err
	}
	records, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	credentials := make([]webauthn.Credential, 0, len(records))
	for _, record := range records {
		credentials = append(credentials, record.Credential)
	}
	return &ownerUser{id: id, credentials: credentials}, nil
}

// remember stores a ceremony keyed by its challenge, which makes every challenge
// single-use because claim removes it.
func (s *Service) remember(pending *ceremony) error {
	now := s.now()
	pending.expires = now.Add(ChallengeTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	if len(s.ceremonies) >= maxOpenCeremonies {
		return ErrBusy
	}
	s.ceremonies[pending.session.Challenge] = pending
	return nil
}

func (s *Service) claim(challenge string, kind ceremonyKind) (*ceremony, error) {
	if challenge == "" {
		return nil, ErrCeremonyUnknown
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	pending, ok := s.ceremonies[challenge]
	if !ok || pending.kind != kind {
		return nil, ErrCeremonyUnknown
	}
	delete(s.ceremonies, challenge)
	if !pending.expires.After(now) {
		return nil, ErrCeremonyUnknown
	}
	return pending, nil
}

func (s *Service) pruneLocked(now time.Time) {
	for challenge, pending := range s.ceremonies {
		if !pending.expires.After(now) {
			delete(s.ceremonies, challenge)
		}
	}
}

// ownerUser presents the single Termlinks owner to the WebAuthn library.
type ownerUser struct {
	id          []byte
	credentials []webauthn.Credential
}

func (o *ownerUser) WebAuthnID() []byte                         { return o.id }
func (o *ownerUser) WebAuthnName() string                       { return "termlinks" }
func (o *ownerUser) WebAuthnDisplayName() string                { return "Termlinks owner" }
func (o *ownerUser) WebAuthnCredentials() []webauthn.Credential { return o.credentials }
