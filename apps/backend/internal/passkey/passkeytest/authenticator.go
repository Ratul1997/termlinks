// Package passkeytest provides a deterministic software authenticator that
// produces the exact byte layout a real platform authenticator does, so tests
// exercise the real WebAuthn verification path rather than a stub.
package passkeytest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

// Authenticator data flags from §6.1 of the WebAuthn specification.
const (
	FlagUserPresent    byte = 0x01
	FlagUserVerified   byte = 0x04
	FlagBackupEligible byte = 0x08
	FlagBackedUp       byte = 0x10
	FlagAttestedData   byte = 0x40
)

// Authenticator is one deterministic software authenticator holding a single
// ES256 credential.
type Authenticator struct {
	key          *ecdsa.PrivateKey
	CredentialID []byte
	aaguid       []byte
	SignCount    uint32
}

type coseES256Key struct {
	KeyType   int64  `cbor:"1,keyasint"`
	Algorithm int64  `cbor:"3,keyasint"`
	Curve     int64  `cbor:"-1,keyasint"`
	X         []byte `cbor:"-2,keyasint"`
	Y         []byte `cbor:"-3,keyasint"`
}

type attestationObject struct {
	Format       string         `cbor:"fmt"`
	AttStatement map[string]any `cbor:"attStmt"`
	AuthData     []byte         `cbor:"authData"`
}

// New creates an authenticator with a fresh ES256 key and credential ID.
func New(t *testing.T) *Authenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate authenticator key: %v", err)
	}
	credentialID := make([]byte, 32)
	if _, err := rand.Read(credentialID); err != nil {
		t.Fatalf("generate credential ID: %v", err)
	}
	return &Authenticator{key: key, CredentialID: credentialID, aaguid: make([]byte, 16), SignCount: 1}
}

func (a *Authenticator) coseKey(t *testing.T) []byte {
	t.Helper()
	encoded, err := cbor.Marshal(coseES256Key{
		KeyType:   2,
		Algorithm: -7,
		Curve:     1,
		X:         padCoordinate(a.key.PublicKey.X),
		Y:         padCoordinate(a.key.PublicKey.Y),
	})
	if err != nil {
		t.Fatalf("encode COSE key: %v", err)
	}
	return encoded
}

func padCoordinate(value *big.Int) []byte {
	padded := make([]byte, 32)
	value.FillBytes(padded)
	return padded
}

func clientData(t *testing.T, ceremonyType, challenge, origin string) []byte {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"type":        ceremonyType,
		"challenge":   challenge,
		"origin":      origin,
		"crossOrigin": false,
	})
	if err != nil {
		t.Fatalf("encode client data: %v", err)
	}
	return encoded
}

func (a *Authenticator) authenticatorData(relyingPartyID string, flags byte, attested []byte) []byte {
	hash := sha256.Sum256([]byte(relyingPartyID))
	data := make([]byte, 0, 37+len(attested))
	data = append(data, hash[:]...)
	data = append(data, flags)
	data = binary.BigEndian.AppendUint32(data, a.SignCount)
	return append(data, attested...)
}

// attestedCredentialData is the AAGUID, credential ID and public key appended to
// the authenticator data during registration.
func (a *Authenticator) attestedCredentialData(t *testing.T) []byte {
	t.Helper()
	data := append([]byte{}, a.aaguid...)
	data = binary.BigEndian.AppendUint16(data, uint16(len(a.CredentialID)))
	data = append(data, a.CredentialID...)
	return append(data, a.coseKey(t)...)
}

// Options control the ceremony an Authenticator produces.
type Options struct {
	Origin         string
	RelyingPartyID string
	Flags          byte
	// TamperSignature replaces a valid signature with a wrong one.
	TamperSignature bool
	// UserHandle overrides the user handle returned with an assertion.
	UserHandle []byte
}

// RegistrationOptions describes a well-formed registration ceremony that a test
// can then mutate to exercise one rejected case at a time.
func RegistrationOptions(origin, relyingPartyID string) Options {
	return Options{
		Origin:         origin,
		RelyingPartyID: relyingPartyID,
		Flags:          FlagUserPresent | FlagUserVerified | FlagBackupEligible | FlagBackedUp | FlagAttestedData,
	}
}

// AssertionOptions describes a well-formed login ceremony.
func AssertionOptions(origin, relyingPartyID string) Options {
	return Options{
		Origin:         origin,
		RelyingPartyID: relyingPartyID,
		Flags:          FlagUserPresent | FlagUserVerified | FlagBackupEligible | FlagBackedUp,
	}
}

// Register builds the JSON body a browser posts after navigator.credentials.create().
func (a *Authenticator) Register(t *testing.T, challenge string, options Options) []byte {
	t.Helper()
	var attested []byte
	if options.Flags&FlagAttestedData != 0 {
		attested = a.attestedCredentialData(t)
	}
	authData := a.authenticatorData(options.RelyingPartyID, options.Flags, attested)
	attestation, err := cbor.Marshal(attestationObject{Format: "none", AttStatement: map[string]any{}, AuthData: authData})
	if err != nil {
		t.Fatalf("encode attestation object: %v", err)
	}
	collected := clientData(t, "webauthn.create", challenge, options.Origin)
	return mustJSON(t, map[string]any{
		"id":    base64.RawURLEncoding.EncodeToString(a.CredentialID),
		"rawId": base64.RawURLEncoding.EncodeToString(a.CredentialID),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(collected),
			"attestationObject": base64.RawURLEncoding.EncodeToString(attestation),
			"transports":        []string{"internal", "hybrid"},
		},
		"clientExtensionResults": map[string]any{},
	})
}

// Assert builds the JSON body a browser posts after navigator.credentials.get().
func (a *Authenticator) Assert(t *testing.T, challenge string, userHandle []byte, options Options) []byte {
	t.Helper()
	a.SignCount++
	authData := a.authenticatorData(options.RelyingPartyID, options.Flags, nil)
	collected := clientData(t, "webauthn.get", challenge, options.Origin)
	collectedHash := sha256.Sum256(collected)
	digest := sha256.Sum256(append(append([]byte{}, authData...), collectedHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatalf("sign assertion: %v", err)
	}
	if options.TamperSignature {
		signature, err = ecdsa.SignASN1(rand.Reader, a.key, sha256.New().Sum(nil))
		if err != nil {
			t.Fatalf("sign assertion: %v", err)
		}
	}
	handle := userHandle
	if options.UserHandle != nil {
		handle = options.UserHandle
	}
	return mustJSON(t, map[string]any{
		"id":    base64.RawURLEncoding.EncodeToString(a.CredentialID),
		"rawId": base64.RawURLEncoding.EncodeToString(a.CredentialID),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(collected),
			"authenticatorData": base64.RawURLEncoding.EncodeToString(authData),
			"signature":         base64.RawURLEncoding.EncodeToString(signature),
			"userHandle":        base64.RawURLEncoding.EncodeToString(handle),
		},
		"clientExtensionResults": map[string]any{},
	})
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode ceremony response: %v", err)
	}
	return encoded
}
