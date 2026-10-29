package adaptor

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
)

const (
	consensusIngressSlowThreshold   = 250 * time.Millisecond
	consensusIngressSlowLogInterval = time.Second
)

type ingressDiagnostics struct {
	lastProposalSlowLog            atomic.Int64
	lastTrustedValidationSlowLog   atomic.Int64
	lastUntrustedValidationSlowLog atomic.Int64
}

func allowSlowIngressLog(last *atomic.Int64, now time.Time) bool {
	nowNS := now.UnixNano()
	previous := last.Load()
	for {
		if previous != 0 && nowNS >= previous && nowNS-previous < consensusIngressSlowLogInterval.Nanoseconds() {
			return false
		}
		if last.CompareAndSwap(previous, nowNS) {
			return true
		}
		previous = last.Load()
	}
}

func (d *ingressDiagnostics) allowProposal(now time.Time) bool {
	return allowSlowIngressLog(&d.lastProposalSlowLog, now)
}

func (d *ingressDiagnostics) allowValidation(trusted bool, now time.Time) bool {
	if trusted {
		return allowSlowIngressLog(&d.lastTrustedValidationSlowLog, now)
	}
	return allowSlowIngressLog(&d.lastUntrustedValidationSlowLog, now)
}

type validationIngressTiming struct {
	receivedAt       time.Time
	handlerStartedAt time.Time
	enqueuedAt       time.Time
	dequeuedAt       time.Time
	verifiedAt       time.Time
}

type ingressStageDurations struct {
	routerWait    time.Duration
	admissionWait time.Duration
	queueWait     time.Duration
	verify        time.Duration
	resultWait    time.Duration
	engine        time.Duration
	total         time.Duration
}

func ingressDuration(start, end time.Time) time.Duration {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return 0
	}
	return end.Sub(start)
}

func ingressStage(d ingressStageDurations) string {
	stage, elapsed := "", time.Duration(0)
	for _, candidate := range []struct {
		name    string
		elapsed time.Duration
	}{
		{name: "router", elapsed: d.routerWait},
		{name: "admission", elapsed: d.admissionWait},
		{name: "validation-queue", elapsed: d.queueWait},
		{name: "verification", elapsed: d.verify},
		{name: "result-queue", elapsed: d.resultWait},
		{name: "engine", elapsed: d.engine},
	} {
		if candidate.elapsed > elapsed {
			stage, elapsed = candidate.name, candidate.elapsed
		}
	}
	if elapsed <= consensusIngressSlowThreshold {
		if d.total > consensusIngressSlowThreshold {
			return "aggregate"
		}
		return ""
	}
	return stage
}

func (r *Router) logSlowProposalIngress(
	msg *peermanagement.InboundMessage,
	proposal *consensus.Proposal,
	handlerStarted, completedAt time.Time,
	admissionElapsed time.Duration,
	engineElapsed time.Duration,
	outcome string,
) {
	if r == nil || r.logger == nil || msg == nil {
		return
	}
	durations := ingressStageDurations{
		routerWait:    ingressDuration(msg.ReceivedAt, handlerStarted),
		admissionWait: admissionElapsed,
		engine:        engineElapsed,
		total:         ingressDuration(msg.ReceivedAt, completedAt),
	}
	if durations.total == 0 {
		durations.total = durations.routerWait + durations.admissionWait + durations.engine
	}
	stage := ingressStage(durations)
	if stage == "" || !r.ingressDiagnostics.allowProposal(completedAt) {
		return
	}
	fields := []any{
		"t", "consensus",
		"event", "proposal-ingress-slow",
		"stage", stage,
		"outcome", outcome,
		"peer", msg.PeerID,
		"router_wait_us", durationMicros(durations.routerWait),
		"admission_us", durationMicros(durations.admissionWait),
		"engine_us", durationMicros(durations.engine),
		"total_us", durationMicros(durations.total),
	}
	if proposal != nil {
		fields = append(fields,
			"seq", proposal.Round.Seq,
			"prev_hash_short", fmt.Sprintf("%x", proposal.PreviousLedger[:8]),
		)
	}
	r.logger.Info("slow consensus proposal ingress", fields...)
}

func (r *Router) logSlowValidationIngress(
	result validationWorkResult,
	handlerStarted, completedAt time.Time,
	engineElapsed time.Duration,
	outcome string,
) {
	if r == nil || r.logger == nil {
		return
	}
	timing := result.ingress
	durations := ingressStageDurations{
		routerWait:    ingressDuration(timing.receivedAt, timing.handlerStartedAt),
		admissionWait: ingressDuration(timing.handlerStartedAt, timing.enqueuedAt),
		queueWait:     ingressDuration(timing.enqueuedAt, timing.dequeuedAt),
		verify:        ingressDuration(timing.dequeuedAt, timing.verifiedAt),
		resultWait:    ingressDuration(timing.verifiedAt, handlerStarted),
		engine:        engineElapsed,
		total:         ingressDuration(timing.receivedAt, completedAt),
	}
	if durations.total == 0 {
		durations.total = durations.routerWait + durations.admissionWait + durations.queueWait + durations.verify + durations.resultWait + durations.engine
	}
	stage := ingressStage(durations)
	if stage == "" || !r.ingressDiagnostics.allowValidation(result.trusted, completedAt) {
		return
	}
	fields := []any{
		"t", "consensus",
		"event", "validation-ingress-slow",
		"stage", stage,
		"outcome", outcome,
		"peer", result.origin.PeerID,
		"trusted", result.trusted,
		"trust_checked", result.trustChecked,
		"router_wait_us", durationMicros(durations.routerWait),
		"admission_us", durationMicros(durations.admissionWait),
		"queue_wait_us", durationMicros(durations.queueWait),
		"verify_us", durationMicros(durations.verify),
		"result_wait_us", durationMicros(durations.resultWait),
		"engine_us", durationMicros(durations.engine),
		"total_us", durationMicros(durations.total),
	}
	if result.validation != nil {
		fields = append(fields,
			"seq", result.validation.LedgerSeq,
			"hash_short", fmt.Sprintf("%x", result.validation.LedgerID[:8]),
		)
	}
	r.logger.Info("slow consensus validation ingress", fields...)
}
