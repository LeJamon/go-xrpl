package types

import (
	"time"

	"github.com/LeJamon/go-xrpl/protocol"
)

const maxValidatedLedgerAge = 120 * time.Second

func ValidatedLedgerStale(info LedgerServerInfo) bool {
	if !info.HaveValidated || info.ValidatedLedgerCloseTime == 0 {
		return true
	}
	nowRipple := time.Now().Unix() - protocol.RippleEpochUnix
	age := nowRipple - info.ValidatedLedgerCloseTime
	return age > int64(maxValidatedLedgerAge/time.Second)
}

func CurrentLedgerUnavailable(apiVersion int) *RpcError {
	if apiVersion == ApiVersion1 {
		return NewRpcError(RpcNO_CURRENT, "noCurrent", "noCurrent", "Current ledger is unavailable.")
	}
	return RpcErrorNotSynced("")
}
