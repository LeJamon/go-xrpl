package mpt

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/crypto/mptcrypto"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/protocol"
)

type ConfidentialMPTClawback struct {
	tx.BaseTx
	MPTokenIssuanceID string `json:"MPTokenIssuanceID" xrpl:"MPTokenIssuanceID"`
	Holder            string `json:"Holder" xrpl:"Holder"`
	MPTAmount         uint64 `json:"MPTAmount" xrpl:"MPTAmount"`
	ZKProof           string `json:"ZKProof" xrpl:"ZKProof"`
}

func (c *ConfidentialMPTClawback) UnmarshalJSON(data []byte) error {
	type alias ConfidentialMPTClawback
	var decoded alias
	amount, err := unmarshalConfidential(data, &decoded)
	if err != nil {
		return err
	}
	decoded.MPTAmount = amount
	*c = ConfidentialMPTClawback(decoded)
	return nil
}

func (c *ConfidentialMPTClawback) TxType() tx.Type { return tx.TypeConfidentialMPTClawback }

func (c *ConfidentialMPTClawback) GetFlagsMask(*amendment.Rules) uint32 { return tx.TfUniversalMask }

func (c *ConfidentialMPTClawback) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureConfidentialTransfer}
}

func (c *ConfidentialMPTClawback) Flatten() (map[string]any, error) {
	result, err := tx.ReflectFlatten(c)
	if err == nil {
		result["MPTAmount"] = fmt.Sprintf("%d", c.MPTAmount)
	}
	return result, err
}

func (c *ConfidentialMPTClawback) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
	}
	id, ok := parseConfidentialID(c.MPTokenIssuanceID)
	if !ok {
		return ter.Errorf(ter.TemINVALID, "invalid MPTokenIssuanceID")
	}
	accountID, accountErr := state.DecodeAccountID(c.Account)
	holderID, holderErr := state.DecodeAccountID(c.Holder)
	if accountErr != nil || holderErr != nil {
		return ter.Errorf(ter.TemMALFORMED, "invalid clawback account")
	}
	if accountID != [20]byte(id[4:]) || accountID == holderID {
		return ter.Errorf(ter.TemMALFORMED, "invalid confidential clawback participants")
	}
	if c.MPTAmount == 0 || c.MPTAmount > protocol.MaxMPTokenAmount {
		return ter.Errorf(ter.TemBAD_AMOUNT, "invalid MPTAmount")
	}
	if _, valid := decodeFixed(c.ZKProof, mptcrypto.ClawbackProofSize); !valid {
		return ter.Errorf(ter.TemMALFORMED, "invalid ZKProof")
	}
	return nil
}

func (c *ConfidentialMPTClawback) CalculateBaseFee(_ tx.LedgerView, config tx.EngineConfig) uint64 {
	return confidentialBaseFee(c, config)
}

func (c *ConfidentialMPTClawback) Preclaim(view tx.LedgerView, _ tx.EngineConfig) ter.Result {
	id, _ := parseConfidentialID(c.MPTokenIssuanceID)
	accountID, err := state.DecodeAccountID(c.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	account, err := tx.ReadAccountRoot(view, accountID)
	if err != nil {
		return ter.TefINTERNAL
	}
	if account == nil {
		return ter.TerNO_ACCOUNT
	}
	holderID, err := state.DecodeAccountID(c.Holder)
	if err != nil {
		return ter.TemMALFORMED
	}
	holderAccount, err := tx.ReadAccountRoot(view, holderID)
	if err != nil {
		return ter.TefINTERNAL
	}
	if holderAccount == nil {
		return ter.TecNO_TARGET
	}

	issuance, _, result := mptutil.ReadIssuance(view, id)
	if result != ter.TesSUCCESS {
		return result
	}
	if issuance.Issuer != accountID {
		return ter.TefINTERNAL
	}
	if len(issuance.IssuerEncryptionKey) == 0 || issuance.Flags&entry.LsfMPTCanClawback == 0 || issuance.Flags&entry.LsfMPTCanHoldConfidentialBalance == 0 {
		return ter.TecNO_PERMISSION
	}
	holder, _, result := readConfidentialHolding(view, id, holderID)
	if result != ter.TesSUCCESS {
		return result
	}
	if len(holder.IssuerEncryptedBalance) == 0 || len(holder.HolderEncryptionKey) == 0 {
		return ter.TecNO_PERMISSION
	}
	if c.MPTAmount > issuance.ConfidentialOutstandingAmount || c.MPTAmount > issuance.OutstandingAmount {
		return ter.TecINSUFFICIENT_FUNDS
	}
	proof, _ := decodeFixed(c.ZKProof, mptcrypto.ClawbackProofSize)
	context, contextOK := mptcrypto.ClawbackContext(accountID, id, c.GetCommon().SeqProxy(), holderID)
	if !contextOK || !mptcrypto.VerifyClawback(proof, issuance.IssuerEncryptionKey, holder.IssuerEncryptedBalance, c.MPTAmount, context) {
		return ter.TecBAD_PROOF
	}
	return ter.TesSUCCESS
}

func (c *ConfidentialMPTClawback) Apply(ctx *tx.ApplyContext) ter.Result {
	id, _ := parseConfidentialID(c.MPTokenIssuanceID)
	holderID, _ := state.DecodeAccountID(c.Holder)
	issuance, issuanceKey, result := mptutil.ReadIssuance(ctx.View, id)
	if result != ter.TesSUCCESS {
		return ter.TecINTERNAL
	}
	holder, holderKey, result := readConfidentialHolding(ctx.View, id, holderID)
	if result != ter.TesSUCCESS {
		return ter.TecINTERNAL
	}
	holderZero, ok := mptcrypto.CanonicalZero(holder.HolderEncryptionKey, holderID, id)
	if !ok {
		return ter.TecINTERNAL
	}
	issuerZero, ok := mptcrypto.CanonicalZero(issuance.IssuerEncryptionKey, holderID, id)
	if !ok {
		return ter.TecINTERNAL
	}
	holder.ConfidentialBalanceInbox = holderZero
	holder.ConfidentialBalanceSpending = holderZero
	holder.IssuerEncryptedBalance = issuerZero
	holder.ConfidentialBalanceVersion++
	if len(holder.AuditorEncryptedBalance) != 0 {
		if len(issuance.AuditorEncryptionKey) == 0 {
			return ter.TecINTERNAL
		}
		auditorZero, ok := mptcrypto.CanonicalZero(issuance.AuditorEncryptionKey, holderID, id)
		if !ok {
			return ter.TecINTERNAL
		}
		holder.AuditorEncryptedBalance = auditorZero
	}
	if c.MPTAmount > issuance.ConfidentialOutstandingAmount || c.MPTAmount > issuance.OutstandingAmount {
		return ter.TecINTERNAL
	}
	issuance.ConfidentialOutstandingAmount -= c.MPTAmount
	issuance.OutstandingAmount -= c.MPTAmount

	issuanceData, err := state.SerializeMPTokenIssuance(issuance)
	if err != nil {
		return ter.TecINTERNAL
	}
	holderData, err := state.SerializeMPToken(holder)
	if err != nil {
		return ter.TecINTERNAL
	}
	if err := ctx.View.Update(holderKey, holderData); err != nil {
		return ter.TecINTERNAL
	}
	if err := ctx.View.Update(issuanceKey, issuanceData); err != nil {
		return ter.TecINTERNAL
	}
	return ter.TesSUCCESS
}
