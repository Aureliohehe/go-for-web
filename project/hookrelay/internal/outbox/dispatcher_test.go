package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aurelio/hookrelay/internal/store"
)

type testRepository struct {
	entry                          *store.Outbox
	claimErr, publishErr, retryErr error
	published, retried             bool
	delay                          time.Duration
}

func (r *testRepository) Claim(context.Context, time.Duration) (*store.Outbox, error) {
	return r.entry, r.claimErr
}
func (r *testRepository) Published(context.Context, *store.Outbox) error {
	r.published = true
	return r.publishErr
}
func (r *testRepository) Retry(_ context.Context, _ *store.Outbox, delay time.Duration) error {
	r.retried = true
	r.delay = delay
	return r.retryErr
}

func TestDispatchOne(t *testing.T) {
	failure := errors.New("injected failure")
	for _, tt := range []struct {
		name                                           string
		empty                                          bool
		claimErr, enqueueErr, publishErr, retryErr     error
		wantEnqueue, wantPublished, wantRetry, wantErr bool
	}{
		{name: "published", wantEnqueue: true, wantPublished: true},
		{name: "idle", empty: true},
		{name: "claim failed", claimErr: failure, wantErr: true},
		{name: "queue failed", enqueueErr: failure, wantEnqueue: true, wantRetry: true, wantErr: true},
		{name: "mark failed after enqueue", publishErr: failure, wantEnqueue: true, wantPublished: true, wantErr: true},
		{name: "retry update failed", enqueueErr: failure, retryErr: failure, wantEnqueue: true, wantRetry: true, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &testRepository{claimErr: tt.claimErr, publishErr: tt.publishErr, retryErr: tt.retryErr}
			if !tt.empty {
				repo.entry = &store.Outbox{ID: 3, DeliveryID: 9, Generation: 2, PublishAttempts: 2}
			}
			enqueued := false
			d := Dispatcher{Store: repo, Enqueue: func(ctx context.Context, id uint64, generation uint32) error {
				enqueued = true
				if id != 9 || generation != 2 {
					t.Fatal("incorrect task IDs")
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > PublishTimeout {
					t.Fatal("enqueue must have a deadline")
				}
				return tt.enqueueErr
			}}
			worked, err := d.DispatchOne(context.Background())
			if enqueued != tt.wantEnqueue || repo.published != tt.wantPublished || repo.retried != tt.wantRetry || (err != nil) != tt.wantErr || worked != tt.wantEnqueue {
				t.Fatalf("worked=%v enqueued=%v published=%v retried=%v err=%v", worked, enqueued, repo.published, repo.retried, err)
			}
			if repo.retried && repo.delay != 10*time.Second {
				t.Fatal("incorrect retry delay")
			}
		})
	}
}

func TestRetryDelay(t *testing.T) {
	for attempt, want := range map[uint32]time.Duration{1: 5 * time.Second, 2: 10 * time.Second, 4: 40 * time.Second, 5: time.Minute, 1000: time.Minute} {
		if got := retryDelay(attempt); got != want {
			t.Fatalf("attempt=%d got=%v want=%v", attempt, got, want)
		}
	}
}

func TestRunCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d := Dispatcher{Store: &testRepository{}}
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not stop")
	}
}
