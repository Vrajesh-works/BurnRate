package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vrajesh-works/BurnRate/internal/limiter"
)

func newTestServer(t *testing.T, def limiter.Policy, faults Faults) (*Server, *bytes.Buffer) {
	t.Helper()
	ps, err := limiter.NewPolicyStore(def)
	if err != nil {
		t.Fatal(err)
	}
	lim := limiter.New(limiter.NewMemoryStore(), ps, nil)
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	m := NewMetrics("test", func() float64 { return float64(lim.ActiveBuckets()) })
	return NewServer(lim, log, m, faults), logs
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestCheckAllowsThenDenies(t *testing.T) {
	s, _ := newTestServer(t, limiter.Policy{Capacity: 2, RefillPerSecond: 1}, Faults{})
	h := s.Handler()
	body := `{"tenant":"acme","key":"card-auth"}`

	for i := 0; i < 2; i++ {
		rec := do(t, h, http.MethodPost, "/v1/check", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
		}
		got := decode[checkResponse](t, rec)
		if !got.Allowed {
			t.Fatalf("check %d denied", i)
		}
		if rec.Header().Get("RateLimit-Limit") != "2" {
			t.Fatalf("RateLimit-Limit=%q", rec.Header().Get("RateLimit-Limit"))
		}
	}

	rec := do(t, h, http.MethodPost, "/v1/check", body)
	got := decode[checkResponse](t, rec)
	if rec.Code != http.StatusOK || got.Allowed {
		t.Fatalf("status=%d allowed=%v, want 200 denied", rec.Code, got.Allowed)
	}
	if got.RetryAfterMS <= 0 || got.RetryAfterMS > 1000 {
		t.Fatalf("retry_after_ms=%d want (0,1000]", got.RetryAfterMS)
	}
	if rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After=%q want 1", rec.Header().Get("Retry-After"))
	}
	if rec.Header().Get("RateLimit-Remaining") != "0" {
		t.Fatalf("RateLimit-Remaining=%q", rec.Header().Get("RateLimit-Remaining"))
	}
}

func TestCheckRejectsBadInput(t *testing.T) {
	s, _ := newTestServer(t, limiter.Policy{Capacity: 5, RefillPerSecond: 1}, Faults{})
	h := s.Handler()
	for name, body := range map[string]string{
		"not json":       `{`,
		"unknown field":  `{"tenant":"a","key":"b","extra":1}`,
		"missing key":    `{"tenant":"a"}`,
		"cost too large": `{"tenant":"a","key":"b","cost":6}`,
		"trailing data":  `{"tenant":"a","key":"b"} {}`,
		"too large":      `{"tenant":"` + strings.Repeat("a", maxBodyBytes) + `","key":"b"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, "/v1/check", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body)
			}
			if decode[map[string]string](t, rec)["error"] == "" {
				t.Fatal("missing error message")
			}
		})
	}
}

func TestPolicyRoundTrip(t *testing.T) {
	s, _ := newTestServer(t, limiter.Policy{Capacity: 5, RefillPerSecond: 1}, Faults{})
	h := s.Handler()

	got := decode[policyResponse](t, do(t, h, http.MethodGet, "/v1/policies/acme", ""))
	if got.Source != "default" || got.Capacity != 5 {
		t.Fatalf("got %+v, want default policy", got)
	}

	rec := do(t, h, http.MethodPut, "/v1/policies/acme", `{"capacity":50,"refill_per_second":10}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rec.Code, rec.Body)
	}

	got = decode[policyResponse](t, do(t, h, http.MethodGet, "/v1/policies/acme", ""))
	if got.Source != "override" || got.Capacity != 50 || got.RefillPerSecond != 10 || got.Tenant != "acme" {
		t.Fatalf("got %+v, want override", got)
	}

	chk := decode[checkResponse](t, do(t, h, http.MethodPost, "/v1/check", `{"tenant":"acme","key":"k","cost":40}`))
	if !chk.Allowed || chk.Limit != 50 {
		t.Fatalf("check under new policy: %+v", chk)
	}
}

func TestPutPolicyValidation(t *testing.T) {
	s, _ := newTestServer(t, limiter.Policy{Capacity: 5, RefillPerSecond: 1}, Faults{})
	h := s.Handler()
	for _, body := range []string{`{"capacity":0,"refill_per_second":1}`, `{"capacity":1}`, `nope`} {
		if rec := do(t, h, http.MethodPut, "/v1/policies/acme", body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status=%d want 400", body, rec.Code)
		}
	}
}

func TestProbes(t *testing.T) {
	s, _ := newTestServer(t, limiter.Policy{Capacity: 5, RefillPerSecond: 1}, Faults{})
	h := s.Handler()
	if rec := do(t, h, http.MethodGet, "/healthz", ""); rec.Code != http.StatusOK {
		t.Fatalf("healthz=%d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/readyz", ""); rec.Code != http.StatusOK {
		t.Fatalf("readyz=%d", rec.Code)
	}
	s.SetReady(false)
	if rec := do(t, h, http.MethodGet, "/readyz", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz while draining=%d want 503", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/healthz", ""); rec.Code != http.StatusOK {
		t.Fatalf("healthz while draining=%d want 200", rec.Code)
	}
}

func TestMethodAndRouteErrors(t *testing.T) {
	s, _ := newTestServer(t, limiter.Policy{Capacity: 5, RefillPerSecond: 1}, Faults{})
	h := s.Handler()
	if rec := do(t, h, http.MethodGet, "/v1/check", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /v1/check=%d want 405", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET /nope=%d want 404", rec.Code)
	}
}

func TestMetricsExposeREDAndDecisions(t *testing.T) {
	s, _ := newTestServer(t, limiter.Policy{Capacity: 1, RefillPerSecond: 1}, Faults{})
	h := s.Handler()
	do(t, h, http.MethodPost, "/v1/check", `{"tenant":"a","key":"b"}`)
	do(t, h, http.MethodPost, "/v1/check", `{"tenant":"a","key":"b"}`)
	do(t, h, http.MethodGet, "/nope", "")

	body := do(t, h, http.MethodGet, "/metrics", "").Body.String()
	for _, want := range []string{
		`http_requests_total{code="200",method="POST",route="/v1/check"} 2`,
		`http_requests_total{code="404",method="GET",route="unmatched"} 1`,
		`http_request_duration_seconds_bucket{method="POST",route="/v1/check",le="0.1"} 2`,
		`burnrate_decisions_total{decision="allowed"} 1`,
		`burnrate_decisions_total{decision="denied"} 1`,
		`burnrate_active_buckets 1`,
		`burnrate_build_info{version="test"} 1`,
		`go_goroutines`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

func TestFaultInjectionReturns500AndCounts(t *testing.T) {
	s, _ := newTestServer(t, limiter.Policy{Capacity: 100, RefillPerSecond: 1}, Faults{ErrorRate: 0.5})
	vals := []float64{0.1, 0.9}
	i := 0
	s.randFloat = func() float64 { v := vals[i%2]; i++; return v }
	h := s.Handler()

	if rec := do(t, h, http.MethodPost, "/v1/check", `{"tenant":"a","key":"b"}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("first call=%d want 500", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/v1/check", `{"tenant":"a","key":"b"}`); rec.Code != http.StatusOK {
		t.Fatalf("second call=%d want 200", rec.Code)
	}
	body := do(t, h, http.MethodGet, "/metrics", "").Body.String()
	if !strings.Contains(body, `http_requests_total{code="500",method="POST",route="/v1/check"} 1`) {
		t.Error("500 not counted")
	}
}

func TestPanicIsRecoveredAndLogged(t *testing.T) {
	s, logs := newTestServer(t, limiter.Policy{Capacity: 1, RefillPerSecond: 1}, Faults{})
	mux := http.NewServeMux()
	s.route(mux, "GET /boom", func(http.ResponseWriter, *http.Request) { panic("kaboom") })
	rec := do(t, mux, http.MethodGet, "/boom", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rec.Code)
	}
	if !strings.Contains(logs.String(), "kaboom") {
		t.Fatalf("panic not logged: %s", logs)
	}
}

func TestAccessLogIsStructured(t *testing.T) {
	s, logs := newTestServer(t, limiter.Policy{Capacity: 1, RefillPerSecond: 1}, Faults{})
	do(t, s.Handler(), http.MethodPost, "/v1/check", `{"tenant":"a","key":"b"}`)
	do(t, s.Handler(), http.MethodGet, "/healthz", "")

	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d log lines, want 1 (probes are not logged): %s", len(lines), logs)
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["msg"] != "request" || entry["route"] != "/v1/check" || entry["status"] != float64(200) {
		t.Fatalf("unexpected log entry: %v", entry)
	}
	if _, ok := entry["trace_id"]; !ok {
		t.Fatal("trace_id field missing")
	}
}

func TestRouteLabel(t *testing.T) {
	for in, want := range map[string]string{
		"POST /v1/check":            "/v1/check",
		"GET /v1/policies/{tenant}": "/v1/policies/{tenant}",
		"/":                         "unmatched",
	} {
		if got := routeLabel(in); got != want {
			t.Errorf("routeLabel(%q)=%q want %q", in, got, want)
		}
	}
}
