package receiver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/aurelio/hookrelay/internal/webhook"
)

type Acceptor func(context.Context, uint64, []byte) (duplicate bool, err error)

// NewHandler verifies before persistence. Non-2xx status simulates rejection
// before processing. For 2xx, delay happens AFTER commit, only on the first write:
// a timed-out first response can be retried and acknowledged without another effect.
func NewHandler(secret string, accept Acceptor, status int, delay time.Duration, retryAfter string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			} else {
				http.Error(w, "invalid body", http.StatusBadRequest)
			}
			return
		}
		for _, name := range []string{webhook.HeaderID, webhook.HeaderTimestamp, webhook.HeaderSignature} {
			if len(r.Header.Values(name)) != 1 {
				http.Error(w, "invalid signature", http.StatusUnauthorized)
				return
			}
		}
		if err := webhook.Verify(secret, r.Header.Get(webhook.HeaderID), r.Header.Get(webhook.HeaderTimestamp),
			r.Header.Get(webhook.HeaderSignature), body, time.Now()); err != nil {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		if status >= 200 && status < 300 {
			id, _ := strconv.ParseUint(r.Header.Get(webhook.HeaderID), 10, 64) // Verified canonical ID.
			duplicate, err := accept(r.Context(), id, body)
			if errors.Is(err, ErrPayloadConflict) {
				http.Error(w, "delivery payload conflict", http.StatusConflict)
				return
			}
			if err != nil {
				// Never expose DB errors, credentials, payload or signature.
				http.Error(w, "receiver storage unavailable", http.StatusServiceUnavailable)
				return
			}
			slog.Info("webhook accepted", "delivery_id", id, "duplicate", duplicate)
			if duplicate {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			w.WriteHeader(status)
		case <-r.Context().Done():
			// The transaction may already have committed; do not undo it.
		}
	})
	return mux
}
