package mpt

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/crypto/mptcrypto"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/protocol"
)

type ConfidentialMPTConvert struct {
	tx.BaseTx
	MPTokenIssuanceID      string  `json:"MPTokenIssuanceID" xrpl:"MPTokenIssuanceID"`
	MPTAmount              uint64  `json:"MPTAmount" xrpl:"MPTAmount"`
	HolderEncryptionKey    *string `json:"HolderEncryptionKey,omitempty" xrpl:"HolderEncryptionKey,omitempty"`
	HolderEncryptedAmount  string  `json:"HolderEncryptedAmount" xrpl:"HolderEncryptedAmount"`
	IssuerEncryptedAmount  string  `json:"IssuerEncryptedAmount" xrpl:"IssuerEncryptedAmount"`
	AuditorEncryptedAmount *string `json:"AuditorEncryptedAmount,omitempty" xrpl:"AuditorEncryptedAmount,omitempty"`
	BlindingFactor         string  `json:"BlindingFactor" xrpl:"BlindingFactor"`
	ZKProof                *string `json:"ZKProof,omitempty" xrpl:"ZKProof,omitempty"`
}

type ConfidentialMPTMergeInbox struct {
	tx.BaseTx
	MPTokenIssuanceID string `json:"MPTokenIssuanceID" xrpl:"MPTokenIssuanceID"`
}

type ConfidentialMPTConvertBack struct {
	tx.BaseTx
	MPTokenIssuanceID      string  `json:"MPTokenIssuanceID" xrpl:"MPTokenIssuanceID"`
	MPTAmount              uint64  `json:"MPTAmount" xrpl:"MPTAmount"`
	HolderEncryptedAmount  string  `json:"HolderEncryptedAmount" xrpl:"HolderEncryptedAmount"`
	IssuerEncryptedAmount  string  `json:"IssuerEncryptedAmount" xrpl:"IssuerEncryptedAmount"`
	AuditorEncryptedAmount *string `json:"AuditorEncryptedAmount,omitempty" xrpl:"AuditorEncryptedAmount,omitempty"`
	BlindingFactor         string  `json:"BlindingFactor" xrpl:"BlindingFactor"`
	ZKProof                string  `json:"ZKProof" xrpl:"ZKProof"`
	BalanceCommitment      string  `json:"BalanceCommitment" xrpl:"BalanceCommitment"`
}

func unmarshalConfidential(data []byte, target any) (uint64, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return 0, err
	}
	rawAmount, hasAmount := values["MPTAmount"]
	delete(values, "MPTAmount")
	base, err := json.Marshal(values)
	if err != nil {
		return 0, err
	}
	if err := json.Unmarshal(base, target); err != nil {
		return 0, err
	}
	if !hasAmount {
		return 0, nil
	}
	value, err := parseUInt64Field(rawAmount)
	if err != nil {
		return 0, fmt.Errorf("invalid MPTAmount: %w", err)
	}
	return value, nil
}

func (c *ConfidentialMPTConvert) UnmarshalJSON(data []byte) error {
	type alias ConfidentialMPTConvert
	var decoded alias
	amount, err := unmarshalConfidential(data, &decoded)
	if err != nil {
		return err
	}
	decoded.MPTAmount = amount
	*c = ConfidentialMPTConvert(decoded)
	return nil
}

func (c *ConfidentialMPTConvertBack) UnmarshalJSON(data []byte) error {
	type alias ConfidentialMPTConvertBack
	var decoded alias
	amount, err := unmarshalConfidential(data, &decoded)
	if err != nil {
		return err
	}
	decoded.MPTAmount = amount
	*c = ConfidentialMPTConvertBack(decoded)
	return nil
}

func (c *ConfidentialMPTConvert) TxType() tx.Type     { return tx.TypeConfidentialMPTConvert }
func (c *ConfidentialMPTMergeInbox) TxType() tx.Type  { return tx.TypeConfidentialMPTMergeInbox }
func (c *ConfidentialMPTConvertBack) TxType() tx.Type { return tx.TypeConfidentialMPTConvertBack }

func (c *ConfidentialMPTConvert) GetFlagsMask(*amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (c *ConfidentialMPTMergeInbox) GetFlagsMask(*amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (c *ConfidentialMPTConvertBack) GetFlagsMask(*amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (c *ConfidentialMPTConvert) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureConfidentialTransfer}
}

func (c *ConfidentialMPTMergeInbox) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureConfidentialTransfer}
}

func (c *ConfidentialMPTConvertBack) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureConfidentialTransfer}
}

func (c *ConfidentialMPTConvert) Flatten() (map[string]any, error) {
	result, err := tx.ReflectFlatten(c)
	if err == nil {
		result["MPTAmount"] = fmt.Sprintf("%d", c.MPTAmount)
	}
	return result, err
}

func (c *ConfidentialMPTMergeInbox) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(c)
}

func (c *ConfidentialMPTConvertBack) Flatten() (map[string]any, error) {
	result, err := tx.ReflectFlatten(c)
	if err == nil {
		result["MPTAmount"] = fmt.Sprintf("%d", c.MPTAmount)
	}
	return result, err
}

func parseConfidentialID(value string) ([24]byte, bool) {
	var result [24]byte
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(result) {
		return result, false
	}
	copy(result[:], decoded)
	return result, true
}

func decodeConfidentialField(value string, size int) ([]byte, bool) {
	decoded, err := hex.DecodeString(value)
	return decoded, err == nil && len(decoded) == size
}

func confidentialIssuerIsAccount(id [24]byte, account string) bool {
	accountID, err := state.DecodeAccountID(account)
	return err == nil && accountID == [20]byte(id[4:])
}

func confidentialRequiredFieldsPresent(common *tx.Common, fields ...string) bool {
	if common == nil || !common.HasField("TransactionType") {
		return true
	}
	for _, field := range fields {
		if !common.HasField(field) {
			return false
		}
	}
	return true
}

func validateEncryptedAmounts(holder, issuer string, auditor *string) error {
	for _, value := range []*string{&holder, &issuer, auditor} {
		if value == nil {
			continue
		}
		ciphertext, valid := decodeConfidentialField(*value, mptcrypto.CiphertextSize)
		if !valid || !mptcrypto.ValidCiphertext(ciphertext) {
			return ter.Errorf(ter.TemBAD_CIPHERTEXT, "invalid ciphertext")
		}
	}
	return nil
}

func (c *ConfidentialMPTConvert) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
	}
	if !confidentialRequiredFieldsPresent(c.GetCommon(), "MPTokenIssuanceID", "MPTAmount", "HolderEncryptedAmount", "IssuerEncryptedAmount", "BlindingFactor") {
		return ter.Errorf(ter.TemMALFORMED, "required confidential convert field is missing")
	}
	id, ok := parseConfidentialID(c.MPTokenIssuanceID)
	if !ok {
		return ter.Errorf(ter.TemINVALID, "invalid MPTokenIssuanceID")
	}
	if confidentialIssuerIsAccount(id, c.Account) {
		return ter.Errorf(ter.TemMALFORMED, "issuer cannot convert")
	}
	if c.MPTAmount > protocol.MaxMPTokenAmount {
		return ter.Errorf(ter.TemBAD_AMOUNT, "MPTAmount exceeds maximum")
	}
	if c.HolderEncryptionKey != nil {
		key, valid := decodeConfidentialField(*c.HolderEncryptionKey, mptcrypto.PublicKeySize)
		if !valid || !mptcrypto.ValidPublicKey(key) {
			return ter.Errorf(ter.TemMALFORMED, "invalid HolderEncryptionKey")
		}
		if c.ZKProof == nil {
			return ter.Errorf(ter.TemMALFORMED, "ZKProof required with HolderEncryptionKey")
		}
		if _, valid := decodeConfidentialField(*c.ZKProof, mptcrypto.ConvertProofSize); !valid {
			return ter.Errorf(ter.TemMALFORMED, "invalid ZKProof")
		}
	} else if c.ZKProof != nil {
		return ter.Errorf(ter.TemMALFORMED, "ZKProof requires HolderEncryptionKey")
	}
	if err := validateEncryptedAmounts(c.HolderEncryptedAmount, c.IssuerEncryptedAmount, c.AuditorEncryptedAmount); err != nil {
		return err
	}
	return nil
}

func (c *ConfidentialMPTMergeInbox) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
	}
	if !confidentialRequiredFieldsPresent(c.GetCommon(), "MPTokenIssuanceID") {
		return ter.Errorf(ter.TemMALFORMED, "MPTokenIssuanceID is missing")
	}
	id, ok := parseConfidentialID(c.MPTokenIssuanceID)
	if !ok {
		return ter.Errorf(ter.TemINVALID, "invalid MPTokenIssuanceID")
	}
	if confidentialIssuerIsAccount(id, c.Account) {
		return ter.Errorf(ter.TemMALFORMED, "issuer cannot merge inbox")
	}
	return nil
}

func (c *ConfidentialMPTConvertBack) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
	}
	if !confidentialRequiredFieldsPresent(c.GetCommon(), "MPTokenIssuanceID", "MPTAmount", "HolderEncryptedAmount", "IssuerEncryptedAmount", "BlindingFactor", "ZKProof", "BalanceCommitment") {
		return ter.Errorf(ter.TemMALFORMED, "required confidential convert-back field is missing")
	}
	id, ok := parseConfidentialID(c.MPTokenIssuanceID)
	if !ok {
		return ter.Errorf(ter.TemINVALID, "invalid MPTokenIssuanceID")
	}
	if confidentialIssuerIsAccount(id, c.Account) {
		return ter.Errorf(ter.TemMALFORMED, "issuer cannot convert back")
	}
	if c.MPTAmount == 0 || c.MPTAmount > protocol.MaxMPTokenAmount {
		return ter.Errorf(ter.TemBAD_AMOUNT, "invalid MPTAmount")
	}
	commitment, valid := decodeConfidentialField(c.BalanceCommitment, mptcrypto.CommitmentSize)
	if !valid || !mptcrypto.ValidCommitment(commitment) {
		return ter.Errorf(ter.TemMALFORMED, "invalid BalanceCommitment")
	}
	if err := validateEncryptedAmounts(c.HolderEncryptedAmount, c.IssuerEncryptedAmount, c.AuditorEncryptedAmount); err != nil {
		return err
	}
	if _, valid := decodeConfidentialField(c.ZKProof, mptcrypto.ConvertBackProofSize); !valid {
		return ter.Errorf(ter.TemMALFORMED, "invalid ZKProof")
	}
	return nil
}
