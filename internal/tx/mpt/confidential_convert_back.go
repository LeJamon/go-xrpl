package mpt

import (
	"github.com/LeJamon/go-xrpl/crypto/mptcrypto"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/protocol"
)

func (c *ConfidentialMPTConvertBack) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	id, _ := parseConfidentialID(c.MPTokenIssuanceID)
	issuance, _, accountID, result := readConfidentialIssuance(view, id, c.Account)
	if result != ter.TesSUCCESS {
		return result
	}
	if issuance.Flags&entry.LsfMPTCanHoldConfidentialBalance == 0 || len(issuance.IssuerEncryptionKey) == 0 {
		return ter.TecNO_PERMISSION
	}
	requiresAuditor := len(issuance.AuditorEncryptionKey) != 0
	if requiresAuditor != (c.AuditorEncryptedAmount != nil) {
		return ter.TecNO_PERMISSION
	}
	if issuance.Issuer == accountID {
		return ter.TefINTERNAL
	}
	token, _, result := readConfidentialHolding(view, id, accountID)
	if result != ter.TesSUCCESS {
		return result
	}
	if len(token.HolderEncryptionKey) == 0 ||
		len(token.ConfidentialBalanceSpending) == 0 ||
		len(token.IssuerEncryptedBalance) == 0 {
		return ter.TecNO_PERMISSION
	}
	if requiresAuditor && len(token.AuditorEncryptedBalance) == 0 {
		return ter.TefINTERNAL
	}
	if issuance.ConfidentialOutstandingAmount < c.MPTAmount {
		return ter.TecINSUFFICIENT_FUNDS
	}
	if mptutil.IsFrozen(view, id, accountID) {
		return ter.TecLOCKED
	}
	if result := mptutil.RequireAuthWithTypeAt(view, id, accountID, mptutil.LegacyAuth, config.ParentCloseTime); result != ter.TesSUCCESS {
		return result
	}

	holderCiphertext, _ := decodeConfidentialField(c.HolderEncryptedAmount, mptcrypto.CiphertextSize)
	issuerCiphertext, _ := decodeConfidentialField(c.IssuerEncryptedAmount, mptcrypto.CiphertextSize)
	blindingFactor, blindingFactorValid := confidentialBlindingFactor(c.BlindingFactor)
	revealedValid := mptcrypto.VerifyRevealed(
		c.MPTAmount,
		blindingFactor,
		confidentialParticipant(token.HolderEncryptionKey, holderCiphertext),
		confidentialParticipant(issuance.IssuerEncryptionKey, issuerCiphertext),
		confidentialAuditor(issuance, c.AuditorEncryptedAmount),
	)
	proof, _ := decodeConfidentialField(c.ZKProof, mptcrypto.ConvertBackProofSize)
	commitment, _ := decodeConfidentialField(c.BalanceCommitment, mptcrypto.CommitmentSize)
	contextHash, contextOK := mptcrypto.ConvertBackContext(
		accountID,
		id,
		c.GetCommon().SeqProxy(),
		token.ConfidentialBalanceVersion,
	)
	proofValid := contextOK && mptcrypto.VerifyConvertBack(
		proof,
		token.HolderEncryptionKey,
		token.ConfidentialBalanceSpending,
		commitment,
		c.MPTAmount,
		contextHash,
	)
	if !blindingFactorValid || !revealedValid || !proofValid {
		return ter.TecBAD_PROOF
	}
	return ter.TesSUCCESS
}

func (c *ConfidentialMPTConvertBack) Apply(ctx *tx.ApplyContext) ter.Result {
	id, _ := parseConfidentialID(c.MPTokenIssuanceID)
	issuance, issuanceKey, token, tokenKey, _, result := readConfidentialState(ctx.View, id, c.Account)
	if result != ter.TesSUCCESS {
		return ter.TecINTERNAL
	}
	if token.MPTAmount > protocol.MaxMPTokenAmount-c.MPTAmount ||
		issuance.ConfidentialOutstandingAmount < c.MPTAmount {
		return ter.TecINTERNAL
	}

	holderCiphertext, _ := decodeConfidentialField(c.HolderEncryptedAmount, mptcrypto.CiphertextSize)
	issuerCiphertext, _ := decodeConfidentialField(c.IssuerEncryptedAmount, mptcrypto.CiphertextSize)
	var ok bool
	if token.ConfidentialBalanceSpending, ok = mptcrypto.SubtractCiphertexts(token.ConfidentialBalanceSpending, holderCiphertext); !ok {
		return ter.TecINTERNAL
	}
	if token.IssuerEncryptedBalance, ok = mptcrypto.SubtractCiphertexts(token.IssuerEncryptedBalance, issuerCiphertext); !ok {
		return ter.TecINTERNAL
	}
	if c.AuditorEncryptedAmount != nil {
		auditorCiphertext, _ := decodeConfidentialField(*c.AuditorEncryptedAmount, mptcrypto.CiphertextSize)
		if token.AuditorEncryptedBalance, ok = mptcrypto.SubtractCiphertexts(token.AuditorEncryptedBalance, auditorCiphertext); !ok {
			return ter.TecINTERNAL
		}
	}

	token.MPTAmount += c.MPTAmount
	issuance.ConfidentialOutstandingAmount -= c.MPTAmount
	token.ConfidentialBalanceVersion++
	return serializeConfidentialState(ctx, issuanceKey, issuance, tokenKey, token)
}
