package xchain

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

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

func (x *XChainCreateBridge) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}

	if x.SignatureReward.IsZero() {
		return tx.Errorf(tx.TemMALFORMED, "SignatureReward is required")
	}

	return nil
}

func (x *XChainCreateBridge) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(x)
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

func (x *XChainModifyBridge) Validate() error {
	return x.BaseTx.Validate()
}

func (x *XChainModifyBridge) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(x)
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

func (x *XChainCreateClaimID) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}

	if x.OtherChainSource == "" {
		return tx.Errorf(tx.TemMALFORMED, "OtherChainSource is required")
	}

	return nil
}

func (x *XChainCreateClaimID) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(x)
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

func (x *XChainCommit) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}

	if x.Amount.IsZero() {
		return tx.Errorf(tx.TemBAD_AMOUNT, "Amount is required")
	}

	return nil
}

func (x *XChainCommit) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(x)
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

func (x *XChainClaim) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}

	if x.Destination == "" {
		return tx.Errorf(tx.TemMALFORMED, "Destination is required")
	}

	if x.Amount.IsZero() {
		return tx.Errorf(tx.TemBAD_AMOUNT, "Amount is required")
	}

	return nil
}

func (x *XChainClaim) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(x)
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

func (x *XChainAccountCreateCommit) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}

	if x.Destination == "" {
		return tx.Errorf(tx.TemMALFORMED, "Destination is required")
	}

	if x.Amount.IsZero() {
		return tx.Errorf(tx.TemBAD_AMOUNT, "Amount is required")
	}

	return nil
}

func (x *XChainAccountCreateCommit) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(x)
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
	WasLockingChainSend bool `json:"WasLockingChainSend" xrpl:"WasLockingChainSend,boolint"`
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

func (x *XChainAddClaimAttestation) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}

	if x.OtherChainSource == "" {
		return tx.Errorf(tx.TemMALFORMED, "OtherChainSource is required")
	}

	if x.AttestationRewardAccount == "" {
		return tx.Errorf(tx.TemMALFORMED, "AttestationRewardAccount is required")
	}

	if x.AttestationSignerAccount == "" {
		return tx.Errorf(tx.TemMALFORMED, "AttestationSignerAccount is required")
	}

	if x.PublicKey == "" {
		return tx.Errorf(tx.TemMALFORMED, "PublicKey is required")
	}

	if x.Signature == "" {
		return tx.Errorf(tx.TemMALFORMED, "Signature is required")
	}

	return nil
}

func (x *XChainAddClaimAttestation) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(x)
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
	WasLockingChainSend bool `json:"WasLockingChainSend" xrpl:"WasLockingChainSend,boolint"`

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

func (x *XChainAddAccountCreateAttestation) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}

	if x.OtherChainSource == "" {
		return tx.Errorf(tx.TemMALFORMED, "OtherChainSource is required")
	}

	if x.Destination == "" {
		return tx.Errorf(tx.TemMALFORMED, "Destination is required")
	}

	return nil
}

func (x *XChainAddAccountCreateAttestation) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(x)
}

func (x *XChainAddAccountCreateAttestation) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureXChainBridge}
}

// The XChain Apply methods are intentionally unimplemented. XChainBridge is
// SupportedNo, so the engine rejects these transactions at preflight with
// temDISABLED and Apply is unreachable. Each returns a hard error that mutates
// no state, guarding against the amendment being enabled before the real
// cross-chain semantics are implemented.

func (x *XChainCreateBridge) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("xchain create bridge apply: not implemented", "account", x.Account)
	return tx.TefINTERNAL
}

func (x *XChainModifyBridge) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("xchain modify bridge apply: not implemented", "account", x.Account)
	return tx.TefINTERNAL
}

func (x *XChainCreateClaimID) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("xchain create claim id apply: not implemented", "account", x.Account)
	return tx.TefINTERNAL
}

func (x *XChainCommit) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("xchain commit apply: not implemented", "account", x.Account)
	return tx.TefINTERNAL
}

func (x *XChainClaim) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("xchain claim apply: not implemented", "account", x.Account)
	return tx.TefINTERNAL
}

func (x *XChainAccountCreateCommit) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("xchain account create commit apply: not implemented", "account", x.Account)
	return tx.TefINTERNAL
}

func (x *XChainAddClaimAttestation) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("xchain add claim attestation apply: not implemented", "account", x.Account)
	return tx.TefINTERNAL
}

func (x *XChainAddAccountCreateAttestation) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("xchain add account create attestation apply: not implemented", "account", x.Account)
	return tx.TefINTERNAL
}
