package payment

import (
	"errors"

	tx "github.com/LeJamon/go-xrpl/internal/tx"
)

// errInsufficientFunds is the sentinel an XRP-movement primitive returns when
// the sender's balance is below the amount being sent. It is a defensive guard:
// the flow engine caps amounts at the sender's available funds, so it should
// never trip during normal operation. It mirrors the insufficient-balance
// branch in rippled's accountSendIOU (native path) / transferXRP, which there
// yields telFAILED_PROCESSING (open view) or tecFAILED_PROCESSING (closed view).
var errInsufficientFunds = errors.New("insufficient XRP balance")

// errXRPBalanceOutOfRange is the sentinel accountSend returns when crediting a
// destination would push its XRP balance above the serializable maximum
// (drops.MaxDrops). rippled's STAmount tolerates such transient over-range
// amounts during the reverse pass; our binary codec does not, so the
// destination XRP endpoint treats this specific failure as the expected codec
// artifact (the over-range credit is discarded by the limiting step and
// re-applied at a bounded amount in the forward pass) while still failing the
// strand on any genuine credit error.
var errXRPBalanceOutOfRange = errors.New("xrp endpoint credit exceeds serializable range")

// failedProcessingResult returns the FAILED_PROCESSING TER variant appropriate
// for this sandbox's view openness, mirroring rippled View.cpp where the guard
// returns view.open() ? telFAILED_PROCESSING : tecFAILED_PROCESSING.
func (s *PaymentSandbox) failedProcessingResult() tx.Result {
	if s.IsOpenLedger() {
		return tx.TelFAILED_PROCESSING
	}
	return tx.TecFAILED_PROCESSING
}
