package mock

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics owns the Prometheus registry and exposed metrics for the mock.
// Kept on the Server (not as package globals) so tests can spin up
// independent instances without colliding on metric registration.
type Metrics struct {
	registry *prometheus.Registry

	RequestsTotal           *prometheus.CounterVec
	RequestDuration         *prometheus.HistogramVec
	WebhookDispatchTotal    *prometheus.CounterVec
	WebhookDispatchDuration prometheus.Histogram
	ProxyForwardsTotal      *prometheus.CounterVec
	ProxyBindings           prometheus.Gauge
}

// NewMetrics initializes and registers all mock-side metrics. A custom
// registry is used (instead of the default global) so the same process can
// host multiple mocks during tests without "duplicate registration" panics.
func NewMetrics(store *Store) *Metrics {
	reg := prometheus.NewRegistry()
	// Go runtime + process metrics - useful baseline for resource use.
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	m := &Metrics{
		registry: reg,
		RequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "telegym_mock_requests_total",
				Help: "Bot API method invocations served by the mock.",
			},
			[]string{"method", "status"},
		),
		RequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "telegym_mock_request_duration_seconds",
				Help:    "Duration of Bot API method handling.",
				Buckets: prometheus.ExponentialBuckets(0.0005, 2, 12), // 0.5ms .. 2s
			},
			[]string{"method"},
		),
		WebhookDispatchTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "telegym_mock_webhook_dispatches_total",
				Help: "Update deliveries to the bot-under-test webhook.",
			},
			[]string{"result"}, // "ok" | "fail" | "no_url"
		),
		WebhookDispatchDuration: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "telegym_mock_webhook_dispatch_duration_seconds",
				Help:    "Latency of webhook delivery to the bot-under-test.",
				Buckets: prometheus.ExponentialBuckets(0.001, 2, 12),
			},
		),
		ProxyForwardsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "telegym_mock_proxy_forwards_total",
				Help: "Outbound messages forwarded to registered proxy webhooks.",
			},
			[]string{"result"},
		),
		ProxyBindings: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "telegym_mock_proxy_bindings",
				Help: "Number of (token, chat_id) bindings registered to proxies.",
			},
		),
	}

	reg.MustRegister(
		m.RequestsTotal,
		m.RequestDuration,
		m.WebhookDispatchTotal,
		m.WebhookDispatchDuration,
		m.ProxyForwardsTotal,
		m.ProxyBindings,
	)
	// Pull-mode collectors for state that lives in the store, scraped fresh
	// each time Prometheus asks.
	reg.MustRegister(newFilesCollector(store.Files))
	reg.MustRegister(newMessagesCollector(store))

	return m
}

// Handler returns the http.Handler serving Prometheus text exposition for
// this Metrics instance.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// ginMiddleware observes every Bot API request and records counters +
// duration. Non-bot paths (debug API, chat UI) are skipped so metrics stay
// focused on the load surface.
func (m *Metrics) ginMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if !strings.HasPrefix(path, "/bot") {
			c.Next()
			return
		}
		method := extractBotMethodName(path)
		start := time.Now()
		c.Next()
		m.RequestsTotal.WithLabelValues(method, strconv.Itoa(c.Writer.Status())).Inc()
		m.RequestDuration.WithLabelValues(method).Observe(time.Since(start).Seconds())
	}
}

// extractBotMethodName returns just the method name from "/bot<token>/[test/]<method>".
// Returns "_unknown" rather than empty so cardinality stays bounded.
func extractBotMethodName(p string) string {
	rest := strings.TrimPrefix(p, "/bot")
	idx := strings.IndexByte(rest, '/')
	if idx < 0 {
		return "_unknown"
	}
	method := strings.TrimPrefix(rest[idx+1:], "test/")
	if method == "" {
		return "_unknown"
	}
	return method
}

// =====================================================================
// Pull-mode collectors
// =====================================================================

type filesCollector struct {
	store *FileStore
	count *prometheus.Desc
	bytes *prometheus.Desc
}

func newFilesCollector(s *FileStore) *filesCollector {
	return &filesCollector{
		store: s,
		count: prometheus.NewDesc(
			"telegym_mock_files_count",
			"Number of captured media files currently held in the in-memory store.",
			nil, nil,
		),
		bytes: prometheus.NewDesc(
			"telegym_mock_files_bytes",
			"Total bytes of captured media held in the file store.",
			nil, nil,
		),
	}
}

func (c *filesCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.count
	ch <- c.bytes
}

func (c *filesCollector) Collect(ch chan<- prometheus.Metric) {
	if c.store == nil {
		return
	}
	n, b := c.store.Stats()
	ch <- prometheus.MustNewConstMetric(c.count, prometheus.GaugeValue, float64(n))
	ch <- prometheus.MustNewConstMetric(c.bytes, prometheus.GaugeValue, float64(b))
}

// messagesCollector exposes per-bot outbound store size as a labeled gauge.
type messagesCollector struct {
	store *Store
	desc  *prometheus.Desc
}

func newMessagesCollector(s *Store) *messagesCollector {
	return &messagesCollector{
		store: s,
		desc: prometheus.NewDesc(
			"telegym_mock_messages_stored",
			"Number of outbound messages currently retained for each bot token.",
			[]string{"token"}, nil,
		),
	}
}

func (c *messagesCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *messagesCollector) Collect(ch chan<- prometheus.Metric) {
	if c.store == nil {
		return
	}
	c.store.mu.RLock()
	defer c.store.mu.RUnlock()
	for token, b := range c.store.bots {
		b.msgMu.RLock()
		n := float64(len(b.messages))
		b.msgMu.RUnlock()
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, n, maskToken(token))
	}
}

// maskToken shortens a token for label cardinality safety - the bot ID
// prefix is identifying enough without leaking the secret half.
func maskToken(t string) string {
	if idx := strings.IndexByte(t, ':'); idx > 0 {
		return t[:idx]
	}
	if len(t) <= 8 {
		return t
	}
	return t[:8]
}

// startMetricsServer spins up the /metrics HTTP listener if MetricsListen
// is non-empty. Runs in a goroutine; failures are logged but don't kill
// the main process.
func (s *Server) startMetricsServer() {
	if s.cfg.MetricsListen == "" || s.metrics == nil {
		return
	}
	addr := s.cfg.MetricsListen
	mux := http.NewServeMux()
	mux.Handle("/metrics", s.metrics.Handler())
	go func() {
		log.Printf("telegym-mock metrics on %s/metrics", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("metrics server: %v", err)
		}
	}()
}
