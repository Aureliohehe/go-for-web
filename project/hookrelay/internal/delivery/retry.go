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
	attempt uint32        // DB attempt number survives replacement queue task IDs.
	delay   time.Duration // Selected once, shared by the DB cooldown and Asynq.
}

func (e *retryError) Error() string { return e.message }

// Configuration validates 0 < BaseDelay <= MaxDelay. The zero value uses the
// default 5s base / 1m cap, including Workers constructed outside cmd/worker.
type RetryPolicy struct {
	BaseDelay time.Duration
	MaxDelay  time.Duration
}

// Delay implements asynq.RetryDelayFunc. For HTTP results use the durable attempt
// number, so endpoint deferral cannot reset backoff. Infrastructure errors fall
// back to Asynq's n (zero for the first retry of the current queue task).
// Equal jitter keeps the delay between half the exponential ceiling and the
// ceiling. A valid Retry-After can extend it, but never beyond MaxDelay.
func (p RetryPolicy) Delay(n int, err error, _ *asynq.Task) time.Duration {
	var retry *retryError
	if errors.As(err, &retry) {
		if retry.delay > 0 {
			return retry.delay // Do not draw jitter or parse Retry-After twice.
		}
		if retry.attempt > 0 {
			n = int(retry.attempt - 1)
		}
	}
	if p == (RetryPolicy{}) {
		p = RetryPolicy{BaseDelay: 5 * time.Second, MaxDelay: time.Minute}
	}
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
	if retry != nil {
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
