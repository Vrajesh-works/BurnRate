package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 8080 || c.DefaultCapacity != 100 || c.DefaultRefill != 50 ||
		c.LogLevel != slog.LevelInfo || c.DrainDelay != 5*time.Second || c.FaultErrorRate != 0 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"PORT": "9000", "LOG_LEVEL": "debug", "DEFAULT_CAPACITY": "5",
		"DEFAULT_REFILL_PER_SECOND": "2.5", "FAULT_ERROR_RATE": "0.3", "FAULT_LATENCY": "50ms",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 9000 || c.LogLevel != slog.LevelDebug || c.DefaultCapacity != 5 ||
		c.DefaultRefill != 2.5 || c.FaultErrorRate != 0.3 || c.FaultLatency != 50*time.Millisecond {
		t.Fatalf("overrides not applied: %+v", c)
	}
}

func TestInvalidValuesAreAllReported(t *testing.T) {
	_, err := Load(env(map[string]string{
		"PORT": "abc", "LOG_LEVEL": "loud", "FAULT_ERROR_RATE": "2", "BUCKET_IDLE_TTL": "soon",
	}))
	if err == nil {
		t.Fatal("want error")
	}
	for _, key := range []string{"PORT", "LOG_LEVEL", "FAULT_ERROR_RATE", "BUCKET_IDLE_TTL"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q does not mention %s", err, key)
		}
	}
}
