package api

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics groups the Prometheus collectors the service exports.
type Metrics struct {
	Registry  *prometheus.Registry
	requests  *prometheus.CounterVec
	duration  *prometheus.HistogramVec
	decisions *prometheus.CounterVec
}

// NewMetrics registers collectors on a fresh registry. activeBuckets is
// sampled on every scrape.
func NewMetrics(version string, activeBuckets func() float64) *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		Registry: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP requests by route, method and status code.",
		}, []string{"route", "method", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_request_duration_seconds",
			Help: "HTTP request latency by route and method.",
			// Bucket edges include the 0.1s latency SLO threshold.
			Buckets: []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
		}, []string{"route", "method"}),
		decisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "burnrate_decisions_total",
			Help: "Rate-limit decisions by outcome.",
		}, []string{"decision"}),
	}
	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "burnrate_build_info",
		Help: "Build information; value is always 1.",
	}, []string{"version"})
	buildInfo.WithLabelValues(version).Set(1)

	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.requests, m.duration, m.decisions, buildInfo,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "burnrate_active_buckets",
			Help: "Token buckets currently held in memory.",
		}, activeBuckets),
	)
	// Pre-create both decision series so rate() works before the first denial.
	m.decisions.WithLabelValues("allowed")
	m.decisions.WithLabelValues("denied")
	return m
}
