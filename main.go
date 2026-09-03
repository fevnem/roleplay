// roleplay — a character roleplay chatbot with per-character personas,
// facts memory, and a temporary in-memory session store.
//
// Production shape: one binary with an embedded frontend (go:embed). The Go
// HTTP server is hardened with timeouts, structured logging, request logging,
// panic recovery, and security headers. All provider credentials come from env.
package main

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"roleplay/api"
	"roleplay/database"
	"roleplay/model"
)

//go:embed public
var publicFS embed.FS

const (
	defaultPort      = "3000"
	shutdownTimeout  = 5 * time.Second
	serverReadHeader = 5 * time.Second
	serverRead       = 15 * time.Second
	serverWrite      = 60 * time.Second
	serverIdle       = 60 * time.Second
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	port := envOr("PORT", defaultPort)
	snapshot := os.Getenv("SNAPSHOT") // e.g. data/sessions.json; empty = pure RAM

	store := database.NewStore(snapshot)
	client := model.NewClient(model.ConfigFromEnv())
	if !client.HasAPIKey() {
		logger.Warn("MODEL_API_KEY is not set — /api/chat will return 503 until configured")
	}

	h := &api.Handler{Store: store, Client: client, Log: logger}

	// auto-forget sessions every 5 min (time-limited memory)
	go func() {
		t := time.NewTicker(5 * time.Minute)
		for range t.C {
			store.Evict()
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat", h.Chat)
	mux.HandleFunc("/api/history", h.History)
	mux.HandleFunc("/api/characters", h.Characters)
	mux.HandleFunc("/healthz", health(true))                   // liveness
	mux.HandleFunc("/readyz", ready(client))                  // readiness (dynamic)
	// serve the embedded single-page frontend at "/" (register after /api).
	sub, err := fs.Sub(publicFS, "public")
	if err != nil {
		logger.Error("embed public failed", "error", err)
		os.Exit(1)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	srv := &http.Server{
		Addr:              "0.0.0.0:" + port,
		Handler:           withMiddleware(mux, logger),
		ReadHeaderTimeout: serverReadHeader,
		ReadTimeout:       serverRead,
		WriteTimeout:      serverWrite,
		IdleTimeout:       serverIdle,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("roleplay listening", "addr", srv.Addr, "model", client.Model())
		errCh <- srv.ListenAndServe()
	}()

	// graceful shutdown → snapshot memory so it survives restart
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	case sig := <-stop:
		logger.Info("shutting down", "signal", sig.String())
	}

	// Drain in-flight requests FIRST so any writes they make land in the store,
	// THEN snapshot — otherwise restart would lose those final turns.
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	err = srv.Shutdown(ctx)
	cancel()
	if err != nil {
		logger.Error("forced shutdown", "error", err)
	}
	store.Save()
	logger.Info("bye")
}

// envOr returns the value of env var key, or fallback when unset/blank.
func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// health returns a liveness probe handler: 200 when ok, else 503.
func health(ok bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not_ready"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}

// ready returns a readiness probe: 200 only when the provider is configured so
// a load balancer or orchestrator won't route traffic to an app that can't serve.
func ready(client *model.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !client.HasAPIKey() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not_ready","reason":"provider_not_configured"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}

// withMiddleware wraps mux with panic recovery, request logging, and security
// headers. Middleware is applied outside-in: recovery outermost, logging, then
// headers.
func withMiddleware(next http.Handler, logger *slog.Logger) http.Handler {
	return secureHeaders(
		requestLogger(logger,
			recoverer(logger, next),
		),
	)
}

// recoverer converts panics into 500s and logs a stack trace.
func recoverer(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("panic recovered", "value", rec, "stack", string(debug.Stack()))
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requestLogger logs a line per request with method, path, remote, and status.
func requestLogger(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"status", sw.status,
			"dur", time.Since(start).Round(time.Microsecond).String(),
		)
	})
}

// statusWriter captures the response status code for logging. It forwards
// Flush so SSE streaming keeps working through the middleware chain.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// secureHeaders sets conservative, non-breaking security headers on every reply.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}