package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	PolicyRequestTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "policy_execution_total",
			Help: "Number of total executed policies by type, name and response code.",
		},
		[]string{"type", "name", "code"},
	)

	PolicyRequestLatencies = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "policy_execution_duration_seconds",
			Help: "Response latency distribution in seconds for the executed policies by type and name.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.15, 0.2, 0.25, 0.3, 0.35, 0.4, 0.45, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0,
				1.25, 1.5, 1.75, 2.0, 2.5, 3.0, 3.5, 4.0, 4.5, 5, 6, 7, 8, 9, 10, 15, 20, 25, 30, 40, 50, 60},
		},
		[]string{"type", "name"},
	)
)

func init() {
	// Register custom metrics with the global prometheus registry
	metrics.Registry.MustRegister(PolicyRequestTotal, PolicyRequestLatencies)
}
