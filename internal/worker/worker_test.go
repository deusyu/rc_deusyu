package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/deusyu/rc_deusyu/internal/model"
	"github.com/deusyu/rc_deusyu/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestDeliverSuccess(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))
	defer srv.Close()

	s := newTestStore(t)
	now := time.Now().UTC()
	s.Create(&model.Notification{
		ID: "ok-1", TargetURL: srv.URL, Method: "POST",
		Status: model.StatusPending, MaxRetries: 3,
		CreatedAt: now, UpdatedAt: now,
	})

	w := New(s)
	ctx := context.Background()
	w.poll(ctx)

	if !called {
		t.Error("external server was not called")
	}
	got, _ := s.GetByID("ok-1")
	if got.Status != model.StatusDelivered {
		t.Errorf("status = %q, want %q", got.Status, model.StatusDelivered)
	}
}

func TestDeliverRetryThenFail(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(500)
	}))
	defer srv.Close()

	s := newTestStore(t)
	now := time.Now().UTC()
	s.Create(&model.Notification{
		ID: "fail-1", TargetURL: srv.URL, Method: "POST",
		Status: model.StatusPending, MaxRetries: 1,
		CreatedAt: now, UpdatedAt: now,
	})

	w := New(s)
	ctx := context.Background()

	// First attempt: should fail and schedule retry
	w.poll(ctx)
	got, _ := s.GetByID("fail-1")
	if got.Status != model.StatusPending {
		t.Fatalf("after 1st attempt: status = %q, want %q", got.Status, model.StatusPending)
	}
	if got.RetryCount != 1 {
		t.Fatalf("after 1st attempt: retry_count = %d, want 1", got.RetryCount)
	}

	// Fast-forward next_retry_at so it becomes ready again
	past := time.Now().UTC().Add(-1 * time.Minute)
	s.UpdateStatus("fail-1", model.StatusPending, got.LastError, &past, got.RetryCount)

	// Second attempt: should reach max_retries and fail permanently
	w.poll(ctx)
	got, _ = s.GetByID("fail-1")
	if got.Status != model.StatusFailed {
		t.Errorf("after 2nd attempt: status = %q, want %q", got.Status, model.StatusFailed)
	}
	if callCount != 2 {
		t.Errorf("call_count = %d, want 2", callCount)
	}
}

func TestDeliverRespectsContextCancellation(t *testing.T) {
	// Server that blocks forever
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	s := newTestStore(t)
	now := time.Now().UTC()
	s.Create(&model.Notification{
		ID: "ctx-1", TargetURL: srv.URL, Method: "POST",
		Status: model.StatusPending, MaxRetries: 1,
		CreatedAt: now, UpdatedAt: now,
	})

	w := New(s)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	w.poll(ctx)
	// Should complete without hanging — the context cancellation terminates the request
}
