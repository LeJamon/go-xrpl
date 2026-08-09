package mpt

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/crypto/mptcrypto"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
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
	if hasAmount {
		value, err := parseUInt64Field(rawAmount)
		if err != nil {
			return 0, fmt.Errorf("invalid MPTAmount: %w", err)
		}
		return value, nil
	}
	return 0, nil
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
func (c *ConfidentialMPTMergeInbox) Flatten() (map[string]any, error) { return tx.ReflectFlatten(c) }
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

func decodeFixed(value string, size int) ([]byte, bool) {
	decoded, err := hex.DecodeString(value)
	return decoded, err == nil && len(decoded) == size
}

func confidentialIssuerIsAccount(id [24]byte, account string) bool {
	accountID, err := state.DecodeAccountID(account)
	return err == nil && accountID == [20]byte(id[4:])
}

func (c *ConfidentialMPTConvert) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
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
		key, valid := decodeFixed(*c.HolderEncryptionKey, mptcrypto.PublicKeySize)
		if !valid || !mptcrypto.ValidPublicKey(key) {
			return ter.Errorf(ter.TemMALFORMED, "invalid HolderEncryptionKey")
		}
		if c.ZKProof == nil {
			return ter.Errorf(ter.TemMALFORMED, "ZKProof required with HolderEncryptionKey")
		}
		if proof, valid := decodeFixed(*c.ZKProof, mptcrypto.ConvertProofSize); !valid || len(proof) != mptcrypto.ConvertProofSize {
			return ter.Errorf(ter.TemMALFORMED, "invalid ZKProof")
		}
	} else if c.ZKProof != nil {
		return ter.Errorf(ter.TemMALFORMED, "ZKProof requires HolderEncryptionKey")
	}
	for _, value := range []*string{&c.HolderEncryptedAmount, &c.IssuerEncryptedAmount, c.AuditorEncryptedAmount} {
		if value == nil {
			continue
		}
		ciphertext, valid := decodeFixed(*value, mptcrypto.CiphertextSize)
		if !valid || !mptcrypto.ValidCiphertext(ciphertext) {
			return ter.Errorf(ter.TemBAD_CIPHERTEXT, "invalid ciphertext")
		}
	}
	if _, valid := decodeFixed(c.BlindingFactor, mptcrypto.BlindingFactorSize); !valid {
		return ter.Errorf(ter.TemMALFORMED, "invalid BlindingFactor")
	}
	return nil
}

func (c *ConfidentialMPTMergeInbox) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
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
	commitment, valid := decodeFixed(c.BalanceCommitment, mptcrypto.CommitmentSize)
	if !valid || !mptcrypto.ValidPublicKey(commitment) {
		return ter.Errorf(ter.TemMALFORMED, "invalid BalanceCommitment")
	}
	for _, value := range []*string{&c.HolderEncryptedAmount, &c.IssuerEncryptedAmount, c.AuditorEncryptedAmount} {
		if value == nil {
			continue
		}
		ciphertext, valid := decodeFixed(*value, mptcrypto.CiphertextSize)
		if !valid || !mptcrypto.ValidCiphertext(ciphertext) {
			return ter.Errorf(ter.TemBAD_CIPHERTEXT, "invalid ciphertext")
		}
	}
	if _, valid := decodeFixed(c.BlindingFactor, mptcrypto.BlindingFactorSize); !valid {
		return ter.Errorf(ter.TemMALFORMED, "invalid BlindingFactor")
	}
	if _, valid := decodeFixed(c.ZKProof, mptcrypto.ConvertBackProofSize); !valid {
		return ter.Errorf(ter.TemMALFORMED, "invalid ZKProof")
	}
	return nil
}

func confidentialBaseFee(transaction tx.Transaction, config tx.EngineConfig) uint64 {
	return sign.CalculateDefaultBaseFee(transaction, config) + 9*config.BaseFee
}
func (c *ConfidentialMPTConvert) CalculateBaseFee(_ tx.LedgerView, config tx.EngineConfig) uint64 {
	return confidentialBaseFee(c, config)
}
func (c *ConfidentialMPTMergeInbox) CalculateBaseFee(_ tx.LedgerView, config tx.EngineConfig) uint64 {
	return confidentialBaseFee(c, config)
}
func (c *ConfidentialMPTConvertBack) CalculateBaseFee(_ tx.LedgerView, config tx.EngineConfig) uint64 {
	return confidentialBaseFee(c, config)
}

func readConfidentialState(view tx.LedgerView, id [24]byte, account string) (*state.MPTokenIssuanceData, keylet.Keylet, *state.MPTokenData, keylet.Keylet, [20]byte, ter.Result) {
	issuance, issuanceKey, accountID, result := readConfidentialIssuance(view, id, account)
	if result != ter.TesSUCCESS {
		return nil, issuanceKey, nil, keylet.Keylet{}, accountID, result
	}
	token, tokenKey, result := readConfidentialHolding(view, id, accountID)
	return issuance, issuanceKey, token, tokenKey, accountID, result
}

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

func commonConfidentialChecks(view tx.LedgerView, id [24]byte, account string, requireIssuerKey bool) (*state.MPTokenIssuanceData, keylet.Keylet, *state.MPTokenData, keylet.Keylet, [20]byte, ter.Result) {
	issuance, issuanceKey, token, tokenKey, accountID, result := readConfidentialState(view, id, account)
	if issuance == nil {
		return nil, issuanceKey, nil, tokenKey, accountID, result
	}
	if issuance.Flags&entry.LsfMPTCanHoldConfidentialBalance == 0 || (requireIssuerKey && len(issuance.IssuerEncryptionKey) == 0) {
		return nil, issuanceKey, nil, tokenKey, accountID, ter.TecNO_PERMISSION
	}
	if issuance.Issuer == accountID {
		return nil, issuanceKey, nil, tokenKey, accountID, ter.TefINTERNAL
	}
	if result != ter.TesSUCCESS {
		return nil, issuanceKey, nil, tokenKey, accountID, result
	}
	return issuance, issuanceKey, token, tokenKey, accountID, ter.TesSUCCESS
}

func participant(pub, ciphertext []byte) mptcrypto.Participant {
	return mptcrypto.Participant{PublicKey: pub, Ciphertext: ciphertext}
}

func (c *ConfidentialMPTConvert) Preclaim(view tx.LedgerView, _ tx.EngineConfig) ter.Result {
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
	if result := mptutil.RequireAuth(view, id, accountID, false); result != ter.TesSUCCESS {
		return result
	}
	if token.MPTAmount < c.MPTAmount {
		return ter.TecINSUFFICIENT_FUNDS
	}
	if len(token.HolderEncryptionKey) == 0 && c.HolderEncryptionKey == nil {
		return ter.TecNO_PERMISSION
	}
	if len(token.HolderEncryptionKey) != 0 && c.HolderEncryptionKey != nil {
		return ter.TecDUPLICATE
	}
	holderKey := token.HolderEncryptionKey
	proofOK := true
	if c.HolderEncryptionKey != nil {
		holderKey, _ = decodeFixed(*c.HolderEncryptionKey, mptcrypto.PublicKeySize)
		proof, _ := decodeFixed(*c.ZKProof, mptcrypto.ConvertProofSize)
		context, ok := mptcrypto.ConvertContext(accountID, id, c.GetCommon().SeqProxy())
		proofOK = ok && mptcrypto.VerifyConvert(proof, holderKey, context)
	}
	blindBytes, _ := decodeFixed(c.BlindingFactor, 32)
	var blind [32]byte
	copy(blind[:], blindBytes)
	holderCT, _ := decodeFixed(c.HolderEncryptedAmount, 66)
	issuerCT, _ := decodeFixed(c.IssuerEncryptedAmount, 66)
	var auditor *mptcrypto.Participant
	if c.AuditorEncryptedAmount != nil {
		ct, _ := decodeFixed(*c.AuditorEncryptedAmount, 66)
		value := participant(issuance.AuditorEncryptionKey, ct)
		auditor = &value
	}
	revealedOK := mptcrypto.VerifyRevealed(c.MPTAmount, blind, participant(holderKey, holderCT), participant(issuance.IssuerEncryptionKey, issuerCT), auditor)
	if !proofOK || !revealedOK {
		return ter.TecBAD_PROOF
	}
	return ter.TesSUCCESS
}

func (c *ConfidentialMPTMergeInbox) Preclaim(view tx.LedgerView, _ tx.EngineConfig) ter.Result {
	id, _ := parseConfidentialID(c.MPTokenIssuanceID)
	_, _, token, _, accountID, result := commonConfidentialChecks(view, id, c.Account, false)
	if result != ter.TesSUCCESS {
		return result
	}
	if len(token.ConfidentialBalanceInbox) == 0 || len(token.ConfidentialBalanceSpending) == 0 || len(token.HolderEncryptionKey) == 0 {
		return ter.TecNO_PERMISSION
	}
	if mptutil.IsFrozen(view, id, accountID) {
		return ter.TecLOCKED
	}
	return mptutil.RequireAuth(view, id, accountID, false)
}

func (c *ConfidentialMPTConvertBack) Preclaim(view tx.LedgerView, _ tx.EngineConfig) ter.Result {
	id, _ := parseConfidentialID(c.MPTokenIssuanceID)
	issuance, _, accountID, result := readConfidentialIssuance(view, id, c.Account)
	if result != ter.TesSUCCESS {
		return result
	}
	if issuance.Flags&entry.LsfMPTCanHoldConfidentialBalance == 0 || len(issuance.IssuerEncryptionKey) == 0 {
		return ter.TecNO_PERMISSION
	}
	if (c.AuditorEncryptedAmount != nil) != (len(issuance.AuditorEncryptionKey) != 0) {
		return ter.TecNO_PERMISSION
	}
	if issuance.Issuer == accountID {
		return ter.TefINTERNAL
	}
	token, _, result := readConfidentialHolding(view, id, accountID)
	if result != ter.TesSUCCESS {
		return result
	}
	if len(token.HolderEncryptionKey) == 0 || len(token.ConfidentialBalanceSpending) == 0 || len(token.IssuerEncryptedBalance) == 0 {
		return ter.TecNO_PERMISSION
	}
	if len(issuance.AuditorEncryptionKey) != 0 && len(token.AuditorEncryptedBalance) == 0 {
		return ter.TefINTERNAL
	}
	if issuance.ConfidentialOutstandingAmount < c.MPTAmount {
		return ter.TecINSUFFICIENT_FUNDS
	}
	if mptutil.IsFrozen(view, id, accountID) {
		return ter.TecLOCKED
	}
	if result := mptutil.RequireAuth(view, id, accountID, false); result != ter.TesSUCCESS {
		return result
	}
	blindBytes, _ := decodeFixed(c.BlindingFactor, 32)
	var blind [32]byte
	copy(blind[:], blindBytes)
	holderCT, _ := decodeFixed(c.HolderEncryptedAmount, 66)
	issuerCT, _ := decodeFixed(c.IssuerEncryptedAmount, 66)
	var auditor *mptcrypto.Participant
	if c.AuditorEncryptedAmount != nil {
		ct, _ := decodeFixed(*c.AuditorEncryptedAmount, 66)
		value := participant(issuance.AuditorEncryptionKey, ct)
		auditor = &value
	}
	revealedOK := mptcrypto.VerifyRevealed(c.MPTAmount, blind, participant(token.HolderEncryptionKey, holderCT), participant(issuance.IssuerEncryptionKey, issuerCT), auditor)
	proof, _ := decodeFixed(c.ZKProof, 816)
	commitment, _ := decodeFixed(c.BalanceCommitment, 33)
	context, contextOK := mptcrypto.ConvertBackContext(accountID, id, c.GetCommon().SeqProxy(), token.ConfidentialBalanceVersion)
	proofOK := contextOK && mptcrypto.VerifyConvertBack(proof, token.HolderEncryptionKey, token.ConfidentialBalanceSpending, commitment, c.MPTAmount, context)
	if !revealedOK || !proofOK {
		return ter.TecBAD_PROOF
	}
	return ter.TesSUCCESS
}

func serializeConfidential(ctx *tx.ApplyContext, issuanceKey keylet.Keylet, issuance *state.MPTokenIssuanceData, tokenKey keylet.Keylet, token *state.MPTokenData) ter.Result {
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

func (c *ConfidentialMPTConvert) Apply(ctx *tx.ApplyContext) ter.Result {
	id, _ := parseConfidentialID(c.MPTokenIssuanceID)
	issuance, issuanceKey, token, tokenKey, accountID, result := readConfidentialState(ctx.View, id, c.Account)
	if result != ter.TesSUCCESS {
		return ter.TefINTERNAL
	}
	if token.MPTAmount < c.MPTAmount || issuance.ConfidentialOutstandingAmount > protocol.MaxMPTokenAmount-c.MPTAmount {
		return ter.TecINTERNAL
	}
	holderCT, _ := decodeFixed(c.HolderEncryptedAmount, 66)
	issuerCT, _ := decodeFixed(c.IssuerEncryptedAmount, 66)
	if c.HolderEncryptionKey != nil {
		token.HolderEncryptionKey, _ = decodeFixed(*c.HolderEncryptionKey, 33)
	}
	initialized := len(token.ConfidentialBalanceInbox) != 0 || len(token.ConfidentialBalanceSpending) != 0 || len(token.IssuerEncryptedBalance) != 0 || len(token.AuditorEncryptedBalance) != 0
	if !initialized {
		zero, ok := mptcrypto.CanonicalZero(token.HolderEncryptionKey, accountID, id)
		if !ok {
			return ter.TecINTERNAL
		}
		token.ConfidentialBalanceInbox, token.ConfidentialBalanceSpending, token.IssuerEncryptedBalance = holderCT, zero, issuerCT
		if c.AuditorEncryptedAmount != nil {
			token.AuditorEncryptedBalance, _ = decodeFixed(*c.AuditorEncryptedAmount, 66)
		}
	} else {
		if len(token.ConfidentialBalanceInbox) == 0 || len(token.ConfidentialBalanceSpending) == 0 || len(token.IssuerEncryptedBalance) == 0 {
			return ter.TecINTERNAL
		}
		if c.AuditorEncryptedAmount != nil && len(token.AuditorEncryptedBalance) == 0 {
			return ter.TecINTERNAL
		}
		var ok bool
		if token.ConfidentialBalanceInbox, ok = mptcrypto.AddCiphertexts(token.ConfidentialBalanceInbox, holderCT); !ok {
			return ter.TecINTERNAL
		}
		if token.IssuerEncryptedBalance, ok = mptcrypto.AddCiphertexts(token.IssuerEncryptedBalance, issuerCT); !ok {
			return ter.TecINTERNAL
		}
		if c.AuditorEncryptedAmount != nil {
			auditorCT, _ := decodeFixed(*c.AuditorEncryptedAmount, 66)
			if token.AuditorEncryptedBalance, ok = mptcrypto.AddCiphertexts(token.AuditorEncryptedBalance, auditorCT); !ok {
				return ter.TecINTERNAL
			}
		}
	}
	token.MPTAmount -= c.MPTAmount
	issuance.ConfidentialOutstandingAmount += c.MPTAmount
	return serializeConfidential(ctx, issuanceKey, issuance, tokenKey, token)
}

func (c *ConfidentialMPTMergeInbox) Apply(ctx *tx.ApplyContext) ter.Result {
	id, _ := parseConfidentialID(c.MPTokenIssuanceID)
	_, _, token, tokenKey, accountID, result := readConfidentialState(ctx.View, id, c.Account)
	if result != ter.TesSUCCESS {
		return ter.TefINTERNAL
	}
	spending, ok := mptcrypto.AddCiphertexts(token.ConfidentialBalanceSpending, token.ConfidentialBalanceInbox)
	if !ok {
		return ter.TecINTERNAL
	}
	zero, ok := mptcrypto.CanonicalZero(token.HolderEncryptionKey, accountID, id)
	if !ok {
		return ter.TecINTERNAL
	}
	token.ConfidentialBalanceSpending, token.ConfidentialBalanceInbox = spending, zero
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

func (c *ConfidentialMPTConvertBack) Apply(ctx *tx.ApplyContext) ter.Result {
	id, _ := parseConfidentialID(c.MPTokenIssuanceID)
	issuance, issuanceKey, token, tokenKey, _, result := readConfidentialState(ctx.View, id, c.Account)
	if result != ter.TesSUCCESS {
		return ter.TefINTERNAL
	}
	if c.MPTAmount > uint64(math.MaxInt64)-token.MPTAmount || issuance.ConfidentialOutstandingAmount < c.MPTAmount {
		return ter.TecINTERNAL
	}
	holderCT, _ := decodeFixed(c.HolderEncryptedAmount, 66)
	issuerCT, _ := decodeFixed(c.IssuerEncryptedAmount, 66)
	var ok bool
	if token.ConfidentialBalanceSpending, ok = mptcrypto.SubtractCiphertexts(token.ConfidentialBalanceSpending, holderCT); !ok {
		return ter.TecINTERNAL
	}
	if token.IssuerEncryptedBalance, ok = mptcrypto.SubtractCiphertexts(token.IssuerEncryptedBalance, issuerCT); !ok {
		return ter.TecINTERNAL
	}
	if c.AuditorEncryptedAmount != nil {
		auditorCT, _ := decodeFixed(*c.AuditorEncryptedAmount, 66)
		if token.AuditorEncryptedBalance, ok = mptcrypto.SubtractCiphertexts(token.AuditorEncryptedBalance, auditorCT); !ok {
			return ter.TecINTERNAL
		}
	}
	token.MPTAmount += c.MPTAmount
	issuance.ConfidentialOutstandingAmount -= c.MPTAmount
	token.ConfidentialBalanceVersion++
	return serializeConfidential(ctx, issuanceKey, issuance, tokenKey, token)
}
