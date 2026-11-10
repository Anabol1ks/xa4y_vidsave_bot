// Package statshttp exposes aggregate, read-only analytics on loopback only.
// It deliberately does not expose Telegram user IDs, videos or database credentials.
package statshttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"xa4yy_vidsave/internal/storage"
)

type Snapshotter func(context.Context) (storage.AnalyticsSnapshot, error)

func validLoopbackAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func Handler(snapshot Snapshotter) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		stats, err := snapshot(ctx)
		if err != nil {
			http.Error(w, "analytics temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := json.NewEncoder(w).Encode(stats); err != nil {
			// Client aborted or transport failed; no data is mutated.
			return
		}
	})
	return mux
}

func Serve(ctx context.Context, address string, snapshot Snapshotter) error {
	if !validLoopbackAddress(address) {
		return fmt.Errorf("analytics listen address must be a literal loopback IP: %q", address)
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           Handler(snapshot),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      12 * time.Second,
		IdleTimeout:       15 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
