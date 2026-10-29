package adaptor

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/stretchr/testify/require"
)

func TestIngressStageSelectsLargestSlowComponent(t *testing.T) {
	tests := []struct {
		name      string
		durations ingressStageDurations
		want      string
	}{
		{
			name: "router wait",
			durations: ingressStageDurations{
				routerWait: consensusIngressSlowThreshold + time.Millisecond,
				engine:     time.Millisecond,
			},
			want: "router",
		},
		{
			name: "validation queue wait",
			durations: ingressStageDurations{
				queueWait: consensusIngressSlowThreshold + time.Millisecond,
				verify:    2 * time.Millisecond,
			},
			want: "validation-queue",
		},
		{
			name: "router admission wait",
			durations: ingressStageDurations{
				admissionWait: consensusIngressSlowThreshold + time.Millisecond,
			},
			want: "admission",
		},
		{
			name: "engine takes longest",
			durations: ingressStageDurations{
				queueWait: consensusIngressSlowThreshold + time.Millisecond,
				engine:    consensusIngressSlowThreshold + 2*time.Millisecond,
			},
			want: "engine",
		},
		{
			name: "below threshold",
			durations: ingressStageDurations{
				engine: consensusIngressSlowThreshold,
			},
		},
		{
			name: "aggregate wait",
			durations: ingressStageDurations{
				routerWait: 100 * time.Millisecond,
				queueWait:  100 * time.Millisecond,
				verify:     100*time.Millisecond + time.Nanosecond,
				total:      consensusIngressSlowThreshold + time.Millisecond,
			},
			want: "aggregate",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ingressStage(tt.durations))
		})
	}
}

func TestIngressDiagnosticsRateLimit(t *testing.T) {
	var diagnostics ingressDiagnostics
	now := time.Unix(100, 0)
	require.True(t, diagnostics.allowProposal(now))
	require.False(t, diagnostics.allowProposal(now.Add(consensusIngressSlowLogInterval-time.Nanosecond)))
	require.True(t, diagnostics.allowProposal(now.Add(consensusIngressSlowLogInterval)))
	require.True(t, diagnostics.allowValidation(true, now))
	require.True(t, diagnostics.allowValidation(false, now))
}

func TestSlowProposalIngressSeparatesRouterAndEngineTime(t *testing.T) {
	var logs bytes.Buffer
	router := &Router{logger: slog.New(slog.NewTextHandler(&logs, nil))}
	started := time.Unix(100, 0)
	completed := started.Add(900 * time.Millisecond)
	router.logSlowProposalIngress(
		&peermanagement.InboundMessage{PeerID: 7, ReceivedAt: started.Add(-time.Second)},
		nil,
		started,
		completed,
		100*time.Millisecond,
		100*time.Millisecond,
		"accepted",
	)

	output := logs.String()
	require.Contains(t, output, "event=proposal-ingress-slow")
	require.Contains(t, output, "stage=router")
	require.Contains(t, output, "router_wait_us=1000000")
	require.Contains(t, output, "admission_us=100000")
	require.Contains(t, output, "engine_us=100000")
	require.Contains(t, output, "total_us=1900000")

	router.logSlowProposalIngress(
		&peermanagement.InboundMessage{PeerID: 8, ReceivedAt: started.Add(-time.Second)},
		nil,
		started,
		completed,
		100*time.Millisecond,
		100*time.Millisecond,
		"accepted",
	)
	require.Equal(t, 1, strings.Count(logs.String(), "event=proposal-ingress-slow"),
		"slow ingress logging must be rate limited")
}

func TestValidationWorkTimingLogsVerifierQueueWait(t *testing.T) {
	lane := newValidationWorkLane(func(*consensus.Validation) error { return nil }, nil, nil)
	lane.trustedWorkers = 1
	lane.untrustedWorkers = 0
	lane.start(t.Context())
	defer lane.stop()

	queuedAt := time.Now().Add(-consensusIngressSlowThreshold - time.Second)
	validation := &consensus.Validation{LedgerSeq: 42, LedgerID: consensus.LedgerID{1}}
	require.Equal(t, validationQueueAccepted, lane.submit(validationWork{
		validation: validation,
		trusted:    true,
		ingress: validationIngressTiming{
			receivedAt: queuedAt,
			enqueuedAt: queuedAt,
		},
	}))

	result := <-lane.trustedResultCh
	require.True(t, result.ingress.dequeuedAt.After(result.ingress.enqueuedAt))
	require.True(t, result.ingress.verifiedAt.After(result.ingress.dequeuedAt))

	var logs bytes.Buffer
	router := &Router{
		engine: &validationProcessorEngine{
			mockEngine: &mockEngine{},
		},
		logger: slog.New(slog.NewTextHandler(&logs, nil)),
	}
	router.handleValidationWorkResult(result)

	output := logs.String()
	require.Contains(t, output, "event=validation-ingress-slow")
	require.Contains(t, output, "stage=validation-queue")
	require.Contains(t, output, "queue_wait_us=")
	require.Contains(t, output, "verify_us=")
}

func TestSlowValidationIngressIncludesResultAndEngineDurations(t *testing.T) {
	var logs bytes.Buffer
	router := &Router{logger: slog.New(slog.NewTextHandler(&logs, nil))}
	now := time.Unix(100, 0)
	result := validationWorkResult{
		validation: &consensus.Validation{LedgerSeq: 42, LedgerID: consensus.LedgerID{2}},
		origin:     consensus.ValidationOrigin{PeerID: 9},
		trusted:    true,
		ingress: validationIngressTiming{
			receivedAt:       now.Add(-3 * time.Second),
			handlerStartedAt: now.Add(-2800 * time.Millisecond),
			enqueuedAt:       now.Add(-2600 * time.Millisecond),
			dequeuedAt:       now.Add(-1600 * time.Millisecond),
			verifiedAt:       now.Add(-1500 * time.Millisecond),
		},
	}
	router.logSlowValidationIngress(result, now, now.Add(2*time.Second), 900*time.Millisecond, "current")

	output := logs.String()
	require.Contains(t, output, "event=validation-ingress-slow")
	require.Contains(t, output, "stage=result-queue")
	require.Contains(t, output, "router_wait_us=200000")
	require.Contains(t, output, "admission_us=200000")
	require.Contains(t, output, "queue_wait_us=1000000")
	require.Contains(t, output, "verify_us=100000")
	require.Contains(t, output, "result_wait_us=1500000")
	require.Contains(t, output, "engine_us=900000")
	require.Contains(t, output, "total_us=5000000")
}

func TestSlowRejectedValidationIngressRecordsDisposition(t *testing.T) {
	adaptor := newTestAdaptor(t)
	router := newTestRouter(&mockEngine{}, adaptor, nil)
	var logs bytes.Buffer
	router.logger = slog.New(slog.NewTextHandler(&logs, nil))

	message := buildValidationAt(t, 7, time.Now().Add(-10*time.Minute))
	message.ReceivedAt = time.Now().Add(-time.Second)
	router.handleValidation(message)

	output := logs.String()
	require.Contains(t, output, "event=validation-ingress-slow")
	require.Contains(t, output, "stage=router")
	require.Contains(t, output, "outcome=not-current")
	require.Contains(t, output, "trust_checked=false")
}
