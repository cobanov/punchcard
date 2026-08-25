package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics holds the process-wide Prometheus registry and the collectors the
// application updates directly. Route-level HTTP metrics are recorded by the
// middleware in the http package using HTTPRequests / HTTPDuration.
type Metrics struct {
	Registry     *prometheus.Registry
	HTTPRequests *prometheus.CounterVec
	HTTPDuration *prometheus.HistogramVec
}

// NewMetrics creates a fresh registry with the standard Go runtime and process
// collectors plus the HTTP request instruments.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	httpRequests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests by route, method, and status class.",
	}, []string{"route", "method", "status"})

	httpDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency by route and method.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route", "method"})

	reg.MustRegister(httpRequests, httpDuration)

	return &Metrics{Registry: reg, HTTPRequests: httpRequests, HTTPDuration: httpDuration}
}
