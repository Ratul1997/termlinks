package config

import (
	"errors"
	"net"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
)

// ErrPublicOriginInvalid describes every way a public origin can be rejected. The
// message stays a single sentence because the CLI prints it directly.
var ErrPublicOriginInvalid = errors.New("public origin must be a plain https:// origin such as https://terminal.example.com, without a path, query, fragment, credentials, wildcard, or IP address")

// NormalizePublicOrigin validates the canonical browser origin of the portal and
// returns it in the exact form WebAuthn ceremonies are verified against: a
// lowercase scheme and host, with the implicit https port removed.
func NormalizePublicOrigin(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ErrPublicOriginInvalid
	}
	trimmed = strings.TrimSuffix(trimmed, "/")
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", ErrPublicOriginInvalid
	}
	if !strings.EqualFold(parsed.Scheme, "https") || parsed.Opaque != "" {
		return "", ErrPublicOriginInvalid
	}
	if parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", ErrPublicOriginInvalid
	}
	host := strings.ToLower(parsed.Hostname())
	if err := validatePublicHost(host); err != nil {
		return "", err
	}
	port := parsed.Port()
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", ErrPublicOriginInvalid
		}
		if number == 443 {
			port = ""
		}
	}
	if port == "" {
		return "https://" + host, nil
	}
	return "https://" + net.JoinHostPort(host, port), nil
}

// RelyingPartyID derives the WebAuthn relying party ID from a normalized public
// origin. It is the exact hostname, so a hostname change makes existing passkeys
// unusable rather than silently accepting them.
func RelyingPartyID(origin string) (string, error) {
	normalized, err := NormalizePublicOrigin(origin)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", ErrPublicOriginInvalid
	}
	return parsed.Hostname(), nil
}

// ErrClientIPHeaderInvalid describes a rejected trusted client IP header name.
var ErrClientIPHeaderInvalid = errors.New("trusted client IP header must be a plain header name such as CF-Connecting-IP")

// NormalizeClientIPHeader validates the header name a trusted proxy uses to
// report the real client address and returns it in canonical form.
func NormalizeClientIPHeader(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > 64 {
		return "", ErrClientIPHeaderInvalid
	}
	for _, character := range trimmed {
		letter := (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z')
		digit := character >= '0' && character <= '9'
		if !letter && !digit && character != '-' {
			return "", ErrClientIPHeaderInvalid
		}
	}
	return textproto.CanonicalMIMEHeaderKey(trimmed), nil
}

func validatePublicHost(host string) error {
	if host == "" || len(host) > 253 || strings.Contains(host, "*") {
		return ErrPublicOriginInvalid
	}
	if net.ParseIP(host) != nil {
		return ErrPublicOriginInvalid
	}
	if strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return ErrPublicOriginInvalid
	}
	// WebAuthn treats localhost as a secure context, but the public origin exists
	// to name the hostname Cloudflare proxies; a single label cannot be one.
	if !strings.Contains(host, ".") {
		return ErrPublicOriginInvalid
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return ErrPublicOriginInvalid
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return ErrPublicOriginInvalid
			}
		}
	}
	return nil
}
