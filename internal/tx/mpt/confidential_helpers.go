package mpt

import (
	"github.com/LeJamon/go-xrpl/crypto/mptcrypto"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

func readConfidentialIssuance(view tx.LedgerView, id [24]byte, account string) (*state.MPTokenIssuanceData, keylet.Keylet, [20]byte, ter.Result) {
	issuance, issuanceKey, result := mptutil.ReadIssuance(view, id)
	if result != ter.TesSUCCESS {
		return nil, issuanceKey, [20]byte{}, result
	}
	accountID, err := state.DecodeAccountID(account)
	if err != nil {
		return nil, issuanceKey, [20]byte{}, ter.TemBAD_SRC_ACCOUNT
	}
	return issuance, issuanceKey, accountID, ter.TesSUCCESS
}

func readConfidentialHolding(view tx.LedgerView, id [24]byte, accountID [20]byte) (*state.MPTokenData, keylet.Keylet, ter.Result) {
	token, tokenKey, result := mptutil.ReadHolding(view, id, accountID)
	if result == ter.TecNO_AUTH {
		result = ter.TecOBJECT_NOT_FOUND
	}
	return token, tokenKey, result
}

func readConfidentialState(view tx.LedgerView, id [24]byte, account string) (*state.MPTokenIssuanceData, keylet.Keylet, *state.MPTokenData, keylet.Keylet, [20]byte, ter.Result) {
	issuance, issuanceKey, accountID, result := readConfidentialIssuance(view, id, account)
	if result != ter.TesSUCCESS {
		return nil, issuanceKey, nil, keylet.Keylet{}, accountID, result
	}
	token, tokenKey, result := readConfidentialHolding(view, id, accountID)
	return issuance, issuanceKey, token, tokenKey, accountID, result
}

func confidentialParticipant(publicKey, ciphertext []byte) mptcrypto.Participant {
	return mptcrypto.Participant{PublicKey: publicKey, Ciphertext: ciphertext}
}

func confidentialBlindingFactor(value string) ([mptcrypto.BlindingFactorSize]byte, bool) {
	decoded, valid := decodeConfidentialField(value, mptcrypto.BlindingFactorSize)
	var result [mptcrypto.BlindingFactorSize]byte
	copy(result[:], decoded)
	return result, valid
}

func confidentialAuditor(issuance *state.MPTokenIssuanceData, encryptedAmount *string) *mptcrypto.Participant {
	if encryptedAmount == nil {
		return nil
	}
	ciphertext, _ := decodeConfidentialField(*encryptedAmount, mptcrypto.CiphertextSize)
	auditor := confidentialParticipant(issuance.AuditorEncryptionKey, ciphertext)
	return &auditor
}

func serializeConfidentialState(ctx *tx.ApplyContext, issuanceKey keylet.Keylet, issuance *state.MPTokenIssuanceData, tokenKey keylet.Keylet, token *state.MPTokenData) ter.Result {
	issuanceData, err := state.SerializeMPTokenIssuance(issuance)
	if err != nil {
		return ter.TefINTERNAL
	}
	tokenData, err := state.SerializeMPToken(token)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Update(issuanceKey, issuanceData); err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Update(tokenKey, tokenData); err != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}
