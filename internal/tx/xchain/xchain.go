package xchain

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// tfClearAccountCreateAmount is the sole XChainModifyBridge type-specific flag:
// it clears the bridge's MinAccountCreateAmount.
const tfClearAccountCreateAmount uint32 = 0x00010000

// tfXChainModifyBridgeMask rejects every flag outside the universal set and
// tfClearAccountCreateAmount (rippled BridgeModify::getFlagsMask).
const tfXChainModifyBridgeMask uint32 = ^(tx.TfUniversal | tfClearAccountCreateAmount)

// XChainBridge identifies a cross-chain bridge
type XChainBridge struct {
	LockingChainDoor  string   `json:"LockingChainDoor"`
	LockingChainIssue tx.Asset `json:"LockingChainIssue"`
	IssuingChainDoor  string   `json:"IssuingChainDoor"`
	IssuingChainIssue tx.Asset `json:"IssuingChainIssue"`
}

// XChainCreateBridge creates a new cross-chain bridge.
type XChainCreateBridge struct {
	tx.BaseTx

	// XChainBridge identifies the bridge (required)
	XChainBridge XChainBridge `json:"XChainBridge" xrpl:"XChainBridge"`

	// SignatureReward is the reward for witnesses (required)
	SignatureReward tx.Amount `json:"SignatureReward" xrpl:"SignatureReward,amount"`

	// MinAccountCreateAmount is the min amount for account creation (optional)
	MinAccountCreateAmount *tx.Amount `json:"MinAccountCreateAmount,omitempty" xrpl:"MinAccountCreateAmount,omitempty,amount"`
}

// NewXChainCreateBridge creates a new XChainCreateBridge transaction
func NewXChainCreateBridge(account string, bridge XChainBridge, signatureReward tx.Amount) *XChainCreateBridge {
	return &XChainCreateBridge{
		BaseTx:          *tx.NewBaseTx(tx.TypeXChainCreateBridge, account),
		XChainBridge:    bridge,
		SignatureReward: signatureReward,
	}
}

func (x *XChainCreateBridge) TxType() tx.Type {
	return tx.TypeXChainCreateBridge
}

// GetFlagsMask adopts the engine FlagsMasker seam. XChainCreateBridge defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (x *XChainCreateBridge) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (x *XChainCreateBridge) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}

	return validateCreateBridge(x.Account, x.XChainBridge, x.SignatureReward, x.MinAccountCreateAmount)
}

func (x *XChainCreateBridge) Flatten() (map[string]any, error) {
	return flattenXChain(x, x.XChainBridge)
}

func (x *XChainCreateBridge) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureXChainBridge}
}

// XChainModifyBridge modifies an existing cross-chain bridge.
type XChainModifyBridge struct {
	tx.BaseTx

	// XChainBridge identifies the bridge (required)
	XChainBridge XChainBridge `json:"XChainBridge" xrpl:"XChainBridge"`

	// SignatureReward is the new reward for witnesses (optional)
	SignatureReward *tx.Amount `json:"SignatureReward,omitempty" xrpl:"SignatureReward,omitempty,amount"`

	// MinAccountCreateAmount is the new min amount (optional)
	MinAccountCreateAmount *tx.Amount `json:"MinAccountCreateAmount,omitempty" xrpl:"MinAccountCreateAmount,omitempty,amount"`
}

// NewXChainModifyBridge creates a new XChainModifyBridge transaction
func NewXChainModifyBridge(account string, bridge XChainBridge) *XChainModifyBridge {
	return &XChainModifyBridge{
		BaseTx:       *tx.NewBaseTx(tx.TypeXChainModifyBridge, account),
		XChainBridge: bridge,
	}
}

func (x *XChainModifyBridge) TxType() tx.Type {
	return tx.TypeXChainModifyBridge
}

// GetFlagsMask adopts the engine FlagsMasker seam with the XChainModifyBridge
// invalid-flags mask (rippled BridgeModify::getFlagsMask =
// tfXChainModifyBridgeMask), checked at preflight0.
func (x *XChainModifyBridge) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tfXChainModifyBridgeMask
}

func (x *XChainModifyBridge) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}
	return validateModifyBridge(x.Account, x.XChainBridge, x.SignatureReward, x.MinAccountCreateAmount, x.GetFlags())
}

func (x *XChainModifyBridge) Flatten() (map[string]any, error) {
	return flattenXChain(x, x.XChainBridge)
}

func (x *XChainModifyBridge) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureXChainBridge}
}

// XChainCreateClaimID creates a claim ID for cross-chain transfers.
type XChainCreateClaimID struct {
	tx.BaseTx

	// XChainBridge identifies the bridge (required)
	XChainBridge XChainBridge `json:"XChainBridge" xrpl:"XChainBridge"`

	// SignatureReward is the reward for witnesses (required)
	SignatureReward tx.Amount `json:"SignatureReward" xrpl:"SignatureReward,amount"`

	// OtherChainSource is the source account on the other chain (required)
	OtherChainSource string `json:"OtherChainSource" xrpl:"OtherChainSource"`
}

// NewXChainCreateClaimID creates a new XChainCreateClaimID transaction
func NewXChainCreateClaimID(account string, bridge XChainBridge, signatureReward tx.Amount, otherChainSource string) *XChainCreateClaimID {
	return &XChainCreateClaimID{
		BaseTx:           *tx.NewBaseTx(tx.TypeXChainCreateClaimID, account),
		XChainBridge:     bridge,
		SignatureReward:  signatureReward,
		OtherChainSource: otherChainSource,
	}
}

func (x *XChainCreateClaimID) TxType() tx.Type {
	return tx.TypeXChainCreateClaimID
}

// GetFlagsMask adopts the engine FlagsMasker seam. XChainCreateClaimID defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (x *XChainCreateClaimID) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (x *XChainCreateClaimID) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}
	if err := validateBridgeFields(x.XChainBridge); err != nil {
		return err
	}

	if x.OtherChainSource == "" {
		return ter.Errorf(ter.TemMALFORMED, "OtherChainSource is required")
	}
	if !x.SignatureReward.IsNative() || x.SignatureReward.IsNegative() || !isLegalNet(x.SignatureReward) {
		return ter.Errorf(ter.TemXCHAIN_BRIDGE_BAD_REWARD_AMOUNT, "invalid signature reward")
	}
	return nil
}

func (x *XChainCreateClaimID) Flatten() (map[string]any, error) {
	return flattenXChain(x, x.XChainBridge)
}

func (x *XChainCreateClaimID) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureXChainBridge}
}

// XChainCommit commits assets to a cross-chain transfer.
type XChainCommit struct {
	tx.BaseTx

	// XChainBridge identifies the bridge (required)
	XChainBridge XChainBridge `json:"XChainBridge" xrpl:"XChainBridge"`

	// XChainClaimID is the claim ID (required)
	XChainClaimID uint64 `json:"XChainClaimID" xrpl:"XChainClaimID"`

	// Amount is the amount to transfer (required)
	Amount tx.Amount `json:"Amount" xrpl:"Amount,amount"`

	// OtherChainDestination is the destination on the other chain (optional)
	OtherChainDestination string `json:"OtherChainDestination,omitempty" xrpl:"OtherChainDestination,omitempty"`
}

// NewXChainCommit creates a new XChainCommit transaction
func NewXChainCommit(account string, bridge XChainBridge, claimID uint64, amount tx.Amount) *XChainCommit {
	return &XChainCommit{
		BaseTx:        *tx.NewBaseTx(tx.TypeXChainCommit, account),
		XChainBridge:  bridge,
		XChainClaimID: claimID,
		Amount:        amount,
	}
}

func (x *XChainCommit) TxType() tx.Type {
	return tx.TypeXChainCommit
}

// GetFlagsMask adopts the engine FlagsMasker seam. XChainCommit defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (x *XChainCommit) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (x *XChainCommit) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}
	if err := validateBridgeFields(x.XChainBridge); err != nil {
		return err
	}

	if x.Amount.Signum() <= 0 || !isLegalNet(x.Amount) {
		return ter.Errorf(ter.TemBAD_AMOUNT, "invalid commit amount")
	}
	if !assetEqual(assetOf(x.Amount), x.XChainBridge.LockingChainIssue) &&
		!assetEqual(assetOf(x.Amount), x.XChainBridge.IssuingChainIssue) {
		return ter.Errorf(ter.TemBAD_ISSUER, "amount is not a bridge issue")
	}
	return nil
}

func (x *XChainCommit) Flatten() (map[string]any, error) {
	return flattenXChain(x, x.XChainBridge)
}

func (x *XChainCommit) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureXChainBridge}
}

// XChainClaim claims assets from a cross-chain transfer.
type XChainClaim struct {
	tx.BaseTx

	// XChainBridge identifies the bridge (required)
	XChainBridge XChainBridge `json:"XChainBridge" xrpl:"XChainBridge"`

	// XChainClaimID is the claim ID (required)
	XChainClaimID uint64 `json:"XChainClaimID" xrpl:"XChainClaimID"`

	// Destination is the account to receive the assets (required)
	Destination string `json:"Destination" xrpl:"Destination"`

	// DestinationTag is an arbitrary tag (optional)
	DestinationTag *uint32 `json:"DestinationTag,omitempty" xrpl:"DestinationTag,omitempty"`

	// Amount is the amount to claim (required)
	Amount tx.Amount `json:"Amount" xrpl:"Amount,amount"`
}

// NewXChainClaim creates a new XChainClaim transaction
func NewXChainClaim(account string, bridge XChainBridge, claimID uint64, destination string, amount tx.Amount) *XChainClaim {
	return &XChainClaim{
		BaseTx:        *tx.NewBaseTx(tx.TypeXChainClaim, account),
		XChainBridge:  bridge,
		XChainClaimID: claimID,
		Destination:   destination,
		Amount:        amount,
	}
}

func (x *XChainClaim) TxType() tx.Type {
	return tx.TypeXChainClaim
}

// GetFlagsMask adopts the engine FlagsMasker seam. XChainClaim defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (x *XChainClaim) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (x *XChainClaim) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}
	if err := validateBridgeFields(x.XChainBridge); err != nil {
		return err
	}

	if x.Destination == "" {
		return ter.Errorf(ter.TemMALFORMED, "Destination is required")
	}

	if x.Amount.Signum() <= 0 ||
		(!assetEqual(assetOf(x.Amount), x.XChainBridge.LockingChainIssue) &&
			!assetEqual(assetOf(x.Amount), x.XChainBridge.IssuingChainIssue)) {
		return ter.Errorf(ter.TemBAD_AMOUNT, "invalid claim amount")
	}
	return nil
}

func (x *XChainClaim) Flatten() (map[string]any, error) {
	return flattenXChain(x, x.XChainBridge)
}

func (x *XChainClaim) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureXChainBridge}
}

// XChainAccountCreateCommit commits to create an account on the other chain.
type XChainAccountCreateCommit struct {
	tx.BaseTx

	// XChainBridge identifies the bridge (required)
	XChainBridge XChainBridge `json:"XChainBridge" xrpl:"XChainBridge"`

	// Destination is the account to create (required)
	Destination string `json:"Destination" xrpl:"Destination"`

	// Amount is the amount to send (required)
	Amount tx.Amount `json:"Amount" xrpl:"Amount,amount"`

	// SignatureReward is the reward for witnesses (required)
	SignatureReward tx.Amount `json:"SignatureReward" xrpl:"SignatureReward,amount"`
}

// NewXChainAccountCreateCommit creates a new XChainAccountCreateCommit transaction
func NewXChainAccountCreateCommit(account string, bridge XChainBridge, destination string, amount, signatureReward tx.Amount) *XChainAccountCreateCommit {
	return &XChainAccountCreateCommit{
		BaseTx:          *tx.NewBaseTx(tx.TypeXChainAccountCreateCommit, account),
		XChainBridge:    bridge,
		Destination:     destination,
		Amount:          amount,
		SignatureReward: signatureReward,
	}
}

func (x *XChainAccountCreateCommit) TxType() tx.Type {
	return tx.TypeXChainAccountCreateCommit
}

// GetFlagsMask adopts the engine FlagsMasker seam. XChainAccountCreateCommit
// defines no type-specific flags, so it uses the base universal mask, checked at
// preflight0.
func (x *XChainAccountCreateCommit) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (x *XChainAccountCreateCommit) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}
	if err := validateBridgeFields(x.XChainBridge); err != nil {
		return err
	}

	if x.Destination == "" {
		return ter.Errorf(ter.TemMALFORMED, "Destination is required")
	}

	if x.Amount.Signum() <= 0 || !x.Amount.IsNative() {
		return ter.Errorf(ter.TemBAD_AMOUNT, "account-create amount must be positive XRP")
	}
	if x.SignatureReward.IsNegative() || !x.SignatureReward.IsNative() ||
		!assetEqual(assetOf(x.SignatureReward), assetOf(x.Amount)) {
		return ter.Errorf(ter.TemBAD_AMOUNT, "account-create reward must be non-negative XRP")
	}
	return nil
}

func (x *XChainAccountCreateCommit) Flatten() (map[string]any, error) {
	return flattenXChain(x, x.XChainBridge)
}

func (x *XChainAccountCreateCommit) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureXChainBridge}
}

// XChainAddClaimAttestation adds a witness attestation for a claim.
type XChainAddClaimAttestation struct {
	tx.BaseTx

	// XChainBridge identifies the bridge (required)
	XChainBridge XChainBridge `json:"XChainBridge" xrpl:"XChainBridge"`

	// XChainClaimID is the claim ID (required)
	XChainClaimID uint64 `json:"XChainClaimID" xrpl:"XChainClaimID"`

	// OtherChainSource is the source on the other chain (required)
	OtherChainSource string `json:"OtherChainSource" xrpl:"OtherChainSource"`

	// Amount is the amount attested (required)
	Amount tx.Amount `json:"Amount" xrpl:"Amount,amount"`

	// AttestationRewardAccount receives the reward (required)
	AttestationRewardAccount string `json:"AttestationRewardAccount" xrpl:"AttestationRewardAccount"`

	// AttestationSignerAccount is the signer account (required)
	AttestationSignerAccount string `json:"AttestationSignerAccount" xrpl:"AttestationSignerAccount"`

	// Destination is the destination account (optional)
	Destination string `json:"Destination,omitempty" xrpl:"Destination,omitempty"`

	// PublicKey is the signer's public key (required)
	PublicKey string `json:"PublicKey" xrpl:"PublicKey"`

	// Signature is the attestation signature (required)
	Signature string `json:"Signature" xrpl:"Signature"`

	// WasLockingChainSend indicates if this was a locking chain send (required)
	WasLockingChainSend uint8 `json:"WasLockingChainSend" xrpl:"WasLockingChainSend"`
}

// NewXChainAddClaimAttestation creates a new XChainAddClaimAttestation transaction
func NewXChainAddClaimAttestation(account string, bridge XChainBridge, claimID uint64) *XChainAddClaimAttestation {
	return &XChainAddClaimAttestation{
		BaseTx:        *tx.NewBaseTx(tx.TypeXChainAddClaimAttestation, account),
		XChainBridge:  bridge,
		XChainClaimID: claimID,
	}
}

func (x *XChainAddClaimAttestation) TxType() tx.Type {
	return tx.TypeXChainAddClaimAttestation
}

// GetFlagsMask adopts the engine FlagsMasker seam. XChainAddClaimAttestation
// defines no type-specific flags, so it uses the base universal mask, checked at
// preflight0.
func (x *XChainAddClaimAttestation) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (x *XChainAddClaimAttestation) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}
	if err := validateBridgeFields(x.XChainBridge); err != nil {
		return err
	}

	if x.OtherChainSource == "" {
		return ter.Errorf(ter.TemMALFORMED, "OtherChainSource is required")
	}

	if x.AttestationRewardAccount == "" {
		return ter.Errorf(ter.TemMALFORMED, "AttestationRewardAccount is required")
	}

	if x.AttestationSignerAccount == "" {
		return ter.Errorf(ter.TemMALFORMED, "AttestationSignerAccount is required")
	}

	if x.PublicKey == "" {
		return ter.Errorf(ter.TemMALFORMED, "PublicKey is required")
	}

	if x.Signature == "" {
		return ter.Errorf(ter.TemMALFORMED, "Signature is required")
	}

	return validateClaimAttestation(x)
}

func (x *XChainAddClaimAttestation) Flatten() (map[string]any, error) {
	return flattenXChain(x, x.XChainBridge)
}

func (x *XChainAddClaimAttestation) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureXChainBridge}
}

// XChainAddAccountCreateAttestation adds a witness attestation for account creation.
type XChainAddAccountCreateAttestation struct {
	tx.BaseTx

	// XChainBridge identifies the bridge (required)
	XChainBridge XChainBridge `json:"XChainBridge" xrpl:"XChainBridge"`

	// OtherChainSource is the source on the other chain (required)
	OtherChainSource string `json:"OtherChainSource" xrpl:"OtherChainSource"`

	// Destination is the destination account (required)
	Destination string `json:"Destination" xrpl:"Destination"`

	// Amount is the amount attested (required)
	Amount tx.Amount `json:"Amount" xrpl:"Amount,amount"`

	// SignatureReward is the signature reward (required)
	SignatureReward tx.Amount `json:"SignatureReward" xrpl:"SignatureReward,amount"`

	// AttestationRewardAccount receives the reward (required)
	AttestationRewardAccount string `json:"AttestationRewardAccount" xrpl:"AttestationRewardAccount"`

	// AttestationSignerAccount is the signer account (required)
	AttestationSignerAccount string `json:"AttestationSignerAccount" xrpl:"AttestationSignerAccount"`

	// PublicKey is the signer's public key (required)
	PublicKey string `json:"PublicKey" xrpl:"PublicKey"`

	// Signature is the attestation signature (required)
	Signature string `json:"Signature" xrpl:"Signature"`

	// WasLockingChainSend indicates if this was a locking chain send (required)
	WasLockingChainSend uint8 `json:"WasLockingChainSend" xrpl:"WasLockingChainSend"`

	// XChainAccountCreateCount is the create count (required)
	XChainAccountCreateCount uint64 `json:"XChainAccountCreateCount" xrpl:"XChainAccountCreateCount"`
}

// NewXChainAddAccountCreateAttestation creates a new XChainAddAccountCreateAttestation transaction
func NewXChainAddAccountCreateAttestation(account string, bridge XChainBridge) *XChainAddAccountCreateAttestation {
	return &XChainAddAccountCreateAttestation{
		BaseTx:       *tx.NewBaseTx(tx.TypeXChainAddAccountCreateAttest, account),
		XChainBridge: bridge,
	}
}

func (x *XChainAddAccountCreateAttestation) TxType() tx.Type {
	return tx.TypeXChainAddAccountCreateAttest
}

// GetFlagsMask adopts the engine FlagsMasker seam. XChainAddAccountCreateAttestation
// defines no type-specific flags, so it uses the base universal mask, checked at
// preflight0.
func (x *XChainAddAccountCreateAttestation) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (x *XChainAddAccountCreateAttestation) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}
	if err := validateBridgeFields(x.XChainBridge); err != nil {
		return err
	}

	if x.OtherChainSource == "" {
		return ter.Errorf(ter.TemMALFORMED, "OtherChainSource is required")
	}

	if x.Destination == "" {
		return ter.Errorf(ter.TemMALFORMED, "Destination is required")
	}

	return validateCreateAccountAttestation(x)
}

func (x *XChainAddAccountCreateAttestation) Flatten() (map[string]any, error) {
	return flattenXChain(x, x.XChainBridge)
}

func (x *XChainAddAccountCreateAttestation) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureXChainBridge}
}
