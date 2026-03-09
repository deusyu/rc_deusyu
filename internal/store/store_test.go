package store

import (
	"testing"
	"time"

	"github.com/deusyu/rc_deusyu/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCreateAndGet(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	n := &model.Notification{
		ID: "test-1", TargetURL: "https://example.com",
		Method: "POST", Status: model.StatusPending,
		MaxRetries: 3, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.Create(n); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetByID("test-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TargetURL != "https://example.com" {
		t.Errorf("target_url = %q, want %q", got.TargetURL, "https://example.com")
	}
	if got.Status != model.StatusPending {
		t.Errorf("status = %q, want %q", got.Status, model.StatusPending)
	}
}

func TestClaimPreventsDoublePick(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	n := &model.Notification{
		ID: "claim-1", TargetURL: "https://example.com",
		Method: "POST", Status: model.StatusPending,
		MaxRetries: 3, CreatedAt: now, UpdatedAt: now,
	}
	s.Create(n)

	ok1, err := s.Claim("claim-1")
	if err != nil || !ok1 {
		t.Fatalf("first claim should succeed, got ok=%v err=%v", ok1, err)
	}

	ok2, _ := s.Claim("claim-1")
	if ok2 {
		t.Error("second claim should fail but succeeded")
	}

	got, _ := s.GetByID("claim-1")
	if got.Status != model.StatusDelivering {
		t.Errorf("status = %q, want %q", got.Status, model.StatusDelivering)
	}
}

func TestFetchReadyRespectsNextRetryAt(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	// Ready now
	s.Create(&model.Notification{
		ID: "ready", TargetURL: "https://example.com",
		Method: "POST", Status: model.StatusPending,
		MaxRetries: 3, CreatedAt: now, UpdatedAt: now,
	})

	// Not ready yet (retry in the future)
	future := now.Add(1 * time.Hour)
	n2 := &model.Notification{
		ID: "not-ready", TargetURL: "https://example.com",
		Method: "POST", Status: model.StatusPending,
		NextRetryAt: &future,
		MaxRetries: 3, CreatedAt: now, UpdatedAt: now,
	}
	s.Create(n2)

	tasks, err := s.FetchReady(10)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != "ready" {
		t.Errorf("expected 1 ready task, got %d", len(tasks))
	}
}

func TestRecoverStale(t *testing.T) {
	s := newTestStore(t)
	past := time.Now().UTC().Add(-10 * time.Minute)
	n := &model.Notification{
		ID: "stale-1", TargetURL: "https://example.com",
		Method: "POST", Status: model.StatusPending,
		MaxRetries: 3, CreatedAt: past, UpdatedAt: past,
	}
	s.Create(n)
	// Manually set to delivering with old updated_at
	s.db.Exec(`UPDATE notifications SET status = 'delivering', updated_at = ? WHERE id = ?`, past, "stale-1")

	recovered, err := s.RecoverStale(2 * time.Minute)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered != 1 {
		t.Errorf("recovered = %d, want 1", recovered)
	}

	got, _ := s.GetByID("stale-1")
	if got.Status != model.StatusPending {
		t.Errorf("status = %q, want %q", got.Status, model.StatusPending)
	}
}

func TestUpdateStatusToFailed(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	s.Create(&model.Notification{
		ID: "fail-1", TargetURL: "https://example.com",
		Method: "POST", Status: model.StatusPending,
		MaxRetries: 1, CreatedAt: now, UpdatedAt: now,
	})

	err := s.UpdateStatus("fail-1", model.StatusFailed, "unexpected status 500", nil, 1)
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	got, _ := s.GetByID("fail-1")
	if got.Status != model.StatusFailed {
		t.Errorf("status = %q, want %q", got.Status, model.StatusFailed)
	}
	if got.LastError != "unexpected status 500" {
		t.Errorf("last_error = %q, want %q", got.LastError, "unexpected status 500")
	}
	if got.RetryCount != 1 {
		t.Errorf("retry_count = %d, want 1", got.RetryCount)
	}
}
