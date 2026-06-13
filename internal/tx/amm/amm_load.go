package amm

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

// readAMM reads and parses the AMM ledger entry for the (asset, asset2) pair.
// It returns terNO_AMM when the entry is absent and tefINTERNAL on a parse
// failure, matching the inline load that opens every AMM transactor.
func readAMM(view tx.LedgerView, asset, asset2 tx.Asset) (*AMMData, keylet.Keylet, tx.Result) {
	ammKey := computeAMMKeylet(asset, asset2)
	ammRawData, err := view.Read(ammKey)
	if err != nil || ammRawData == nil {
		return nil, ammKey, TerNO_AMM
	}
	amm, err := parseAMMData(ammRawData)
	if err != nil {
		return nil, ammKey, tx.TefINTERNAL
	}
	return amm, ammKey, tx.TesSUCCESS
}

// readAccount reads and parses an AccountRoot by its decoded ID. It returns
// tefINTERNAL when the entry is missing or unparseable; callers that need a
// distinct "account absent" code should check existence separately.
func readAccount(view tx.LedgerView, accountID [20]byte) (*state.AccountRoot, tx.Result) {
	data, err := view.Read(keylet.Account(accountID))
	if err != nil || data == nil {
		return nil, tx.TefINTERNAL
	}
	account, err := state.ParseAccountRoot(data)
	if err != nil {
		return nil, tx.TefINTERNAL
	}
	return account, tx.TesSUCCESS
}

// loadedAMM bundles the AMM ledger entry, its pseudo-account, and the current
// pool balances reordered to the transaction's asset ordering.
type loadedAMM struct {
	Key            keylet.Keylet
	Data           *AMMData
	AccountID      [20]byte
	AccountKey     keylet.Keylet
	Account        *state.AccountRoot
	AssetBalance1  tx.Amount
	AssetBalance2  tx.Amount
	LPTokenBalance tx.Amount
}

// loadAMM reads the AMM for the (asset, asset2) pair, its pseudo-account, and
// the pool balances (reordered so AssetBalance1 carries the issue of txAsset).
// It mirrors the load+reorder snippet that opens AMMDeposit, AMMWithdraw and
// AMMClawback's apply logic and the re-`peek` in rippled's applyGuts. Because
// preclaim already confirmed the AMM exists, an absent entry here is an
// internal inconsistency and returns tecINTERNAL (matching applyGuts); a parse
// failure returns tefINTERNAL.
func loadAMM(view tx.LedgerView, asset, asset2, txAsset tx.Asset) (*loadedAMM, tx.Result) {
	amm, ammKey, result := readAMM(view, asset, asset2)
	if result != tx.TesSUCCESS {
		if result == TerNO_AMM {
			return nil, tx.TecINTERNAL
		}
		return nil, result
	}

	ammAccountID := amm.Account
	ammAccountKey := keylet.Account(ammAccountID)
	ammAccountData, err := view.Read(ammAccountKey)
	if err != nil {
		return nil, tx.TefINTERNAL
	}
	ammAccount, err := state.ParseAccountRoot(ammAccountData)
	if err != nil {
		return nil, tx.TefINTERNAL
	}

	assetBalance1, assetBalance2, lptBalance := AMMHolds(view, amm, false)
	if !matchesAssetByIssue(amm.Asset, txAsset) {
		assetBalance1, assetBalance2 = assetBalance2, assetBalance1
	}

	return &loadedAMM{
		Key:            ammKey,
		Data:           amm,
		AccountID:      ammAccountID,
		AccountKey:     ammAccountKey,
		Account:        ammAccount,
		AssetBalance1:  assetBalance1,
		AssetBalance2:  assetBalance2,
		LPTokenBalance: lptBalance,
	}, tx.TesSUCCESS
}
