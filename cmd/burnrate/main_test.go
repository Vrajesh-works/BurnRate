package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestRunServesAndShutsDownGracefully starts the real server on a random
// port, exercises it over TCP, then cancels and checks the readiness flip.
func TestRunServesAndShutsDownGracefully(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String()
	// No keep-alives: http.Server.Shutdown treats a connection that is still
	// in StateNew as active for up to 5s, which made this test flaky.
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	env := map[string]string{
		"SHUTDOWN_DRAIN_DELAY": "300ms",
		"DEFAULT_CAPACITY":     "3",
		"TENANT_POLICIES":      `{"vip":{"capacity":50,"refill_per_second":5}}`,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, func(k string) string { return env[k] }, ln) }()

	waitFor(t, func() bool {
		resp, err := client.Get(base + "/readyz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})

	resp, err := client.Post(base+"/v1/check", "application/json", strings.NewReader(`{"tenant":"t","key":"k"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("RateLimit-Limit") != "3" {
		t.Fatalf("check status=%d limit=%q", resp.StatusCode, resp.Header.Get("RateLimit-Limit"))
	}

	resp, err = client.Get(base + "/v1/policies/vip")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"capacity":50`) || !strings.Contains(string(body), `"source":"override"`) {
		t.Fatalf("static tenant policy not loaded: %s", body)
	}

	cancel()
	waitFor(t, func() bool {
		resp, err := client.Get(base + "/readyz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusServiceUnavailable
	})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after cancel")
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	err := run(context.Background(), func(k string) string {
		if k == "PORT" {
			return "0"
		}
		return ""
	}, nil)
	if err == nil {
		t.Fatal("want config error")
	}
}

func TestHealthcheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	if got := healthcheck(port); got != 0 {
		t.Fatalf("healthcheck=%d want 0", got)
	}
	_ = srv.Close()
	if got := healthcheck(port); got != 1 {
		t.Fatalf("healthcheck on closed port=%d want 1", got)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
