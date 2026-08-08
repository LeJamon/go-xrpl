package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TransactionOwnerFunds(
	txJSON map[string]any,
	view types.LedgerStateView,
	reserveBase, reserveInc uint64,
) (string, bool, error) {
	accountID, amount, ok := transactionOwnerFundsInput(txJSON)
	if !ok {
		return "", false, nil
	}

	if amount.IsMPT() {
		id, _ := mptutil.DecodeID(amount.MPTIssuanceID())
		funds, result := mptutil.Funds(view, id, accountID, false)
		switch result {
		case ter.TesSUCCESS:
		case ter.TecOBJECT_NOT_FOUND, ter.TecNO_AUTH:
			funds = 0
		default:
			return "", true, fmt.Errorf("owner funds: MPT funds failed: %s", result)
		}
		return state.NewMPTAmountWithIssuanceID(funds, "", amount.MPTIssuanceID()).Value(), true, nil
	}

	funds, err := tx.AccountFundsNoFreezeStrict(view, accountID, amount, reserveBase, reserveInc)
	if err != nil {
		return "", true, err
	}
	return funds.Value(), true, nil
}

// TransactionOwnerFundsRequirements reports whether txJSON needs owner_funds
// and whether that calculation depends on XRP reserve settings.
func TransactionOwnerFundsRequirements(txJSON map[string]any) (applicable, needsReserves bool) {
	_, amount, ok := transactionOwnerFundsInput(txJSON)
	return ok, ok && amount.IsNative()
}

func transactionOwnerFundsInput(txJSON map[string]any) ([20]byte, state.Amount, bool) {
	if txJSON["TransactionType"] != "OfferCreate" {
		return [20]byte{}, state.Amount{}, false
	}
	account, _ := txJSON["Account"].(string)
	if account == "" {
		return [20]byte{}, state.Amount{}, false
	}
	amount, ok := parseTransactionAmount(txJSON["TakerGets"])
	if !ok {
		return [20]byte{}, state.Amount{}, false
	}

	_, idBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil || len(idBytes) != 20 {
		return [20]byte{}, state.Amount{}, false
	}
	var accountID [20]byte
	copy(accountID[:], idBytes)

	if amount.IsMPT() {
		id, err := mptutil.DecodeID(amount.MPTIssuanceID())
		if err != nil || accountID == mptutil.Issuer(id) {
			return [20]byte{}, state.Amount{}, false
		}
	}
	if !amount.IsNative() && amount.Issuer == account {
		return [20]byte{}, state.Amount{}, false
	}
	return accountID, amount, true
}

func reserveSettingsFromLedger(view types.LedgerStateView, fallbackBase, fallbackInc uint64) (uint64, uint64, error) {
	if view == nil {
		return 0, 0, fmt.Errorf("reserve settings: nil ledger view")
	}
	data, err := view.Read(keylet.Fees())
	if err != nil {
		return 0, 0, fmt.Errorf("reserve settings: read FeeSettings: %w", err)
	}
	if data == nil {
		return fallbackBase, fallbackInc, nil
	}
	settings, err := state.ParseFeeSettings(data)
	if err != nil {
		return 0, 0, fmt.Errorf("reserve settings: parse FeeSettings: %w", err)
	}
	reserveBase, reserveInc := fallbackBase, fallbackInc
	if settings.XRPFeesMode {
		if settings.HasReserveBaseDrops {
			reserveBase = settings.ReserveBaseDrops
		}
		if settings.HasReserveIncrementDrops {
			reserveInc = settings.ReserveIncrementDrops
		}
	} else {
		if settings.HasReserveBase {
			reserveBase = uint64(settings.ReserveBase)
		}
		if settings.HasReserveIncrement {
			reserveInc = uint64(settings.ReserveIncrement)
		}
	}
	return reserveBase, reserveInc, nil
}

func parseTransactionAmount(raw any) (state.Amount, bool) {
	if raw == nil {
		return state.Amount{}, false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return state.Amount{}, false
	}
	var amount state.Amount
	if err := json.Unmarshal(encoded, &amount); err != nil {
		return state.Amount{}, false
	}
	return amount, true
}
