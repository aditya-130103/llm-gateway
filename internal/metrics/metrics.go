package metrics

import "github.com/prometheus/client_golang/prometheus/promauto"
import "github.com/prometheus/client_golang/prometheus"

var (
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llmgw_requests_total",
		Help: "Total requests, labeled by team, provider, and status code.",
	}, []string{"team", "provider", "status_code"})

	TokensTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llmgw_tokens_total",
		Help: "Total tokens consumed, labeled by team, provider, and token type.",
	}, []string{"team", "provider", "type"})

	CacheHitsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llmgw_cache_hits_total",
		Help: "Total semantic cache hits, labeled by team.",
	}, []string{"team"})

	RequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "llmgw_request_duration_seconds",
		Help:    "Request latency in seconds, labeled by team and provider.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10},
	}, []string{"team", "provider"})
)
