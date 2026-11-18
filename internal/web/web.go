package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func ValidID(id string) bool { return idPattern.MatchString(id) }

func Env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func JSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("response write failed", "error", err)
	}
}

func Error(w http.ResponseWriter, code int, reason string) {
	JSON(w, code, map[string]string{"error": reason})
}

func Decode(w http.ResponseWriter, r *http.Request, v any) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		Error(w, http.StatusUnsupportedMediaType, "expected_application_json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	err = d.Decode(v)
	if err == nil {
		var extra json.RawMessage
		if err = d.Decode(&extra); err == io.EOF {
			return true
		}
	}
	var limit *http.MaxBytesError
	if errors.As(err, &limit) {
		Error(w, http.StatusRequestEntityTooLarge, "body_too_large")
	} else {
		Error(w, http.StatusBadRequest, "invalid_json")
	}
	return false
}

func Router(name string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Request-ID", middleware.GetReqID(r.Context()))
			next.ServeHTTP(w, r)
		})
	})
	r.Use(middleware.Logger, middleware.Recoverer)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) { Error(w, 404, "not_found") })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) { Error(w, 405, "method_not_allowed") })
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		JSON(w, 200, map[string]string{"service": name, "status": "ok"})
	})
	return r
}

func Serve(name, fallback string, router http.Handler) error {
	addr := Env("ADDR", fallback)
	s := &http.Server{
		Addr: addr, Handler: router, ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errs := make(chan error, 1)
	go func() {
		slog.Info("listening", "service", name, "addr", addr)
		errs <- s.ListenAndServe()
	}()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		end, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		return s.Shutdown(end)
	}
}

// BaseURL normalizes a configured service address, not a caller-provided URL.
func BaseURL(v string) string { return strings.TrimRight(v, "/") }
