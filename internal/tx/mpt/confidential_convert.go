package mpt

import (
	"github.com/LeJamon/go-xrpl/crypto/mptcrypto"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/protocol"
)

func (c *ConfidentialMPTConvert) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	id, _ := parseConfidentialID(c.MPTokenIssuanceID)
	issuance, _, accountID, result := readConfidentialIssuance(view, id, c.Account)
	if result != ter.TesSUCCESS {
		return result
	}
	if issuance.Flags&entry.LsfMPTCanHoldConfidentialBalance == 0 || len(issuance.IssuerEncryptionKey) == 0 {
		return ter.TecNO_PERMISSION
	}
	if issuance.Issuer == accountID {
		return ter.TefINTERNAL
	}
	if (c.AuditorEncryptedAmount != nil) != (len(issuance.AuditorEncryptionKey) != 0) {
		return ter.TecNO_PERMISSION
	}
	token, _, result := readConfidentialHolding(view, id, accountID)
	if result != ter.TesSUCCESS {
		return result
	}
	if mptutil.IsFrozen(view, id, accountID) {
		return ter.TecLOCKED
	}
	if result := mptutil.RequireAuthWithTypeAt(view, id, accountID, mptutil.LegacyAuth, config.ParentCloseTime); result != ter.TesSUCCESS {
		return result
	}
	funds, result := mptutil.Funds(view, id, accountID, true)
	if result != ter.TesSUCCESS {
		return result
	}
	if funds < int64(c.MPTAmount) {
		return ter.TecINSUFFICIENT_FUNDS
	}
	hasHolderKeyOnLedger := len(token.HolderEncryptionKey) != 0
	hasHolderKeyInTx := c.HolderEncryptionKey != nil
	if !hasHolderKeyOnLedger && !hasHolderKeyInTx {
		return ter.TecNO_PERMISSION
	}
	if hasHolderKeyOnLedger && hasHolderKeyInTx {
		return ter.TecDUPLICATE
	}

	holderKey := token.HolderEncryptionKey
	proofValid := true
	if hasHolderKeyInTx {
		holderKey, _ = decodeConfidentialField(*c.HolderEncryptionKey, mptcrypto.PublicKeySize)
		proof, _ := decodeConfidentialField(*c.ZKProof, mptcrypto.ConvertProofSize)
		contextHash, ok := mptcrypto.ConvertContext(accountID, id, c.GetCommon().SeqProxy())
		proofValid = ok && mptcrypto.VerifyConvert(proof, holderKey, contextHash)
	}

	holderCiphertext, _ := decodeConfidentialField(c.HolderEncryptedAmount, mptcrypto.CiphertextSize)
	issuerCiphertext, _ := decodeConfidentialField(c.IssuerEncryptedAmount, mptcrypto.CiphertextSize)
	blindingFactor, blindingFactorValid := confidentialBlindingFactor(c.BlindingFactor)
	revealedValid := mptcrypto.VerifyRevealed(
		c.MPTAmount,
		blindingFactor,
		confidentialParticipant(holderKey, holderCiphertext),
		confidentialParticipant(issuance.IssuerEncryptionKey, issuerCiphertext),
		confidentialAuditor(issuance, c.AuditorEncryptedAmount),
	)
	if !proofValid || !blindingFactorValid || !revealedValid {
		return ter.TecBAD_PROOF
	}
	return ter.TesSUCCESS
}

func (c *ConfidentialMPTConvert) Apply(ctx *tx.ApplyContext) ter.Result {
	id, _ := parseConfidentialID(c.MPTokenIssuanceID)
	issuance, issuanceKey, token, tokenKey, accountID, result := readConfidentialState(ctx.View, id, c.Account)
	if result != ter.TesSUCCESS {
		return ter.TecINTERNAL
	}
	if token.MPTAmount < c.MPTAmount || issuance.ConfidentialOutstandingAmount > protocol.MaxMPTokenAmount-c.MPTAmount {
		return ter.TecINTERNAL
	}

	holderCiphertext, _ := decodeConfidentialField(c.HolderEncryptedAmount, mptcrypto.CiphertextSize)
	issuerCiphertext, _ := decodeConfidentialField(c.IssuerEncryptedAmount, mptcrypto.CiphertextSize)
	if c.HolderEncryptionKey != nil {
		token.HolderEncryptionKey, _ = decodeConfidentialField(*c.HolderEncryptionKey, mptcrypto.PublicKeySize)
	}

	hasInbox := len(token.ConfidentialBalanceInbox) != 0
	hasSpending := len(token.ConfidentialBalanceSpending) != 0
	hasIssuerBalance := len(token.IssuerEncryptedBalance) != 0
	hasAuditorBalance := len(token.AuditorEncryptedBalance) != 0
	if hasInbox && hasSpending && hasIssuerBalance {
		var ok bool
		if token.ConfidentialBalanceInbox, ok = mptcrypto.AddCiphertexts(holderCiphertext, token.ConfidentialBalanceInbox); !ok {
			return ter.TecINTERNAL
		}
		if token.IssuerEncryptedBalance, ok = mptcrypto.AddCiphertexts(issuerCiphertext, token.IssuerEncryptedBalance); !ok {
			return ter.TecINTERNAL
		}
		if c.AuditorEncryptedAmount != nil {
			if !hasAuditorBalance {
				return ter.TecINTERNAL
			}
			auditorCiphertext, _ := decodeConfidentialField(*c.AuditorEncryptedAmount, mptcrypto.CiphertextSize)
			if token.AuditorEncryptedBalance, ok = mptcrypto.AddCiphertexts(auditorCiphertext, token.AuditorEncryptedBalance); !ok {
				return ter.TecINTERNAL
			}
		}
	} else if !hasInbox && !hasSpending && !hasIssuerBalance && !hasAuditorBalance {
		zero, ok := mptcrypto.CanonicalZero(token.HolderEncryptionKey, accountID, id)
		if !ok {
			return ter.TecINTERNAL
		}
		token.ConfidentialBalanceInbox = holderCiphertext
		token.ConfidentialBalanceSpending = zero
		token.IssuerEncryptedBalance = issuerCiphertext
		if c.AuditorEncryptedAmount != nil {
			token.AuditorEncryptedBalance, _ = decodeConfidentialField(*c.AuditorEncryptedAmount, mptcrypto.CiphertextSize)
		}
	} else {
		return ter.TecINTERNAL
	}

	token.MPTAmount -= c.MPTAmount
	issuance.ConfidentialOutstandingAmount += c.MPTAmount
	return serializeConfidentialState(ctx, issuanceKey, issuance, tokenKey, token)
}
