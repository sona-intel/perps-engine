package server

import (
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	Registry *prometheus.Registry
	requests *prometheus.HistogramVec
}

// NewMetrics leaves the snapshot age, positions and paid calls to the refresh package, which registers them in the same registry
func NewMetrics(src Source) *Metrics {
	m := &Metrics{
		Registry: prometheus.NewRegistry(),
		requests: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "perps_request_duration_seconds",
			Help:    "Duration of served requests",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}, []string{"route", "code"}),
	}
	snapshot := func(f func(*Snapshot) float64) func() float64 {
		return func() float64 {
			if s := src.Snapshot(); s != nil {
				return f(s)
			}
			return 0
		}
	}
	m.Registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}), m.requests,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "perps_snapshot_block", Help: "Block of the latest snapshot, 0 before the first"},
			snapshot(func(s *Snapshot) float64 { return float64(s.Block) })),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "perps_markets", Help: "Markets with a mark price in the latest snapshot"},
			snapshot(func(s *Snapshot) float64 { return float64(len(s.priced())) })))
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}

func (m *Metrics) observe(route string, status int, seconds float64) {
	m.requests.WithLabelValues(route, strconv.Itoa(status)).Observe(seconds)
}
