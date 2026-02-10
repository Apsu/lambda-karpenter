package provider

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	instanceCreateTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "lambda",
			Name:      "instance_create_total",
			Help:      "Total number of instance create attempts",
		},
		[]string{"result"},
	)

	instanceDeleteTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "lambda",
			Name:      "instance_delete_total",
			Help:      "Total number of instance delete attempts",
		},
		[]string{"result"},
	)
)

func init() {
	metrics.Registry.MustRegister(instanceCreateTotal, instanceDeleteTotal)
}
