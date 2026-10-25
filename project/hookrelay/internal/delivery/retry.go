package delivery

import (
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hibiken/asynq"
)

// Metadata stays in memory until Asynq computes its schedule. Error() never
// includes an untrusted response header or the original HTTP error/URL.
type retryError struct {
	after   string
	message string
}

func (e *retryError) Error() string { return e.message }

// Configuration validates 0 < BaseDelay <= MaxDelay.
type RetryPolicy struct {
	BaseDelay time.Duration
	MaxDelay  time.Duration
}

// Delay implements asynq.RetryDelayFunc. n is zero for the first retry.
// Equal jitter keeps the delay between half the exponential ceiling and the
// ceiling. A valid Retry-After can extend it, but never beyond MaxDelay.
func (p RetryPolicy) Delay(n int, err error, _ *asynq.Task) time.Duration {
	ceiling := p.BaseDelay
	for i := 0; i < n && ceiling < p.MaxDelay; i++ {
		if ceiling > p.MaxDelay/2 {
			ceiling = p.MaxDelay
			break
		}
		ceiling *= 2
	}
	half := ceiling / 2
	delay := ceiling - half + time.Duration(rand.Int64N(int64(half)+1))
	var retry *retryError
	if errors.As(err, &retry) {
		if hint := parseRetryAfter(retry.after, time.Now(), p.MaxDelay); hint > delay {
			delay = hint
		}
	}
	return delay
}

func parseRetryAfter(raw string, now time.Time, maxDelay time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	digits := true
	for i := range raw {
		if raw[i] < '0' || raw[i] > '9' {
			digits = false
			break
		}
	}
	if digits {
		seconds, err := strconv.ParseUint(raw, 10, 64)
		// Check before converting seconds to Duration to avoid overflow.
		if err != nil || seconds > uint64(maxDelay/time.Second) {
			return maxDelay
		}
		return time.Duration(seconds) * time.Second
	}
	date, err := http.ParseTime(raw)
	if err != nil || !date.After(now) {
		return 0
	}
	delay := date.Sub(now)
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}
