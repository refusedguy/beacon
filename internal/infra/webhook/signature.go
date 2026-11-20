// Package webhook turns signed GitHub deliveries into hints for the CI bridge.
// A webhook never reads CI state and never publishes a verdict: it only asks
// the bridge to re-read the authoritative state through its own port.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// MaxBodyBytes is the largest payload GitHub can deliver (25 MB). A body over
// the limit is refused before signature verification so memory stays bounded.
const MaxBodyBytes int64 = 25 << 20

const signaturePrefix = "sha256="

var (
	// ErrBodyTooLarge reports a delivery over MaxBodyBytes.
	ErrBodyTooLarge = errors.New("webhook: payload exceeds 25 MB")
	// ErrEmptySecret reports a missing signing secret.
	ErrEmptySecret = errors.New("webhook: empty signing secret")
)

// VerifySignature checks the raw body against X-Hub-Signature-256 in constant
// time. The bytes GitHub signed are the bytes on the wire, never the re-encoded
// JSON. A missing, malformed or wrong digest is rejected.
func VerifySignature(secret, body []byte, header string) bool {
	if len(secret) == 0 || header == "" {
		return false
	}
	header = strings.TrimSpace(header)
	if len(header) < len(signaturePrefix) || !strings.EqualFold(header[:len(signaturePrefix)], signaturePrefix) {
		return false
	}
	got, err := hex.DecodeString(header[len(signaturePrefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// ReadBody reads one delivery up to limit bytes. It reads one byte past the
// limit so an oversized body is detected without buffering the whole payload.
// A non-positive limit falls back to MaxBodyBytes.
func ReadBody(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = MaxBodyBytes
	}
	if r == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read webhook body: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, ErrBodyTooLarge
	}
	return body, nil
}

// LoadSecret reads a webhook secret from a state file. The file must be
// regular and owner-only: a secret readable by the group or by the world is
// refused instead of silently widening who can forge a delivery.
func LoadSecret(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat webhook secret: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("webhook secret %q is not a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("webhook secret %q must be 0600", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read webhook secret: %w", err)
	}
	secret := []byte(strings.TrimSpace(string(raw)))
	if len(secret) == 0 {
		return nil, ErrEmptySecret
	}
	return secret, nil
}
