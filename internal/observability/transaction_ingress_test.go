package observability

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestTransactionIngressMetricsSourceReplacement(t *testing.T) {
	registry := prometheus.NewPedanticRegistry()
	require.NoError(t, registry.Register(NewTransactionIngressCollector()))
	old := SetTransactionIngressMetrics(func() TransactionIngressMetricsSnapshot {
		return TransactionIngressMetricsSnapshot{Frames: 1}
	})
	defer old()
	cleanup := SetTransactionIngressMetrics(func() TransactionIngressMetricsSnapshot {
		return TransactionIngressMetricsSnapshot{
			Frames: 10, ParseWork: 2, ParseErrors: 1,
			InputHits: 7, InputMisses: 2, InputBypass: 1, Deduped: 6,
			ParseWorkUs: 25_000, QueueLatencyUs: 50_000, QueueLatencySamples: 10,
			MemoEntries: 2, MemoPayloadBytes: 512, MemoEvictions: 3,
			PrivateAttempts: 3, StaleRetries: 1, Reparses: 2,
			PrivateAttemptDuration: 20 * time.Millisecond,
			StaleRetryDuration:     5 * time.Millisecond, ReparseDuration: 3 * time.Millisecond,
		}
	})
	defer cleanup()
	old()
	families, err := registry.Gather()
	require.NoError(t, err)
	require.Equal(t, float64(10), metricFamily(families, "goxrpl_transaction_ingress_frames_total").Metric[0].Counter.GetValue())
	require.Equal(t, float64(512), metricFamily(families, "goxrpl_transaction_ingress_memo_payload_bytes").Metric[0].Gauge.GetValue())
	require.Equal(t, 0.025, metricFamily(families, "goxrpl_transaction_ingress_parse_seconds_total").Metric[0].Counter.GetValue())
	require.Equal(t, float64(3), metricFamily(families, "goxrpl_transaction_ingress_private_attempts_total").Metric[0].Counter.GetValue())
	require.Equal(t, 0.005, metricFamily(families, "goxrpl_transaction_ingress_stale_retry_seconds_total").Metric[0].Counter.GetValue())
	wait := metricFamily(families, "goxrpl_transaction_ingress_queue_wait_seconds").Metric[0].Summary
	require.EqualValues(t, 10, wait.GetSampleCount())
	require.Equal(t, 0.05, wait.GetSampleSum())
	inputs := metricFamily(families, "goxrpl_transaction_ingress_inputs_total")
	require.Len(t, inputs.Metric, 3)
	for _, metric := range inputs.Metric {
		require.Len(t, metric.Label, 1)
		require.Equal(t, "result", metric.Label[0].GetName())
		require.Contains(t, []string{"hit", "miss", "bypass"}, metric.Label[0].GetValue())
	}
	cleanup()
	families, err = registry.Gather()
	require.NoError(t, err)
	require.Empty(t, families)
}
