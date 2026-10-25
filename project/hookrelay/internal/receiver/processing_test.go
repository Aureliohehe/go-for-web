package receiver

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aurelio/hookrelay/internal/delivery"
)

func TestProcessingResults(t *testing.T) {
	for _, tt := range []struct {
		name      string
		status    int
		duplicate bool
		err       error
		want      int
		calls     int
	}{
		{"new write", 200, false, nil, 200, 1},
		{"already processed", 200, true, nil, 204, 1},
		{"payload conflict", 200, false, ErrPayloadConflict, 409, 1},
		{"DB unavailable", 200, false, errors.New("private DB detail"), 503, 1},
		{"simulated failure", 500, false, nil, 500, 0},
		{"simulated rate limit", 429, false, nil, 429, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := make(chan struct{}, 2)
			accept := func(context.Context, uint64, []byte) (bool, error) {
				calls <- struct{}{}
				return tt.duplicate, tt.err
			}
			delay := time.Duration(0)
			if tt.duplicate {
				delay = time.Hour // Duplicate acknowledgements bypass post-commit delay.
			}
			server := httptest.NewServer(NewHandler(testSecret, accept, tt.status, delay, ""))
			defer server.Close()
			result, _ := delivery.NewSender(time.Second).Send(context.Background(), delivery.Request{
				URL: server.URL + "/webhook", DeliveryID: "9", SigningSecret: testSecret, Payload: []byte(`{}`),
			})
			if result.HTTPStatus != tt.want || len(calls) != tt.calls {
				t.Fatalf("status=%d calls=%d", result.HTTPStatus, len(calls))
			}
		})
	}
}
