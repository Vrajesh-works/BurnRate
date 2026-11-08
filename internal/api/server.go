// Package api exposes the limiter over HTTP with metrics, traces and logs.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/Vrajesh-works/BurnRate/internal/limiter"
	"github.com/Vrajesh-works/BurnRate/internal/telemetry"
)

const maxBodyBytes = 4 << 10

// Faults configures deliberate failure injection for canary and SLO demos.
type Faults struct {
	ErrorRate float64
	Latency   time.Duration
}

// Server is the HTTP front end of the limiter.
type Server struct {
	lim     *limiter.Limiter
	log     *slog.Logger
	metrics *Metrics
	faults  Faults
	tracer  trace.Tracer
	ready   atomic.Bool
	// allowed maps static paths to their methods for 405 answers.
	allowed map[string][]string
	// randFloat is swappable in tests.
	randFloat func() float64
}

// NewServer builds a Server. It starts ready.
func NewServer(lim *limiter.Limiter, log *slog.Logger, metrics *Metrics, faults Faults) *Server {
	s := &Server{
		lim:       lim,
		log:       log,
		metrics:   metrics,
		faults:    faults,
		tracer:    otel.Tracer("github.com/Vrajesh-works/BurnRate/internal/api"),
		randFloat: rand.Float64,
	}
	s.ready.Store(true)
	return s
}

// SetReady flips the readiness probe. Shutdown sets it false before draining.
func (s *Server) SetReady(v bool) { s.ready.Store(v) }

// Handler returns the full HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.route(mux, "POST /v1/check", s.handleCheck)
	s.route(mux, "GET /v1/policies/{tenant}", s.handleGetPolicy)
	s.route(mux, "PUT /v1/policies/{tenant}", s.handlePutPolicy)
	s.route(mux, "GET /healthz", s.handleHealthz)
	s.route(mux, "GET /readyz", s.handleReadyz)
	s.route(mux, "GET /metrics", promhttp.HandlerFor(s.metrics.Registry, promhttp.HandlerOpts{}).ServeHTTP)
	s.route(mux, "/", func(w http.ResponseWriter, r *http.Request) {
		// The catch-all also receives known paths with the wrong method, so
		// answer those with 405 like ServeMux would on its own.
		if allow := s.allowed[r.URL.Path]; len(allow) > 0 {
			w.Header().Set("Allow", strings.Join(allow, ", "))
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeError(w, http.StatusNotFound, "not found")
	})
	return otelhttp.NewHandler(mux, "http.server",
		otelhttp.WithFilter(func(r *http.Request) bool {
			// Probes and scrapes would drown real traffic in traces.
			switch r.URL.Path {
			case "/healthz", "/readyz", "/metrics":
				return false
			}
			return true
		}),
	)
}

// route registers h under pattern, wrapped with metrics, span naming,
// panic recovery and an access log line.
func (s *Server) route(mux *http.ServeMux, pattern string, h http.HandlerFunc) {
	label := routeLabel(pattern)
	if method, path, ok := strings.Cut(pattern, " "); ok && !strings.Contains(path, "{") {
		if s.allowed == nil {
			s.allowed = make(map[string][]string)
		}
		s.allowed[path] = append(s.allowed[path], method)
	}
	mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		span := trace.SpanFromContext(r.Context())
		span.SetName(r.Method + " " + label)
		span.SetAttributes(attribute.String("http.route", label))

		defer func() {
			if p := recover(); p != nil {
				s.log.ErrorContext(r.Context(), "panic", "panic", fmt.Sprint(p), "route", label)
				if !rec.wrote {
					writeError(rec, http.StatusInternalServerError, "internal error")
				} else {
					rec.status = http.StatusInternalServerError
				}
			}
			elapsed := time.Since(start)
			code := strconv.Itoa(rec.status)
			s.metrics.requests.WithLabelValues(label, r.Method, code).Inc()
			s.metrics.duration.WithLabelValues(label, r.Method).Observe(elapsed.Seconds())
			if rec.status >= 500 {
				span.SetStatus(codes.Error, http.StatusText(rec.status))
			}
			if label == "/healthz" || label == "/readyz" || label == "/metrics" {
				return
			}
			s.log.InfoContext(r.Context(), "request",
				"method", r.Method,
				"route", label,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", float64(elapsed.Microseconds())/1000,
				"trace_id", telemetry.TraceID(r.Context()),
			)
		}()
		h(rec, r)
	}))
}

// routeLabel strips the method from a ServeMux pattern.
func routeLabel(pattern string) string {
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == ' ' {
			return pattern[i+1:]
		}
	}
	if pattern == "/" {
		return "unmatched"
	}
	return pattern
}

type checkRequest struct {
	Tenant string  `json:"tenant"`
	Key    string  `json:"key"`
	Cost   float64 `json:"cost"`
}

type checkResponse struct {
	Allowed      bool    `json:"allowed"`
	Remaining    float64 `json:"remaining"`
	Limit        float64 `json:"limit"`
	RetryAfterMS int64   `json:"retry_after_ms"`
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	if s.faults.Latency > 0 {
		time.Sleep(s.faults.Latency)
	}
	if s.faults.ErrorRate > 0 && s.randFloat() < s.faults.ErrorRate {
		writeError(w, http.StatusInternalServerError, "injected fault")
		return
	}

	var req checkRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	_, span := s.tracer.Start(r.Context(), "limiter.Check",
		trace.WithAttributes(attribute.String("tenant", req.Tenant)))
	dec, err := s.lim.Check(limiter.Request{Tenant: req.Tenant, Key: req.Key, Cost: req.Cost})
	span.SetAttributes(attribute.Bool("allowed", dec.Allowed))
	span.End()
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, limiter.ErrInvalidRequest) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
		return
	}

	outcome := "denied"
	if dec.Allowed {
		outcome = "allowed"
	}
	s.metrics.decisions.WithLabelValues(outcome).Inc()

	h := w.Header()
	h.Set("RateLimit-Limit", strconv.FormatFloat(dec.Limit, 'f', -1, 64))
	h.Set("RateLimit-Remaining", strconv.FormatFloat(dec.Remaining, 'f', -1, 64))
	if !dec.Allowed {
		h.Set("Retry-After", strconv.FormatInt(int64(math.Ceil(dec.RetryAfter.Seconds())), 10))
	}
	writeJSON(w, http.StatusOK, checkResponse{
		Allowed:      dec.Allowed,
		Remaining:    dec.Remaining,
		Limit:        dec.Limit,
		RetryAfterMS: int64(math.Ceil(float64(dec.RetryAfter) / float64(time.Millisecond))),
	})
}

type policyResponse struct {
	Tenant string `json:"tenant"`
	limiter.Policy
	Source string `json:"source"`
}

func (s *Server) handleGetPolicy(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	p, ok := s.lim.Policies().Lookup(tenant)
	src := "override"
	if !ok {
		p, src = s.lim.Policies().Default(), "default"
	}
	writeJSON(w, http.StatusOK, policyResponse{Tenant: tenant, Policy: p, Source: src})
}

func (s *Server) handlePutPolicy(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	var p limiter.Policy
	if err := decodeJSON(w, r, &p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.lim.Policies().Set(tenant, p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.log.InfoContext(r.Context(), "policy updated", "tenant", tenant,
		"capacity", p.Capacity, "refill_per_second", p.RefillPerSecond)
	writeJSON(w, http.StatusOK, policyResponse{Tenant: tenant, Policy: p, Source: "override"})
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	if !s.ready.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if dec.More() {
		return errors.New("invalid JSON body: trailing data")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.wrote = true
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
