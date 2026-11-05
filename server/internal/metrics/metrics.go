// Package metrics exposes Prometheus metrics for the server role (tech
// design 15.6): HTTP traffic by contract operation, the run queue and
// runner capacity read from the database at scrape time, lease
// expirations, and the database connection pool.
//
// Queue and capacity figures come from the shared database, not from
// process memory, so every server of a multi-node deployment reports the
// same deployment-wide values; aggregate them with max(), not sum().
//
// A nil *Metrics is valid and records nothing, so callers need not check
// whether metrics are enabled.
package metrics

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Namespace prefixes every lerryn metric.
const Namespace = "lerryn"

// Metrics holds the registry and the metrics updated by hooks.
type Metrics struct {
	reg              *prometheus.Registry
	httpRequests     *prometheus.CounterVec
	httpDuration     *prometheus.HistogramVec
	httpInFlight     prometheus.Gauge
	leaseExpirations prometheus.Counter
	retentionRemoved *prometheus.CounterVec
	retentionBytes   *prometheus.CounterVec
}

// New returns metrics on a fresh registry with the Go runtime and process
// collectors registered.
func New() *Metrics {
	m := &Metrics{
		reg: prometheus.NewRegistry(),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "HTTP requests served, by contract operation and status code.",
		}, []string{"operation", "code"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace,
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "Time to serve HTTP requests, by contract operation. Long polls and streams count their full duration.",
			Buckets:   []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60},
		}, []string{"operation"}),
		httpInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "http",
			Name:      "requests_in_flight",
			Help:      "HTTP requests being served, including long polls and streams.",
		}),
		leaseExpirations: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "runs",
			Name:      "lease_expirations_total",
			Help:      "Runs this server marked interrupted because their runner stopped renewing the lease.",
		}),
		retentionRemoved: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "retention",
			Name:      "removed_total",
			Help:      "What the retention cleanup on this server removed: runs, logs, artifacts and object store objects.",
		}, []string{"kind"}),
		retentionBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "retention",
			Name:      "removed_bytes_total",
			Help:      "Size of the logs and artifacts the retention cleanup on this server removed, before deduplication.",
		}, []string{"kind"}),
	}
	m.reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.httpRequests, m.httpDuration, m.httpInFlight, m.leaseExpirations,
		m.retentionRemoved, m.retentionBytes,
	)
	// Present from the start, so a rate() over them has a baseline.
	for _, k := range []string{"runs", "logs", "artifacts", "objects"} {
		m.retentionRemoved.WithLabelValues(k)
	}
	for _, k := range []string{"logs", "artifacts"} {
		m.retentionBytes.WithLabelValues(k)
	}
	return m
}

// Registry returns the registry, for collectors registered elsewhere.
func (m *Metrics) Registry() *prometheus.Registry {
	if m == nil {
		return nil
	}
	return m.reg
}

// AddLeaseExpirations counts runs interrupted by an expired lease.
func (m *Metrics) AddLeaseExpirations(n int) {
	if m == nil || n <= 0 {
		return
	}
	m.leaseExpirations.Add(float64(n))
}

// Retention is what one retention cleanup removed.
type Retention struct {
	Runs, Logs, Artifacts, Objects int
	LogBytes, ArtifactBytes        int64
}

// AddRetention counts what a retention cleanup removed.
func (m *Metrics) AddRetention(r Retention) {
	if m == nil {
		return
	}
	for k, n := range map[string]int{"runs": r.Runs, "logs": r.Logs, "artifacts": r.Artifacts, "objects": r.Objects} {
		if n > 0 {
			m.retentionRemoved.WithLabelValues(k).Add(float64(n))
		}
	}
	for k, n := range map[string]int64{"logs": r.LogBytes, "artifacts": r.ArtifactBytes} {
		if n > 0 {
			m.retentionBytes.WithLabelValues(k).Add(float64(n))
		}
	}
}

// InstrumentHTTP wraps next to count requests and time them under the
// label that operation returns. operation must map requests onto a small,
// fixed set of names; paths carry IDs and would make every request a new
// series.
func (m *Metrics) InstrumentHTTP(next http.Handler, operation func(*http.Request) string) http.Handler {
	if m == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op := operation(r)
		start := time.Now()
		m.httpInFlight.Inc()
		rec := &statusRecorder{ResponseWriter: w}
		defer func() {
			m.httpInFlight.Dec()
			code := rec.status
			if code == 0 {
				// Nothing written: net/http answers 200.
				code = http.StatusOK
			}
			m.httpRequests.WithLabelValues(op, strconv.Itoa(code)).Inc()
			m.httpDuration.WithLabelValues(op).Observe(time.Since(start).Seconds())
		}()
		next.ServeHTTP(rec, r)
	})
}

// Handler serves the registry in the Prometheus exposition format. With a
// non-empty token, a scrape must present it as a bearer token.
func (m *Metrics) Handler(token string) http.Handler {
	h := promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{
		// A failing collector drops its own metrics, not the whole scrape.
		ErrorHandling: promhttp.ContinueOnError,
	})
	if token == "" {
		return h
	}
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// statusRecorder remembers the status code of a response. It forwards
// Flush and exposes the original writer through Unwrap, so streaming
// responses (SSE, MCP) keep working behind it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	_ = http.NewResponseController(s.ResponseWriter).Flush()
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
