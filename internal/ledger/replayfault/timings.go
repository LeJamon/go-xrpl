package replayfault

import "github.com/prometheus/client_golang/prometheus"

var journalTimings = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "goxrpl_replay_fault_duration_seconds",
	Help:    "Replay journal I/O, writer lock, validator fence and advisory gate observation durations; phases overlap.",
	Buckets: []float64{0.000001, 0.00001, 0.0001, 0.001, 0.01, 0.1, 1, 10},
}, []string{"phase"})

var (
	stateWaitTiming       = journalTimings.WithLabelValues("state_lock_wait")
	stateHoldTiming       = journalTimings.WithLabelValues("state_lock_hold")
	mutationFenceTiming   = journalTimings.WithLabelValues("mutation_fence_wait")
	validatorFenceTiming  = journalTimings.WithLabelValues("validator_fence_wait")
	fileSyncTiming        = journalTimings.WithLabelValues("file_sync")
	directorySyncTiming   = journalTimings.WithLabelValues("directory_sync")
	gateObservationTiming = journalTimings.WithLabelValues("gate_observation")
)

func TimingCollector() prometheus.Collector { return journalTimings }
