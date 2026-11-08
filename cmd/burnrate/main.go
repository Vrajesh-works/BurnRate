// Command burnrate runs the rate-limit decision service.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Vrajesh-works/BurnRate/internal/api"
	"github.com/Vrajesh-works/BurnRate/internal/config"
	"github.com/Vrajesh-works/BurnRate/internal/limiter"
	"github.com/Vrajesh-works/BurnRate/internal/telemetry"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		// Used by the Docker HEALTHCHECK; distroless has no curl.
		os.Exit(healthcheck(os.Getenv("PORT")))
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv, nil); err != nil {
		fmt.Fprintln(os.Stderr, "burnrate:", err)
		os.Exit(1)
	}
}

// run starts the service and blocks until ctx is cancelled and shutdown is
// complete. If ln is nil it listens on the configured port.
func run(ctx context.Context, getenv func(string) string, ln net.Listener) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	log := telemetry.NewLogger(os.Stdout, cfg.LogLevel)
	slog.SetDefault(log)

	shutdownTracing, err := telemetry.SetupTracing(ctx, version)
	if err != nil {
		return fmt.Errorf("tracing: %w", err)
	}

	policies, err := limiter.NewPolicyStore(limiter.Policy{Capacity: cfg.DefaultCapacity, RefillPerSecond: cfg.DefaultRefill})
	if err != nil {
		return fmt.Errorf("default policy: %w", err)
	}
	lim := limiter.New(limiter.NewMemoryStore(), policies, nil)
	metrics := api.NewMetrics(version, func() float64 { return float64(lim.ActiveBuckets()) })
	srv := api.NewServer(lim, log, metrics, api.Faults{ErrorRate: cfg.FaultErrorRate, Latency: cfg.FaultLatency})

	janitorCtx, stopJanitor := context.WithCancel(context.Background())
	defer stopJanitor()
	go lim.RunJanitor(janitorCtx, time.Minute, cfg.BucketIdleTTL)

	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	if ln == nil {
		ln, err = net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
		if err != nil {
			return err
		}
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.Serve(ln) }()
	log.Info("burnrate started", "addr", ln.Addr().String(), "version", version,
		"tracing", telemetry.TracingEnabled(getenv),
		"fault_error_rate", cfg.FaultErrorRate, "fault_latency", cfg.FaultLatency.String())

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	// Fail readiness first so the endpoint controller removes this pod from
	// the Service before we stop accepting connections.
	log.Info("shutdown requested, draining", "drain_delay", cfg.DrainDelay.String())
	srv.SetReady(false)
	time.Sleep(cfg.DrainDelay)

	shCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	if err := shutdownTracing(shCtx); err != nil {
		log.Warn("tracing shutdown", "err", err)
	}
	log.Info("burnrate stopped")
	return nil
}

func healthcheck(port string) int {
	if port == "" {
		port = "8080"
	}
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
