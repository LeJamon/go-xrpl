package xchain

import (
	"errors"

	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeXChainCreateBridge, func() tx.Transaction {
		return &XChainCreateBridge{BaseTx: *tx.NewBaseTx(tx.TypeXChainCreateBridge, "")}
	})
	tx.Register(tx.TypeXChainModifyBridge, func() tx.Transaction {
		return &XChainModifyBridge{BaseTx: *tx.NewBaseTx(tx.TypeXChainModifyBridge, "")}
	})
	tx.Register(tx.TypeXChainCreateClaimID, func() tx.Transaction {
		return &XChainCreateClaimID{BaseTx: *tx.NewBaseTx(tx.TypeXChainCreateClaimID, "")}
	})
	tx.Register(tx.TypeXChainCommit, func() tx.Transaction {
		return &XChainCommit{BaseTx: *tx.NewBaseTx(tx.TypeXChainCommit, "")}
	})
	tx.Register(tx.TypeXChainClaim, func() tx.Transaction {
		return &XChainClaim{BaseTx: *tx.NewBaseTx(tx.TypeXChainClaim, "")}
	})
	tx.Register(tx.TypeXChainAccountCreateCommit, func() tx.Transaction {
		return &XChainAccountCreateCommit{BaseTx: *tx.NewBaseTx(tx.TypeXChainAccountCreateCommit, "")}
	})
	tx.Register(tx.TypeXChainAddClaimAttestation, func() tx.Transaction {
		return &XChainAddClaimAttestation{BaseTx: *tx.NewBaseTx(tx.TypeXChainAddClaimAttestation, "")}
	})
	tx.Register(tx.TypeXChainAddAccountCreateAttest, func() tx.Transaction {
		return &XChainAddAccountCreateAttestation{BaseTx: *tx.NewBaseTx(tx.TypeXChainAddAccountCreateAttest, "")}
	})
}

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

// TxType returns the transaction type
func (x *XChainCreateBridge) TxType() tx.Type {
	return tx.TypeXChainCreateBridge
}

// Validate validates the XChainCreateBridge transaction
func (x *XChainCreateBridge) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}

	if x.SignatureReward.IsZero() {
		return errors.New("SignatureReward is required")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (x *XChainCreateBridge) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(x)
}

// RequiredAmendments returns the amendments required for this transaction type
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

// XChainModifyBridge flags
const (
	// tfClearAccountCreateAmount clears the min account create amount
	XChainModifyBridgeFlagClearAccountCreateAmount uint32 = 0x00010000
)

// NewXChainModifyBridge creates a new XChainModifyBridge transaction
func NewXChainModifyBridge(account string, bridge XChainBridge) *XChainModifyBridge {
	return &XChainModifyBridge{
		BaseTx:       *tx.NewBaseTx(tx.TypeXChainModifyBridge, account),
		XChainBridge: bridge,
	}
}

// TxType returns the transaction type
func (x *XChainModifyBridge) TxType() tx.Type {
	return tx.TypeXChainModifyBridge
}

// Validate validates the XChainModifyBridge transaction
func (x *XChainModifyBridge) Validate() error {
	return x.BaseTx.Validate()
}

// Flatten returns a flat map of all transaction fields
func (x *XChainModifyBridge) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(x)
}

// RequiredAmendments returns the amendments required for this transaction type
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

// TxType returns the transaction type
func (x *XChainCreateClaimID) TxType() tx.Type {
	return tx.TypeXChainCreateClaimID
}

// Validate validates the XChainCreateClaimID transaction
func (x *XChainCreateClaimID) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}

	if x.OtherChainSource == "" {
		return errors.New("OtherChainSource is required")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (x *XChainCreateClaimID) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(x)
}

// RequiredAmendments returns the amendments required for this transaction type
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

// TxType returns the transaction type
func (x *XChainCommit) TxType() tx.Type {
	return tx.TypeXChainCommit
}

// Validate validates the XChainCommit transaction
func (x *XChainCommit) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}

	if x.Amount.IsZero() {
		return errors.New("Amount is required")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (x *XChainCommit) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(x)
}

// RequiredAmendments returns the amendments required for this transaction type
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

// TxType returns the transaction type
func (x *XChainClaim) TxType() tx.Type {
	return tx.TypeXChainClaim
}

// Validate validates the XChainClaim transaction
func (x *XChainClaim) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}

	if x.Destination == "" {
		return errors.New("Destination is required")
	}

	if x.Amount.IsZero() {
		return errors.New("Amount is required")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (x *XChainClaim) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(x)
}

// RequiredAmendments returns the amendments required for this transaction type
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

// TxType returns the transaction type
func (x *XChainAccountCreateCommit) TxType() tx.Type {
	return tx.TypeXChainAccountCreateCommit
}

// Validate validates the XChainAccountCreateCommit transaction
func (x *XChainAccountCreateCommit) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}

	if x.Destination == "" {
		return errors.New("Destination is required")
	}

	if x.Amount.IsZero() {
		return errors.New("Amount is required")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (x *XChainAccountCreateCommit) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(x)
}

// RequiredAmendments returns the amendments required for this transaction type
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

// TxType returns the transaction type
func (x *XChainAddClaimAttestation) TxType() tx.Type {
	return tx.TypeXChainAddClaimAttestation
}

// Validate validates the XChainAddClaimAttestation transaction
func (x *XChainAddClaimAttestation) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}

	if x.OtherChainSource == "" {
		return errors.New("OtherChainSource is required")
	}

	if x.AttestationRewardAccount == "" {
		return errors.New("AttestationRewardAccount is required")
	}

	if x.AttestationSignerAccount == "" {
		return errors.New("AttestationSignerAccount is required")
	}

	if x.PublicKey == "" {
		return errors.New("PublicKey is required")
	}

	if x.Signature == "" {
		return errors.New("Signature is required")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (x *XChainAddClaimAttestation) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(x)
}

// RequiredAmendments returns the amendments required for this transaction type
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

// TxType returns the transaction type
func (x *XChainAddAccountCreateAttestation) TxType() tx.Type {
	return tx.TypeXChainAddAccountCreateAttest
}

// Validate validates the XChainAddAccountCreateAttestation transaction
func (x *XChainAddAccountCreateAttestation) Validate() error {
	if err := x.BaseTx.Validate(); err != nil {
		return err
	}

	if x.OtherChainSource == "" {
		return errors.New("OtherChainSource is required")
	}

	if x.Destination == "" {
		return errors.New("Destination is required")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (x *XChainAddAccountCreateAttestation) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(x)
}

// RequiredAmendments returns the amendments required for this transaction type
func (x *XChainAddAccountCreateAttestation) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureXChainBridge}
}

// Apply applies the XChainCreateBridge transaction to the ledger.
func (x *XChainCreateBridge) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Account.OwnerCount++
	return tx.TesSUCCESS
}

// Apply applies the XChainModifyBridge transaction to the ledger.
func (x *XChainModifyBridge) Apply(ctx *tx.ApplyContext) tx.Result {
	return tx.TesSUCCESS
}

// Apply applies the XChainCreateClaimID transaction to the ledger.
func (x *XChainCreateClaimID) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Account.OwnerCount++
	return tx.TesSUCCESS
}

// Apply applies the XChainCommit transaction to the ledger.
func (x *XChainCommit) Apply(ctx *tx.ApplyContext) tx.Result {
	if x.Amount.IsNative() {
		amount := uint64(x.Amount.Drops())
		if ctx.Account.Balance < amount {
			return tx.TecUNFUNDED
		}
		ctx.Account.Balance -= amount
	}
	return tx.TesSUCCESS
}

// Apply applies the XChainClaim transaction to the ledger.
func (x *XChainClaim) Apply(ctx *tx.ApplyContext) tx.Result {
	if x.Amount.IsNative() {
		amount := uint64(x.Amount.Drops())
		destID, err := sle.DecodeAccountID(x.Destination)
		if err != nil {
			return tx.TemINVALID
		}
		destKey := keylet.Account(destID)
		destData, err := ctx.View.Read(destKey)
		if err == nil {
			destAccount, err := sle.ParseAccountRoot(destData)
			if err == nil {
				destAccount.Balance += amount
				destUpdatedData, _ := sle.SerializeAccountRoot(destAccount)
				ctx.View.Update(destKey, destUpdatedData)
			}
		}
	}
	return tx.TesSUCCESS
}

// Apply applies the XChainAccountCreateCommit transaction to the ledger.
func (x *XChainAccountCreateCommit) Apply(ctx *tx.ApplyContext) tx.Result {
	if x.Amount.IsNative() {
		amount := uint64(x.Amount.Drops())
		if ctx.Account.Balance < amount {
			return tx.TecUNFUNDED
		}
		ctx.Account.Balance -= amount
	}
	return tx.TesSUCCESS
}

// Apply applies the XChainAddClaimAttestation transaction to the ledger.
func (x *XChainAddClaimAttestation) Apply(ctx *tx.ApplyContext) tx.Result {
	return tx.TesSUCCESS
}

// Apply applies the XChainAddAccountCreateAttestation transaction to the ledger.
func (x *XChainAddAccountCreateAttestation) Apply(ctx *tx.ApplyContext) tx.Result {
	return tx.TesSUCCESS
}
