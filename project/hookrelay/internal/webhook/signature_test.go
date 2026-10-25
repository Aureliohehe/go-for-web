package webhook

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const testSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestSignatureVector(t *testing.T) {
	body := []byte(`{ "order_id": 42 }`)
	now := time.Unix(1700000000, 0)
	ts, sig, err := Sign(testSecret, "9", body, now)
	// Independently calculated with Python hmac/sha256 and bytes.fromhex(key).
	if err != nil || ts != "1700000000" || sig != "v1=d5c85909a17858898612eab93cbd85e9a47855f3b3be2c248681f85a75d7b911" {
		t.Fatal("signature does not match protocol vector")
	}
	if err := Verify(testSecret, "9", ts, sig, body, now); err != nil {
		t.Fatal(err)
	}
	laterTS, laterSig, err := Sign(testSecret, "9", body, now.Add(time.Second))
	if err != nil || laterTS == ts || laterSig == sig {
		t.Fatal("new attempt must sign its new timestamp")
	}
}

func TestVerify(t *testing.T) {
	now := time.Unix(1700000000, 0)
	body := []byte(`{ "order_id": 42 }`)
	ts, sig, _ := Sign(testSecret, "9", body, now)
	for _, tt := range []struct {
		name, secret, id, timestamp, signature string
		body                                   []byte
		now                                    time.Time
		valid                                  bool
	}{
		{"valid", testSecret, "9", ts, sig, body, now, true},
		{"past boundary", testSecret, "9", ts, sig, body, now.Add(Tolerance), true},
		{"future boundary", testSecret, "9", ts, sig, body, now.Add(-Tolerance), true},
		{"expired", testSecret, "9", ts, sig, body, now.Add(Tolerance + time.Second), false},
		{"future", testSecret, "9", ts, sig, body, now.Add(-Tolerance - time.Second), false},
		{"wrong key", strings.Repeat("b", 64), "9", ts, sig, body, now, false},
		{"changed ID", testSecret, "10", ts, sig, body, now, false},
		{"missing ID", testSecret, "", ts, sig, body, now, false},
		{"changed payload", testSecret, "9", ts, sig, []byte(`{ "order_id": 43 }`), now, false},
		{"reserialized JSON", testSecret, "9", ts, sig, []byte(`{"order_id":42}`), now, false},
		{"changed timestamp", testSecret, "9", "1700000001", sig, body, now, false},
		{"timestamp overflow", testSecret, "9", "9223372036854775808", sig, body, now, false},
		{"timestamp max", testSecret, "9", "9223372036854775807", sig, body, now, false},
		{"timestamp min", testSecret, "9", "-9223372036854775808", sig, body, now, false},
		{"missing timestamp", testSecret, "9", "", sig, body, now, false},
		{"noncanonical timestamp", testSecret, "9", "+" + ts, sig, body, now, false},
		{"missing signature", testSecret, "9", ts, "", body, now, false},
		{"unknown version", testSecret, "9", ts, "v2=" + sig[3:], body, now, false},
		{"bad hex", testSecret, "9", ts, "v1=" + strings.Repeat("z", 64), body, now, false},
		{"short MAC", testSecret, "9", ts, sig[:len(sig)-2], body, now, false},
		{"wrong MAC", testSecret, "9", ts, "v1=" + strings.Repeat("0", 64), body, now, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := Verify(tt.secret, tt.id, tt.timestamp, tt.signature, tt.body, tt.now)
			if (err == nil) != tt.valid || !tt.valid && !errors.Is(err, ErrInvalidSignature) {
				t.Fatalf("valid=%v err=%v", tt.valid, err)
			}
		})
	}
}

func TestInvalidSecret(t *testing.T) {
	for _, secret := range []string{"", "secret", strings.Repeat("a", 62), strings.Repeat("a", 66), strings.Repeat("z", 64)} {
		if err := ValidateSecret(secret); !errors.Is(err, ErrInvalidSecret) {
			t.Fatal("invalid secret accepted")
		}
		if _, _, err := Sign(secret, "9", nil, time.Now()); !errors.Is(err, ErrInvalidSecret) {
			t.Fatal("signed with invalid secret")
		}
		if err := Verify(secret, "9", "", "", nil, time.Now()); !errors.Is(err, ErrInvalidSecret) {
			t.Fatal("verified with invalid secret")
		}
	}
}
