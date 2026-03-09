package model

import "time"

type Status string

const (
	StatusPending    Status = "pending"
	StatusDelivering Status = "delivering"
	StatusDelivered  Status = "delivered"
	StatusFailed     Status = "failed"
)

type Notification struct {
	ID          string            `json:"id"`
	TargetURL   string            `json:"target_url"`
	Method      string            `json:"method"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        string            `json:"body,omitempty"`
	Status      Status            `json:"status"`
	RetryCount  int               `json:"retry_count"`
	MaxRetries  int               `json:"max_retries"`
	NextRetryAt *time.Time        `json:"next_retry_at,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	LastError   string            `json:"last_error,omitempty"`
}
