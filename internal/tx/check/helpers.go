package check

import (
	"encoding/hex"
	"strconv"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// newCheckData builds the state.CheckData for a Check ledger entry from a
// CheckCreate transaction. The directory page fields (OwnerNode/DestinationNode)
// are filled in by the caller after dirInsert. The single serializer for a Check
// entry — creation and later modification alike — is state.SerializeCheckFromData.
func newCheckData(checkTx *CheckCreate, ownerID, destID [20]byte, sequence uint32, sendMax tx.Amount) *state.CheckData {
	cd := &state.CheckData{
		Account:       ownerID,
		DestinationID: destID,
		Sequence:      sequence,
	}

	if sendMax.IsNative() {
		cd.IsNativeSendMax = true
		cd.SendMax = uint64(sendMax.Drops())
		cd.SendMaxAmount = state.NewXRPAmountFromInt(int64(cd.SendMax))
	} else {
		cd.SendMaxAmount = sendMax
	}

	if checkTx.SourceTag != nil {
		cd.SourceTag = *checkTx.SourceTag
		cd.HasSourceTag = true
	}
	if checkTx.Expiration != nil {
		cd.Expiration = *checkTx.Expiration
	}
	if checkTx.DestinationTag != nil {
		cd.DestinationTag = *checkTx.DestinationTag
		cd.HasDestTag = true
	}
	if checkTx.InvoiceID != "" {
		if b, err := hex.DecodeString(checkTx.InvoiceID); err == nil && len(b) == 32 {
			copy(cd.InvoiceID[:], b)
			cd.HasInvoiceID = true
		}
	}

	return cd
}

// parseFee parses the fee string (in drops) to uint64.
// Returns 0 if the fee is empty or invalid.
func parseFee(fee string) uint64 {
	if fee == "" {
		return 0
	}
	v, err := strconv.ParseUint(fee, 10, 64)
	if err != nil {
		return 0
	}
	return v
}
