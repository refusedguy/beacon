// Package webhook implements a local GitHub webhook receiver as a prototype
// event source for Beacon. It checks the raw request signature, de-duplicates
// deliveries and publishes a verdict through the domain ports.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// SignatureHeader is the GitHub header carrying the HMAC of the raw body.
const SignatureHeader = "X-Hub-Signature-256"

const signaturePrefix = "sha256="

var (
	// ErrMissingSignature reports a request without the signature header.
	ErrMissingSignature = errors.New("webhook: missing signature header")
	// ErrInvalidSignature reports a header that does not match the body.
	ErrInvalidSignature = errors.New("webhook: invalid signature")
)

// VerifySignature checks header against the HMAC-SHA256 of body keyed by
// secret. The digest is decoded before comparison, so a malformed hex string
// cannot pass as a near miss. An empty secret is refused: an unsigned payload
// must never move a Watch verdict.
func VerifySignature(body []byte, header, secret string) error {
	if secret == "" {
		return errors.New("webhook: empty secret")
	}
	if header == "" {
		return ErrMissingSignature
	}
	raw := strings.TrimPrefix(header, signaturePrefix)
	if raw == header || raw == "" {
		return fmt.Errorf("%w: bad prefix", ErrInvalidSignature)
	}
	got, err := hex.DecodeString(raw)
	if err != nil {
		return fmt.Errorf("%w: bad hex", ErrInvalidSignature)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return ErrInvalidSignature
	}
	return nil
}

// Sign builds the value GitHub sends in SignatureHeader. It is the counterpart
// of VerifySignature for local senders and tests.
func Sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}
