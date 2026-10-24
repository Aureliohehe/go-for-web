package delivery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aurelio/hookrelay/internal/store"
	"github.com/aurelio/hookrelay/internal/task"
	"github.com/hibiken/asynq"
)

type testRepository struct {
	work                *store.DeliveryWork
	claimErr, finishErr error
	status, reason      string
	httpStatus          int
	claims, finishes    int
}

func (r *testRepository) Claim(_ context.Context, id uint64, gen uint32, max uint32, lease time.Duration) (*store.DeliveryWork, error) {
	r.claims++
	return r.work, r.claimErr
}
func (r *testRepository) Finish(_ context.Context, work *store.DeliveryWork, status string, httpStatus int, reason string) error {
	r.finishes++
	r.status, r.reason, r.httpStatus = status, reason, httpStatus
	return r.finishErr
}

func TestWorker(t *testing.T) {
	for _, tt := range []struct {
		name, status                    string
		code                            int
		retryable, terminal, disallowed bool
		attempt                         uint32
		claimErr, sendErr, finishErr    error
		wantErr, skipRetry              bool
	}{
		{name: "success", status: "succeeded", code: 204, attempt: 1},
		{name: "temporary error", status: "retrying", code: 500, retryable: true, sendErr: ErrHTTPStatus, attempt: 1, wantErr: true},
		{name: "timeout", status: "retrying", retryable: true, sendErr: context.DeadlineExceeded, attempt: 1, wantErr: true},
		{name: "permanent error", status: "failed", code: 400, sendErr: ErrHTTPStatus, attempt: 1, wantErr: true, skipRetry: true},
		{name: "budget exhausted", status: "failed", code: 500, retryable: true, sendErr: ErrHTTPStatus, attempt: task.MaxAttempts, wantErr: true, skipRetry: true},
		{name: "terminal skips send", terminal: true},
		{name: "claim error", claimErr: errors.New("db error"), wantErr: true},
		{name: "save failed", status: "succeeded", code: 200, attempt: 1, finishErr: errors.New("db error"), wantErr: true},
		{name: "URL disallowed", status: "failed", disallowed: true, attempt: 1, wantErr: true, skipRetry: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &testRepository{claimErr: tt.claimErr, finishErr: tt.finishErr}
			if !tt.terminal {
				repo.work = &store.DeliveryWork{
					Delivery: store.Delivery{ID: 9, Generation: 1, AttemptCount: tt.attempt},
					Event:    store.Event{ID: 7, Payload: []byte(`{ "id": 1 }`)},
					Endpoint: store.Endpoint{URL: "http://receiver/webhook"},
				}
			}
			sends := 0
			worker := Worker{Store: repo, AllowedURL: "http://receiver/webhook", Send: func(_ context.Context, req Request) (Result, error) {
				sends++
				if req.DeliveryID != "9" || req.URL != "http://receiver/webhook" || string(req.Payload) != `{ "id": 1 }` {
					t.Fatal("wrong sender input")
				}
				return Result{HTTPStatus: tt.code, Retryable: tt.retryable}, tt.sendErr
			}}
			if tt.disallowed {
				worker.AllowedURL = "http://other/webhook"
			}
			err := worker.ProcessTask(context.Background(), asynq.NewTask(task.TypeDelivery, []byte(`{"delivery_id":9,"generation":1}`)))
			if (err != nil) != tt.wantErr || errors.Is(err, asynq.SkipRetry) != tt.skipRetry || repo.status != tt.status {
				t.Fatalf("status=%s err=%v", repo.status, err)
			}
			wantSends := 1
			if tt.terminal || tt.claimErr != nil || tt.disallowed {
				wantSends = 0
			}
			if sends != wantSends {
				t.Fatalf("sends=%d want=%d", sends, wantSends)
			}
		})
	}
}

func TestWorkerInvalidTask(t *testing.T) {
	repo := &testRepository{}
	worker := Worker{Store: repo}
	for _, body := range []string{`{`, `{}`, `{"delivery_id":9}`} {
		err := worker.ProcessTask(context.Background(), asynq.NewTask(task.TypeDelivery, []byte(body)))
		if !errors.Is(err, asynq.SkipRetry) || repo.claims != 0 {
			t.Fatal("invalid tasks must not touch database")
		}
	}
}
