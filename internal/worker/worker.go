package worker

import (
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/deusyu/rc_deusyu/internal/model"
	"github.com/deusyu/rc_deusyu/internal/store"
)

type Worker struct {
	store    *store.Store
	client   *http.Client
	interval time.Duration
	batch    int
}

func New(s *store.Store) *Worker {
	return &Worker{
		store:    s,
		client:   &http.Client{Timeout: 10 * time.Second},
		interval: 1 * time.Second,
		batch:    20,
	}
}

func (w *Worker) Run(ctx context.Context) {
	log.Println("worker started")
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("worker stopping")
			return
		case <-ticker.C:
			w.poll(ctx)
		}
	}
}

func (w *Worker) poll(ctx context.Context) {
	tasks, err := w.store.FetchReady(w.batch)
	if err != nil {
		log.Printf("ERROR fetch ready: %v", err)
		return
	}
	for _, n := range tasks {
		select {
		case <-ctx.Done():
			return
		default:
			w.deliver(n)
		}
	}
}

func (w *Worker) deliver(n *model.Notification) {
	err := w.doHTTP(n)
	if err == nil {
		if dbErr := w.store.UpdateStatus(n.ID, model.StatusDelivered, "", nil, n.RetryCount); dbErr != nil {
			log.Printf("ERROR update delivered %s: %v", n.ID, dbErr)
		} else {
			log.Printf("delivered %s -> %s", n.ID, n.TargetURL)
		}
		return
	}

	n.RetryCount++
	errMsg := err.Error()
	if n.RetryCount >= n.MaxRetries {
		if dbErr := w.store.UpdateStatus(n.ID, model.StatusFailed, errMsg, nil, n.RetryCount); dbErr != nil {
			log.Printf("ERROR update failed %s: %v", n.ID, dbErr)
		}
		log.Printf("FAILED %s after %d retries: %s", n.ID, n.RetryCount, errMsg)
		return
	}

	next := nextRetryTime(n.RetryCount)
	if dbErr := w.store.UpdateStatus(n.ID, model.StatusPending, errMsg, &next, n.RetryCount); dbErr != nil {
		log.Printf("ERROR update retry %s: %v", n.ID, dbErr)
	}
	log.Printf("retry %d/%d for %s, next at %s: %s", n.RetryCount, n.MaxRetries, n.ID, next.Format(time.RFC3339), errMsg)
}

func (w *Worker) doHTTP(n *model.Notification) error {
	var bodyReader io.Reader
	if n.Body != "" {
		bodyReader = strings.NewReader(n.Body)
	}

	req, err := http.NewRequest(n.Method, n.TargetURL, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	for k, v := range n.Headers {
		req.Header.Set(k, v)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("http error: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("unexpected status %d", resp.StatusCode)
}

// nextRetryTime calculates exponential backoff with jitter.
// Base delay: 5s, then 10s, 20s, 40s, 80s...
func nextRetryTime(retryCount int) time.Time {
	base := 5.0 * math.Pow(2, float64(retryCount-1))
	jitter := base * 0.3 * rand.Float64()
	delay := time.Duration(base+jitter) * time.Second
	return time.Now().UTC().Add(delay)
}
