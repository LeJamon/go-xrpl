package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TransactionOwnerFunds(
	txJSON map[string]any,
	view types.LedgerStateView,
	reserveBase, reserveInc uint64,
) (string, bool) {
	if txJSON["TransactionType"] != "OfferCreate" {
		return "", false
	}
	account, _ := txJSON["Account"].(string)
	if account == "" {
		return "", false
	}
	amount, ok := parseTransactionAmount(txJSON["TakerGets"])
	if !ok {
		return "", false
	}

	_, idBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil || len(idBytes) != 20 {
		return "", false
	}
	var accountID [20]byte
	copy(accountID[:], idBytes)

	if amount.IsMPT() {
		id, err := mptutil.DecodeID(amount.MPTIssuanceID())
		if err != nil || accountID == mptutil.Issuer(id) {
			return "", false
		}
		funds, _ := mptutil.Funds(view, id, accountID, false)
		return state.NewMPTAmountWithIssuanceID(funds, "", amount.MPTIssuanceID()).Value(), true
	}
	if !amount.IsNative() && amount.Issuer == account {
		return "", false
	}

	funds := tx.AccountFunds(view, accountID, amount, false, reserveBase, reserveInc)
	return funds.Value(), true
}

func reserveSettingsFromLedger(view types.LedgerStateView, fallbackBase, fallbackInc uint64) (uint64, uint64) {
	data, err := view.Read(keylet.Fees())
	if err != nil || data == nil {
		return fallbackBase, fallbackInc
	}
	settings, err := state.ParseFeeSettings(data)
	if err != nil {
		return fallbackBase, fallbackInc
	}
	return settings.GetReserveBase(), settings.GetReserveIncrement()
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
