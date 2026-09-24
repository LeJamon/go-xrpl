package handlers

import (
	"context"
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
	RevalidateReplayFault(context.Context, string) error
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
		"blocked":                 blocked,
		"follower_mode":           status.FollowerMode,
		"transition_verification": verificationState,
		"recovery":                recovery,
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
