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
	"sync"
	"time"

	"github.com/deusyu/rc_deusyu/internal/model"
	"github.com/deusyu/rc_deusyu/internal/store"
)

type Worker struct {
	store    *store.Store
	client   *http.Client
	interval time.Duration
	batch    int
	wg       sync.WaitGroup
}

func New(s *store.Store) *Worker {
	return &Worker{
		store:    s,
		client:   &http.Client{Timeout: 10 * time.Second},
		interval: 1 * time.Second,
		batch:    20,
	}
}

// Run polls for pending notifications and delivers them. It blocks until
// ctx is cancelled, then waits for in-flight deliveries to finish.
func (w *Worker) Run(ctx context.Context) {
	log.Println("worker started")

	// Recover any notifications stuck in 'delivering' from a previous crash.
	if n, err := w.store.RecoverStale(2 * time.Minute); err != nil {
		log.Printf("ERROR recover stale: %v", err)
	} else if n > 0 {
		log.Printf("recovered %d stale delivering notifications", n)
	}

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("worker stopping, waiting for in-flight deliveries...")
			w.wg.Wait()
			log.Println("worker stopped")
			return
		case <-ticker.C:
			w.poll(ctx)
		}
	}
}

// Done returns a channel that can be used to wait for the worker to finish
// after its context is cancelled. Callers should cancel the context first.
func (w *Worker) Done() <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(ch)
	}()
	return ch
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
			w.deliver(ctx, n)
		}
	}
}

func (w *Worker) deliver(ctx context.Context, n *model.Notification) {
	// Atomically claim the notification so no other worker can pick it up.
	claimed, err := w.store.Claim(n.ID)
	if err != nil {
		log.Printf("ERROR claim %s: %v", n.ID, err)
		return
	}
	if !claimed {
		return // already claimed by another worker
	}

	w.wg.Add(1)
	defer w.wg.Done()

	deliverErr := w.doHTTP(ctx, n)
	if deliverErr == nil {
		if dbErr := w.store.UpdateStatus(n.ID, model.StatusDelivered, "", nil, n.RetryCount); dbErr != nil {
			log.Printf("ERROR update delivered %s: %v", n.ID, dbErr)
		} else {
			log.Printf("delivered %s -> %s", n.ID, n.TargetURL)
		}
		return
	}

	n.RetryCount++
	errMsg := deliverErr.Error()
	if n.RetryCount > n.MaxRetries {
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

func (w *Worker) doHTTP(ctx context.Context, n *model.Notification) error {
	var bodyReader io.Reader
	if n.Body != "" {
		bodyReader = strings.NewReader(n.Body)
	}

	req, err := http.NewRequestWithContext(ctx, n.Method, n.TargetURL, bodyReader)
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

// nextRetryTime calculates exponential backoff with positive jitter.
// Base delay: 5s, then 10s, 20s, 40s, 80s...
// Jitter adds 0–30% of the base delay to avoid thundering herd.
func nextRetryTime(retryCount int) time.Time {
	base := 5.0 * math.Pow(2, float64(retryCount-1))
	jitter := base * 0.3 * rand.Float64()
	delay := time.Duration(base+jitter) * time.Second
	return time.Now().UTC().Add(delay)
}
