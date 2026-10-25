// Package webhook defines the shared sender/receiver signature protocol.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	HeaderID        = "X-Webhook-ID"
	HeaderTimestamp = "X-Webhook-Timestamp"
	HeaderSignature = "X-Webhook-Signature"
	Tolerance       = 5 * time.Minute
)

var (
	ErrInvalidSecret    = errors.New("invalid signing secret")
	ErrInvalidSignature = errors.New("invalid webhook signature")
)

// Secrets are the 64-character hex strings returned when creating endpoints.
// The HMAC key is the decoded 32 bytes, NOT the ASCII hex representation.
func ValidateSecret(secret string) error {
	key, err := hex.DecodeString(secret)
	if err != nil || len(key) != 32 {
		return ErrInvalidSecret
	}
	return nil
}

// Sign signs the exact bytes sent over HTTP. Each attempt gets a fresh timestamp.
func Sign(secret, id string, body []byte, now time.Time) (timestamp, signature string, err error) {
	if err := ValidateSecret(secret); err != nil {
		return "", "", err
	}
	if !validID(id) {
		return "", "", ErrInvalidSignature
	}
	timestamp = strconv.FormatInt(now.Unix(), 10)
	return timestamp, "v1=" + hex.EncodeToString(digest(secret, timestamp, id, body)), nil
}

// Verify accepts timestamps within five minutes in either direction. This limits
// replay age, but does not deduplicate valid requests inside the time window.
func Verify(secret, id, timestamp, signature string, body []byte, now time.Time) error {
	if err := ValidateSecret(secret); err != nil {
		return err
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	window := int64(Tolerance / time.Second)
	if err != nil || strconv.FormatInt(seconds, 10) != timestamp || !validID(id) ||
		seconds < now.Unix()-window || seconds > now.Unix()+window {
		return ErrInvalidSignature
	}
	if !strings.HasPrefix(signature, "v1=") || len(signature) != 3+sha256.Size*2 {
		return ErrInvalidSignature
	}
	got, err := hex.DecodeString(signature[3:])
	if err != nil || !hmac.Equal(got, digest(secret, timestamp, id, body)) {
		return ErrInvalidSignature
	}
	return nil
}

func validID(id string) bool {
	n, err := strconv.ParseUint(id, 10, 64)
	return err == nil && n != 0 && strconv.FormatUint(n, 10) == id
}

func digest(secret, timestamp, id string, body []byte) []byte {
	key, _ := hex.DecodeString(secret) // Validated by the caller.
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(timestamp + "." + id + "."))
	mac.Write(body)
	return mac.Sum(nil)
}
