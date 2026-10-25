package receiver

import (
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"sync"
	"testing"

	"github.com/aurelio/hookrelay/internal/config"
	"github.com/aurelio/hookrelay/internal/store"
	"gorm.io/gorm"
)

func TestStoreIntegration(t *testing.T) {
	if os.Getenv("HOOKRELAY_RECEIVER_INTEGRATION") != "1" {
		t.Skip("requires separate receiver test database and receiver migration")
	}
	cfg, err := config.LoadReceiverDB()
	if err != nil {
		t.Fatal("load receiver config failed")
	}
	ctx := context.Background()
	db, pool, err := store.OpenMySQL(ctx, cfg)
	if err != nil {
		t.Fatal("connect receiver database failed")
	}
	defer pool.Close()
	repo := Store{DB: db}
	newID := func(t *testing.T) uint64 {
		id := rand.Uint64() | 1
		t.Cleanup(func() {
			if err := db.Where("delivery_id = ?", id).Delete(&Effect{}).Error; err != nil {
				t.Error(err)
			}
			if err := db.Where("delivery_id = ?", id).Delete(&Receipt{}).Error; err != nil {
				t.Error(err)
			}
		})
		return id
	}
	count := func(t *testing.T, id uint64, want int64) {
		t.Helper()
		for _, model := range []any{&Receipt{}, &Effect{}} {
			var n int64
			if err := db.Model(model).Where("delivery_id = ?", id).Count(&n).Error; err != nil || n != want {
				t.Fatalf("%T count=%d want=%d err=%v", model, n, want, err)
			}
		}
	}
	t.Run("concurrent duplicates and connection restart", func(t *testing.T) {
		id := newID(t)
		body := []byte(`{ "order": 42 }`)
		const workers = 8
		duplicates, errs := make([]bool, workers), make([]error, workers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range workers {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; duplicates[i], errs[i] = repo.Accept(ctx, id, body) }()
		}
		close(start)
		wg.Wait()
		created := 0
		for i, err := range errs {
			if err != nil {
				t.Fatal(err)
			}
			if !duplicates[i] {
				created++
			}
		}
		if created != 1 {
			t.Fatalf("new writes=%d", created)
		}
		count(t, id, 1)
		var effect Effect
		if err := db.Where("delivery_id = ?", id).Take(&effect).Error; err != nil || string(effect.Payload) != string(body) {
			t.Fatal("business payload changed")
		}
		// A new pool/repository has no in-memory knowledge of the first receipt.
		db2, pool2, err := store.OpenMySQL(ctx, cfg)
		if err != nil {
			t.Fatal("reconnect failed")
		}
		defer pool2.Close()
		if duplicate, err := (Store{DB: db2}).Accept(ctx, id, body); err != nil || !duplicate {
			t.Fatal("deduplication did not survive new connection")
		}
		if _, err := repo.Accept(ctx, id, []byte(`{"order":42}`)); !errors.Is(err, ErrPayloadConflict) {
			t.Fatal("same ID with different bytes must conflict")
		}
		count(t, id, 1)
	})
	t.Run("concurrent different payloads", func(t *testing.T) {
		id := newID(t)
		start := make(chan struct{})
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i, body := range []string{`{"order":1}`, `{"order":2}`} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[i] = repo.Accept(ctx, id, []byte(body))
			}()
		}
		close(start)
		wg.Wait()
		conflicts, accepted := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				accepted++
			case errors.Is(err, ErrPayloadConflict):
				conflicts++
			default:
				t.Fatal(err)
			}
		}
		if accepted != 1 || conflicts != 1 {
			t.Fatal("different payloads must have one winner and one conflict")
		}
		count(t, id, 1)
	})
	t.Run("business write failure rolls back receipt", func(t *testing.T) {
		id := newID(t)
		injected := errors.New("injected effect write failure")
		const callback = "test:fail_receiver_effect"
		if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "receiver_effects" {
				tx.AddError(injected)
			}
		}); err != nil {
			t.Fatal(err)
		}
		_, err := repo.Accept(ctx, id, []byte(`{}`))
		db.Callback().Create().Remove(callback)
		if !errors.Is(err, injected) {
			t.Fatal("expected injected failure")
		}
		count(t, id, 0)
		if duplicate, err := repo.Accept(ctx, id, []byte(`{}`)); err != nil || duplicate {
			t.Fatal("retry after rollback must be a new write")
		}
		count(t, id, 1)
	})
}
