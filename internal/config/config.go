// Package config loads service settings from environment variables.
package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Vrajesh-works/BurnRate/internal/limiter"
)

// Config holds every runtime setting.
type Config struct {
	Port            int
	LogLevel        slog.Level
	DefaultCapacity float64
	DefaultRefill   float64
	// TenantPolicies are static per-tenant overrides shared by every
	// replica, from TENANT_POLICIES as JSON: {"acme":{"capacity":500,"refill_per_second":200}}.
	TenantPolicies  map[string]limiter.Policy
	BucketIdleTTL   time.Duration
	DrainDelay      time.Duration
	ShutdownTimeout time.Duration
	// FaultErrorRate in [0,1] makes that share of /v1/check calls fail with
	// 500. It exists to demo the Argo Rollouts canary abort and SLO alerts.
	FaultErrorRate float64
	// FaultLatency is added to every /v1/check call.
	FaultLatency time.Duration
}

// Load reads Config from getenv (os.Getenv when nil).
func Load(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	p := parser{getenv: getenv}
	c := Config{
		Port:            p.int("PORT", 8080),
		DefaultCapacity: p.float("DEFAULT_CAPACITY", 100),
		DefaultRefill:   p.float("DEFAULT_REFILL_PER_SECOND", 50),
		BucketIdleTTL:   p.duration("BUCKET_IDLE_TTL", 10*time.Minute),
		DrainDelay:      p.duration("SHUTDOWN_DRAIN_DELAY", 5*time.Second),
		ShutdownTimeout: p.duration("SHUTDOWN_TIMEOUT", 15*time.Second),
		FaultErrorRate:  p.float("FAULT_ERROR_RATE", 0),
		FaultLatency:    p.duration("FAULT_LATENCY", 0),
	}
	if raw := getenv("TENANT_POLICIES"); raw != "" {
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c.TenantPolicies); err != nil {
			p.errs = append(p.errs, fmt.Sprintf("TENANT_POLICIES: %v", err))
		}
		for tenant, pol := range c.TenantPolicies {
			if err := pol.Validate(); err != nil {
				p.errs = append(p.errs, fmt.Sprintf("TENANT_POLICIES[%s]: %v", tenant, err))
			}
		}
	}
	switch strings.ToLower(getenv("LOG_LEVEL")) {
	case "", "info":
		c.LogLevel = slog.LevelInfo
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "warn":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	default:
		p.errs = append(p.errs, fmt.Sprintf("LOG_LEVEL: unknown level %q", getenv("LOG_LEVEL")))
	}
	if c.Port < 1 || c.Port > 65535 {
		p.errs = append(p.errs, "PORT: must be 1-65535")
	}
	if c.FaultErrorRate < 0 || c.FaultErrorRate > 1 {
		p.errs = append(p.errs, "FAULT_ERROR_RATE: must be between 0 and 1")
	}
	if len(p.errs) > 0 {
		return Config{}, fmt.Errorf("invalid config: %s", strings.Join(p.errs, "; "))
	}
	return c, nil
}

type parser struct {
	getenv func(string) string
	errs   []string
}

func (p *parser) int(key string, def int) int {
	v := p.getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		p.errs = append(p.errs, fmt.Sprintf("%s: %v", key, err))
	}
	return n
}

func (p *parser) float(key string, def float64) float64 {
	v := p.getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		p.errs = append(p.errs, fmt.Sprintf("%s: %v", key, err))
	}
	return f
}

func (p *parser) duration(key string, def time.Duration) time.Duration {
	v := p.getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		p.errs = append(p.errs, fmt.Sprintf("%s: %v", key, err))
	}
	return d
}
