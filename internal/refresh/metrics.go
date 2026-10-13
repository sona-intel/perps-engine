package refresh

import (
	"github.com/prometheus/client_golang/prometheus"
)

type metrics struct {
	duration     prometheus.Histogram
	errors       *prometheus.CounterVec
	positions    prometheus.Gauge
	liquidations prometheus.Gauge
}

// newMetrics registers on reg, or on a private registry when reg is nil
func newMetrics(reg prometheus.Registerer, r *Runner) (*metrics, error) {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}
	m := &metrics{
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "perps_refresh_duration_seconds",
			Help:    "Duration of one snapshot refresh",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 4, 8, 15, 30, 60},
		}),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "perps_refresh_errors_total",
			Help: "Errors by stage, the previous snapshot stays served after a refresh error",
		}, []string{"stage"}),
		positions: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "perps_positions",
			Help: "Open positions in the latest snapshot",
		}),
		liquidations: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "perps_liquidations_indexed",
			Help: "Liquidation events stored",
		}),
	}
	age := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "perps_snapshot_age_seconds",
		Help: "Seconds since the latest snapshot was taken, 0 before the first one",
	}, func() float64 {
		s := r.latest.Load()
		if s == nil {
			return 0
		}
		return r.now().Sub(s.TakenAt).Seconds()
	})
	last := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "perps_last_refresh_success_timestamp_seconds",
		Help: "Unix time of the latest snapshot, 0 before the first one",
	}, func() float64 {
		s := r.latest.Load()
		if s == nil {
			return 0
		}
		return float64(s.TakenAt.Unix())
	})
	paid := prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "perps_paid_rpc_calls_total",
		Help: "RPC responses that were served by a paid provider",
	}, func() float64 { return float64(r.chain.PaidCalls()) })
	for _, c := range []prometheus.Collector{m.duration, m.errors, m.positions, m.liquidations, age, last, paid} {
		if err := reg.Register(c); err != nil {
			return nil, err
		}
	}
	return m, nil
}
