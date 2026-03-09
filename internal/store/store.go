package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/deusyu/rc_deusyu/internal/model"
	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db *sql.DB
}

func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS notifications (
			id          TEXT PRIMARY KEY,
			target_url  TEXT NOT NULL,
			method      TEXT NOT NULL DEFAULT 'POST',
			headers     TEXT,
			body        TEXT,
			status      TEXT NOT NULL DEFAULT 'pending',
			retry_count INTEGER NOT NULL DEFAULT 0,
			max_retries INTEGER NOT NULL DEFAULT 5,
			next_retry_at DATETIME,
			created_at  DATETIME NOT NULL,
			updated_at  DATETIME NOT NULL,
			last_error  TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_status_next_retry
			ON notifications(status, next_retry_at);
	`)
	return err
}

func (s *Store) Create(n *model.Notification) error {
	headers, _ := json.Marshal(n.Headers)
	_, err := s.db.Exec(`
		INSERT INTO notifications (id, target_url, method, headers, body, status, retry_count, max_retries, next_retry_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ID, n.TargetURL, n.Method, string(headers), n.Body,
		n.Status, n.RetryCount, n.MaxRetries, n.NextRetryAt,
		n.CreatedAt, n.UpdatedAt,
	)
	return err
}

func (s *Store) GetByID(id string) (*model.Notification, error) {
	row := s.db.QueryRow(`SELECT id, target_url, method, headers, body, status, retry_count, max_retries, next_retry_at, created_at, updated_at, last_error FROM notifications WHERE id = ?`, id)
	return scanNotification(row)
}

// FetchReady retrieves up to `limit` notifications that are ready for delivery.
func (s *Store) FetchReady(limit int) ([]*model.Notification, error) {
	now := time.Now().UTC()
	rows, err := s.db.Query(`
		SELECT id, target_url, method, headers, body, status, retry_count, max_retries, next_retry_at, created_at, updated_at, last_error
		FROM notifications
		WHERE status = 'pending'
		  AND (next_retry_at IS NULL OR next_retry_at <= ?)
		ORDER BY next_retry_at ASC, created_at ASC
		LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*model.Notification
	for rows.Next() {
		n, err := scanNotificationRows(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, n)
	}
	return result, rows.Err()
}

// Claim atomically transitions a pending notification to delivering.
// Returns false if the row was already claimed by another worker.
func (s *Store) Claim(id string) (bool, error) {
	res, err := s.db.Exec(`
		UPDATE notifications
		SET status = 'delivering', updated_at = ?
		WHERE id = ? AND status IN ('pending')`,
		time.Now().UTC(), id,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) UpdateStatus(id string, status model.Status, lastError string, nextRetryAt *time.Time, retryCount int) error {
	_, err := s.db.Exec(`
		UPDATE notifications
		SET status = ?, last_error = ?, next_retry_at = ?, retry_count = ?, updated_at = ?
		WHERE id = ?`,
		status, lastError, nextRetryAt, retryCount, time.Now().UTC(), id,
	)
	return err
}

// RecoverStale resets notifications stuck in 'delivering' for longer than
// the given timeout back to 'pending' so they can be retried.
func (s *Store) RecoverStale(timeout time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-timeout)
	res, err := s.db.Exec(`
		UPDATE notifications
		SET status = 'pending', updated_at = ?
		WHERE status = 'delivering' AND updated_at < ?`,
		time.Now().UTC(), cutoff,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) Close() error {
	return s.db.Close()
}

type scannable interface {
	Scan(dest ...any) error
}

func scanInto(sc scannable) (*model.Notification, error) {
	var n model.Notification
	var headers sql.NullString
	var nextRetry sql.NullTime
	var lastError sql.NullString

	err := sc.Scan(&n.ID, &n.TargetURL, &n.Method, &headers, &n.Body,
		&n.Status, &n.RetryCount, &n.MaxRetries, &nextRetry,
		&n.CreatedAt, &n.UpdatedAt, &lastError)
	if err != nil {
		return nil, err
	}
	if headers.Valid {
		_ = json.Unmarshal([]byte(headers.String), &n.Headers)
	}
	if nextRetry.Valid {
		n.NextRetryAt = &nextRetry.Time
	}
	if lastError.Valid {
		n.LastError = lastError.String
	}
	return &n, nil
}

func scanNotification(row *sql.Row) (*model.Notification, error) {
	return scanInto(row)
}

func scanNotificationRows(rows *sql.Rows) (*model.Notification, error) {
	return scanInto(rows)
}
