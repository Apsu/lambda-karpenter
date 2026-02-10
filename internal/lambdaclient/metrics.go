package lambdaclient

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	apiRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "lambda",
			Name:      "api_requests_total",
			Help:      "Total number of Lambda API requests",
		},
		[]string{"method", "path", "status_code"},
	)

	apiRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "lambda",
			Name:      "api_request_duration_seconds",
			Help:      "Duration of Lambda API requests in seconds",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)
)

func init() {
	metrics.Registry.MustRegister(apiRequestsTotal, apiRequestDuration)
}
