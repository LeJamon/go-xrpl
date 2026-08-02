package node

import (
	"encoding/hex"
	"strings"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/rpc"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/protocol"
)

type ledgerInfoService interface {
	GetValidatedLedger() *ledger.Ledger
	GetServerInfo() service.ServerInfo
}

// ledgerInfoAdapter adapts the ledger service to the LedgerInfoProvider interface
type ledgerInfoAdapter struct {
	ledgerService ledgerInfoService
}

func (a *ledgerInfoAdapter) GetCurrentLedgerInfo() *types.LedgerSubscribeInfo {
	if a.ledgerService == nil {
		return nil
	}

	validatedLedger := a.ledgerService.GetValidatedLedger()
	if validatedLedger == nil {
		return nil
	}

	baseFee, reserveBase, reserveInc := service.FeesFromLedger(validatedLedger)

	ledgerTime := protocol.ToRippleTime(validatedLedger.CloseTime())

	hash := validatedLedger.Hash()
	serverInfo := a.ledgerService.GetServerInfo()
	validatedLedgers := ""
	if serverPublishesValidatedRange(serverInfo.ServerState) && !serverInfo.NeedsNetworkLedger {
		validatedLedgers = serverInfo.CompleteLedgers
	}
	xrpFeesEnabled := validatedLedger.Rules() != nil && validatedLedger.Rules().XRPFeesEnabled()

	return &types.LedgerSubscribeInfo{
		LedgerIndex:      validatedLedger.Sequence(),
		LedgerHash:       upperHex(hash[:]),
		LedgerTime:       ledgerTime,
		FeeBase:          jsonClippedXRPAmount(int64(baseFee)),
		FeeRef:           deprecatedFeeReferenceUnits,
		ReserveBase:      jsonClippedXRPAmount(int64(reserveBase)),
		ReserveInc:       jsonClippedXRPAmount(int64(reserveInc)),
		ValidatedLedgers: validatedLedgers,
		NetworkID:        serverInfo.NetworkID,
		XRPFeesEnabled:   xrpFeesEnabled,
	}
}

const deprecatedFeeReferenceUnits uint64 = 10

func serverPublishesValidatedRange(state string) bool {
	switch state {
	case "syncing", "tracking", "full", "proposing", "validating":
		return true
	default:
		return false
	}
}

func buildLedgerCloseEvent(event *service.LedgerAcceptedEvent, serverInfo service.ServerInfo) *rpc.LedgerCloseEvent {
	if event == nil || event.LedgerInfo == nil || event.Ledger == nil {
		return nil
	}
	baseFee, reserveBase, reserveInc := service.FeesFromLedger(event.Ledger)
	var feeRef *uint64
	if rules := event.Ledger.Rules(); rules == nil || !rules.XRPFeesEnabled() {
		value := deprecatedFeeReferenceUnits
		feeRef = &value
	}
	validatedLedgers := ""
	if serverPublishesValidatedRange(serverInfo.ServerState) {
		validatedLedgers = serverInfo.CompleteLedgers
	}
	return &rpc.LedgerCloseEvent{
		Type:             "ledgerClosed",
		LedgerIndex:      event.LedgerInfo.Sequence,
		LedgerHash:       upperHex(event.LedgerInfo.Hash[:]),
		LedgerTime:       protocol.ToRippleTime(event.LedgerInfo.CloseTime),
		FeeBase:          jsonClippedXRPAmount(int64(baseFee)),
		FeeRef:           feeRef,
		NetworkID:        serverInfo.NetworkID,
		ReserveBase:      jsonClippedXRPAmount(int64(reserveBase)),
		ReserveInc:       jsonClippedXRPAmount(int64(reserveInc)),
		TxnCount:         len(event.TransactionResults),
		ValidatedLedgers: validatedLedgers,
	}
}

// upperHex renders bytes as uppercase hex
func upperHex(b []byte) string {
	return strings.ToUpper(hex.EncodeToString(b))
}

func jsonClippedXRPAmount(value int64) int32 {
	return drops.XRPAmount(value).JSONClipped()
}

type acceptedLedgerView struct {
	info service.LedgerInfo
}

func newAcceptedLedgerView(info service.LedgerInfo) *acceptedLedgerView {
	return &acceptedLedgerView{info: info}
}

func (a *acceptedLedgerView) Sequence() uint32 {
	return a.info.Sequence
}

func (a *acceptedLedgerView) Hash() [32]byte {
	return a.info.Hash
}

func (a *acceptedLedgerView) CloseTime() int64 {
	return protocol.RippleSeconds(a.info.CloseTime)
}

func (a *acceptedLedgerView) IsValidated() bool {
	return a.info.Validated
}
