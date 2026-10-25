package delivery

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, tt := range []struct {
		name, raw string
		want      time.Duration
	}{
		{"seconds", "12", 12 * time.Second},
		{"whitespace", " 12 ", 12 * time.Second},
		{"zero", "0", 0},
		{"empty", "", 0},
		{"negative", "-1", 0},
		{"plus sign", "+10", 0},
		{"fraction", "1.5", 0},
		{"malformed", "later", 0},
		{"duplicate values", "10, 20", 0},
		{"leading zeroes", "00012", 12 * time.Second},
		{"exact cap", "60", time.Minute},
		{"non-ASCII digits", "１２", 0},
		{"large seconds", "120", time.Minute},
		{"duration overflow", "9223372036854775807", time.Minute},
		{"integer overflow", strings.Repeat("9", 100), time.Minute},
		{"future date", now.Add(30 * time.Second).Format(http.TimeFormat), 30 * time.Second},
		{"long future date", now.Add(24 * time.Hour).Format(http.TimeFormat), time.Minute},
		{"past date", now.Add(-time.Second).Format(http.TimeFormat), 0},
		{"present date", now.Format(http.TimeFormat), 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseRetryAfter(tt.raw, now, time.Minute); got != tt.want {
				t.Fatalf("got=%v want=%v", got, tt.want)
			}
		})
	}
	if got := parseRetryAfter("1", now, 500*time.Millisecond); got != 500*time.Millisecond {
		t.Fatal("subsecond cap was exceeded")
	}
}

func TestRetryPolicyBackoff(t *testing.T) {
	p := RetryPolicy{BaseDelay: 4 * time.Second, MaxDelay: 32 * time.Second}
	for _, tt := range []struct {
		n        int
		min, max time.Duration
	}{
		{0, 2 * time.Second, 4 * time.Second},
		{1, 4 * time.Second, 8 * time.Second},
		{2, 8 * time.Second, 16 * time.Second},
		{3, 16 * time.Second, 32 * time.Second},
		{1000000, 16 * time.Second, 32 * time.Second},
	} {
		for range 100 {
			got := p.Delay(tt.n, errors.New("database failure"), nil)
			if got < tt.min || got > tt.max {
				t.Fatalf("n=%d delay=%v not in [%v,%v]", tt.n, got, tt.min, tt.max)
			}
		}
	}
	large := RetryPolicy{BaseDelay: time.Nanosecond, MaxDelay: time.Duration(1<<63 - 1)}
	if got := large.Delay(1000000, nil, nil); got < large.MaxDelay/2 || got > large.MaxDelay {
		t.Fatal("backoff overflowed")
	}
}

func TestRetryPolicyBoundaryDelays(t *testing.T) {
	for _, tt := range []struct {
		name      string
		base, cap time.Duration
		n         int
		min, max  time.Duration
	}{
		{"non-power-of-two cap", 4 * time.Second, 10 * time.Second, 2, 5 * time.Second, 10 * time.Second},
		{"equal limits", time.Second, time.Second, 0, 500 * time.Millisecond, time.Second},
		{"one nanosecond", time.Nanosecond, time.Nanosecond, 100, time.Nanosecond, time.Nanosecond},
		{"odd ceiling rounds up", 5 * time.Nanosecond, 5 * time.Nanosecond, 0, 3 * time.Nanosecond, 5 * time.Nanosecond},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := RetryPolicy{BaseDelay: tt.base, MaxDelay: tt.cap}
			for range 100 {
				if got := p.Delay(tt.n, nil, nil); got < tt.min || got > tt.max {
					t.Fatalf("delay=%v not in [%v,%v]", got, tt.min, tt.max)
				}
			}
		})
	}
}

func TestRetryPolicyUsesDurableAttempt(t *testing.T) {
	p := RetryPolicy{BaseDelay: 4 * time.Second, MaxDelay: 32 * time.Second}
	// Queue retry count restarts at zero after deferral, but this is still the
	// second HTTP attempt. Its ceiling must be 8s, not the first attempt's 4s.
	for range 100 {
		got := p.Delay(0, &retryError{message: "HTTP failure", attempt: 2}, nil)
		if got < 4*time.Second || got > 8*time.Second {
			t.Fatalf("backoff reset after deferral: %v", got)
		}
	}
}

func TestRetryPolicyHint(t *testing.T) {
	p := RetryPolicy{BaseDelay: 4 * time.Second, MaxDelay: 32 * time.Second}
	for _, tt := range []struct {
		raw      string
		min, max time.Duration
	}{
		{"10", 10 * time.Second, 10 * time.Second},
		{"999999999999999999999999", 32 * time.Second, 32 * time.Second},
		{"1", 2 * time.Second, 4 * time.Second},
		{"invalid", 2 * time.Second, 4 * time.Second},
		{"0", 2 * time.Second, 4 * time.Second},
	} {
		err := fmt.Errorf("wrapped: %w", &retryError{after: tt.raw, message: "temporary failure"})
		for range 20 {
			got := p.Delay(0, err, nil)
			if got < tt.min || got > tt.max {
				t.Fatalf("hint=%q got=%v", tt.raw, got)
			}
		}
	}
	if got := (&retryError{after: "untrusted-header", message: "temporary failure"}).Error(); got != "temporary failure" {
		t.Fatal("error exposed header")
	}
}
