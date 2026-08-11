package mpt

import (
	"github.com/LeJamon/go-xrpl/crypto/mptcrypto"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func (c *ConfidentialMPTMergeInbox) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	id, _ := parseConfidentialID(c.MPTokenIssuanceID)
	issuance, _, accountID, result := readConfidentialIssuance(view, id, c.Account)
	if result != ter.TesSUCCESS {
		return result
	}
	if issuance.Flags&entry.LsfMPTCanHoldConfidentialBalance == 0 {
		return ter.TecNO_PERMISSION
	}
	if issuance.Issuer == accountID {
		return ter.TefINTERNAL
	}
	token, _, result := readConfidentialHolding(view, id, accountID)
	if result != ter.TesSUCCESS {
		return result
	}
	if len(token.ConfidentialBalanceInbox) == 0 ||
		len(token.ConfidentialBalanceSpending) == 0 ||
		len(token.HolderEncryptionKey) == 0 {
		return ter.TecNO_PERMISSION
	}
	if mptutil.IsFrozen(view, id, accountID) {
		return ter.TecLOCKED
	}
	return mptutil.RequireAuthWithTypeAt(view, id, accountID, mptutil.LegacyAuth, config.ParentCloseTime)
}

func (c *ConfidentialMPTMergeInbox) Apply(ctx *tx.ApplyContext) ter.Result {
	id, _ := parseConfidentialID(c.MPTokenIssuanceID)
	_, _, token, tokenKey, accountID, result := readConfidentialState(ctx.View, id, c.Account)
	if result != ter.TesSUCCESS {
		return ter.TecINTERNAL
	}
	if len(token.ConfidentialBalanceSpending) == 0 ||
		len(token.ConfidentialBalanceInbox) == 0 ||
		len(token.HolderEncryptionKey) == 0 {
		return ter.TecINTERNAL
	}

	spending, ok := mptcrypto.AddCiphertexts(token.ConfidentialBalanceSpending, token.ConfidentialBalanceInbox)
	if !ok {
		return ter.TecINTERNAL
	}
	zero, ok := mptcrypto.CanonicalZero(token.HolderEncryptionKey, accountID, id)
	if !ok {
		return ter.TecINTERNAL
	}
	token.ConfidentialBalanceSpending = spending
	token.ConfidentialBalanceInbox = zero
	token.ConfidentialBalanceVersion++

	data, err := state.SerializeMPToken(token)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Update(tokenKey, data); err != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}
