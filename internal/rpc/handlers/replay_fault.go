package handlers

import (
	"encoding/hex"
	"strings"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// replayFaultStatusReader is intentionally narrower than types.LedgerService.
// Replay-fault control is an optional operator capability of the concrete
// ledger-service adapter, so RPC-only fixtures and alternate ledger providers
// do not need to implement it.
type replayFaultStatusReader interface {
	ReplayFaultStatus() replayfault.Status
	ReplayBlocked() bool
}

type replayFaultRecovery interface {
	StartReplayFaultRecovery(string) error
}

func replayFaultStatus(services *types.ServiceGraph) (map[string]any, bool) {
	if services == nil || services.Ledger() == nil {
		return nil, false
	}
	reader, ok := services.Ledger().(replayFaultStatusReader)
	if !ok {
		return nil, false
	}
	status := reader.ReplayFaultStatus()
	return replayFaultStatusJSON(status, status.Blocked || status.PersistenceError != nil || reader.ReplayBlocked()), true
}

// replayFaultStatusJSON projects the durable status into an operator-safe
// diagnostic shape. Fault Evidence contains serialized parent state and
// transactions; it must never be returned through server_info or RPC control
// responses. The transition_verification field makes an in-flight recovery
// attempt distinguishable from a healthy node with no pending fault.
func replayFaultStatusJSON(status replayfault.Status, blocked bool) map[string]any {
	recovery := map[string]any{
		"in_flight": status.Recovery.InFlight,
	}
	if status.Recovery.Strategy != "" {
		recovery["strategy"] = string(status.Recovery.Strategy)
	}
	if status.Recovery.Strategy == replayfault.RecoveryStrategyVerifiedPivot {
		recovery["revision"] = status.Recovery.Revision
		recovery["evidence_authenticated"] = status.Recovery.EvidenceAuthenticated
		recovery["state_verified"] = status.Recovery.StateVerified
		recovery["replay_lag"] = status.Recovery.ReplayLag
		recovery["reused_node_reads"] = status.Recovery.ReusedNodes
		recovery["fetched_nodes"] = status.Recovery.FetchedNodes
		recovery["state_missing_nodes_discovered"] = status.Recovery.MissingNodes
	}
	if status.Recovery.Outcome != "" {
		recovery["outcome"] = string(status.Recovery.Outcome)
	}
	if status.Recovery.FailureFingerprint != "" {
		recovery["failure_fingerprint"] = status.Recovery.FailureFingerprint
	}
	if status.Recovery.FailureAttempts != 0 {
		recovery["failure_attempts"] = status.Recovery.FailureAttempts
	}
	if status.Recovery.Phase != "" {
		recovery["phase"] = string(status.Recovery.Phase)
	}
	for _, point := range []struct {
		name     string
		sequence uint32
		hash     [32]byte
	}{
		{"pivot", status.Recovery.PivotSequence, status.Recovery.PivotHash},
		{"verified_successor", status.Recovery.SuccessorSequence, status.Recovery.SuccessorHash},
		{"frontier", status.Recovery.FrontierSequence, status.Recovery.FrontierHash},
	} {
		if point.sequence != 0 {
			recovery[point.name] = map[string]any{
				"sequence": point.sequence,
				"hash":     strings.ToUpper(hex.EncodeToString(point.hash[:])),
			}
		}
	}
	if status.Recovery.ID != "" {
		recovery["id"] = status.Recovery.ID
	}
	if status.Recovery.Generation != 0 {
		recovery["generation"] = status.Recovery.Generation
	}
	if status.Recovery.Attempts != 0 {
		recovery["attempts"] = status.Recovery.Attempts
	}
	if !status.Recovery.StartedAt.IsZero() {
		recovery["started_at"] = status.Recovery.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if status.Recovery.LastError != "" {
		recovery["last_error"] = status.Recovery.LastError
	}
	if status.Recovery.AcquisitionAttempts != 0 {
		recovery["acquisition_attempts"] = status.Recovery.AcquisitionAttempts
	}
	if status.Recovery.AcquisitionError != "" {
		recovery["acquisition_error"] = status.Recovery.AcquisitionError
	}
	if status.Recovery.BlockedReason != "" {
		recovery["blocked_reason"] = status.Recovery.BlockedReason
	}
	if status.Recovery.OperatorAction != "" {
		recovery["operator_action"] = status.Recovery.OperatorAction
	}
	verificationState := status.TransitionVerification
	if verificationState == "" {
		verificationState = "unknown"
		if blocked {
			verificationState = "pending"
			if status.Recovery.InFlight {
				verificationState = "running"
			}
		}
	}
	result := map[string]any{
		"blocked":                  blocked,
		"validator_duties_blocked": blocked,
		"follower_mode":            status.FollowerMode,
		"transition_verification":  verificationState,
		"recovery":                 recovery,
	}
	if status.LastTransitionHash != ([32]byte{}) {
		result["last_transition_hash"] = strings.ToUpper(hex.EncodeToString(status.LastTransitionHash[:]))
	}

	if status.PersistenceError != nil {
		result["persistence_error"] = status.PersistenceError.Error()
	}
	if fault := status.Fault; fault != nil {
		result["fault"] = map[string]any{
			"id":          fault.ID,
			"class":       string(fault.Class),
			"parent_hash": strings.ToUpper(hex.EncodeToString(fault.ParentHash[:])),
			"target_hash": strings.ToUpper(hex.EncodeToString(fault.TargetHash[:])),
			"sequence":    fault.Sequence,
			"message":     fault.Message,
			"revision":    fault.Revision,
			"created_at":  fault.CreatedAt.UTC().Format(time.RFC3339Nano),
			"attempts":    fault.Attempts,
		}
	}
	return result
}
