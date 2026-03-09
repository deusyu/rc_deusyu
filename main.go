package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/deusyu/rc_deusyu/internal/api"
	"github.com/deusyu/rc_deusyu/internal/store"
	"github.com/deusyu/rc_deusyu/internal/worker"
)

func main() {
	dbPath := envOr("DB_PATH", "notifications.db")
	addr := envOr("ADDR", ":8080")

	s, err := store.New(dbPath)
	if err != nil {
		log.Fatalf("init store: %v", err)
	}
	defer s.Close()

	mux := http.NewServeMux()
	api.New(s).Register(mux)

	srv := &http.Server{Addr: addr, Handler: mux}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	w := worker.New(s)
	go w.Run(ctx)

	go func() {
		log.Printf("listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")

	// Shut down HTTP server first (stop accepting new requests).
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	srv.Shutdown(shutdownCtx)

	// Wait for in-flight worker deliveries to finish.
	workerDone := w.Done()
	select {
	case <-workerDone:
		log.Println("all in-flight deliveries completed")
	case <-time.After(15 * time.Second):
		log.Println("timed out waiting for in-flight deliveries")
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
