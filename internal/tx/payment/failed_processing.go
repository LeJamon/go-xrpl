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

// failedProcessingResult returns the FAILED_PROCESSING TER variant appropriate
// for this sandbox's view openness, mirroring rippled View.cpp where the guard
// returns view.open() ? telFAILED_PROCESSING : tecFAILED_PROCESSING.
func (s *PaymentSandbox) failedProcessingResult() tx.Result {
	if s.IsOpenLedger() {
		return tx.TelFAILED_PROCESSING
	}
	return tx.TecFAILED_PROCESSING
}
