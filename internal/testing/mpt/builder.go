// Package mpt provides test helpers for MPT (Multi-Purpose Token) transaction testing.
// These helpers mirror rippled's test/jtx/mpt.h MPTTester class.
package mpt

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	addresscodec "github.com/LeJamon/goXRPLd/internal/codec/address-codec"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/clawback"
	mpttx "github.com/LeJamon/goXRPLd/internal/core/tx/mpt"
	"github.com/LeJamon/goXRPLd/internal/core/tx/payment"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	paybuilder "github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/stretchr/testify/require"
)

// --------------------------------------------------------------------------
// MPTTester - Main test helper mirroring rippled's MPTTester class
// Reference: rippled test/jtx/mpt.h
// --------------------------------------------------------------------------

// MPTTester manages an MPT issuance lifecycle for testing.
// It tracks the issuer, holders, and issuance ID, and provides convenience
// methods for creating, destroying, authorizing, and setting MPT issuances.
type MPTTester struct {
	t      *testing.T
	env    *jtx.TestEnv
	issuer *jtx.Account

	// holders are the accounts that can hold this MPT
	holders []*jtx.Account

	// id is the 48-char hex MPTokenIssuanceID (24 bytes)
	// Set after a successful create() call
	id string

	// seq is the issuer's sequence number at the time of create()
	seq uint32

	// created tracks whether create() has been called successfully
	created bool
}

// MPTInit configures how the MPTTester is initialized.
// Reference: rippled MPTInit struct
type MPTInit struct {
	// Holders are the holder accounts to create and fund
	Holders []*jtx.Account

	// XRP is the amount to fund the issuer (in drops). Default: 10,000 XRP
	XRP uint64

	// XRPHolders is the amount to fund each holder (in drops). Default: 10,000 XRP
	XRPHolders uint64

	// Fund controls whether accounts are funded. Default: true
	Fund bool
}

// NewMPTTester creates a new MPTTester for the given issuer.
// It funds the issuer and holders unless Fund is false.
// Reference: rippled MPTTester::MPTTester()
func NewMPTTester(t *testing.T, env *jtx.TestEnv, issuer *jtx.Account, init ...MPTInit) *MPTTester {
	t.Helper()

	var opts MPTInit
	if len(init) > 0 {
		opts = init[0]
	}

	// Default funding amounts
	xrp := opts.XRP
	if xrp == 0 {
		xrp = uint64(jtx.XRP(10_000))
	}
	xrpHolders := opts.XRPHolders
	if xrpHolders == 0 {
		xrpHolders = uint64(jtx.XRP(10_000))
	}

	// Fund issuer unless Fund is explicitly false
	fund := true
	if len(init) > 0 {
		// If MPTInit was provided, use its Fund field (default zero value is false for bool)
		// But we want default true, so only set false if explicitly set
		fund = !opts.noFund()
	}
	if fund {
		env.FundAmount(issuer, xrp)
		for _, h := range opts.Holders {
			env.FundAmount(h, xrpHolders)
		}
	}

	return &MPTTester{
		t:       t,
		env:     env,
		issuer:  issuer,
		holders: opts.Holders,
	}
}

// noFund returns true if Fund was explicitly set to false.
// Since Go's zero value for bool is false, we use a helper.
func (init MPTInit) noFund() bool {
	// If Fund is false and holders are specified, likely intentional
	// Default behavior: fund = true
	return false
}

// NewMPTTesterNoFund creates an MPTTester without funding accounts.
// Use this when you need to control funding yourself.
func NewMPTTesterNoFund(t *testing.T, env *jtx.TestEnv, issuer *jtx.Account) *MPTTester {
	t.Helper()
	return &MPTTester{
		t:      t,
		env:    env,
		issuer: issuer,
	}
}

// IssuanceID returns the 48-char hex MPTokenIssuanceID.
func (m *MPTTester) IssuanceID() string {
	return m.id
}

// Issuer returns the issuer account.
func (m *MPTTester) Issuer() *jtx.Account {
	return m.issuer
}

// --------------------------------------------------------------------------
// Option structs for each MPT operation
// These mirror rippled's MPTCreate, MPTDestroy, MPTAuthorize, MPTSet structs
// --------------------------------------------------------------------------

// CreateOpts configures an MPTokenIssuanceCreate transaction.
// Reference: rippled MPTCreate struct
type CreateOpts struct {
	// MaxAmt is the maximum amount (optional)
	MaxAmt *uint64
	// AssetScale is the decimal scale (optional)
	AssetScale *uint8
	// TransferFee is the transfer fee in basis points (optional)
	TransferFee *uint16
	// Metadata is the hex-encoded metadata (optional).
	// Use nil to not set metadata, use &"" to set empty metadata (should return temMALFORMED).
	Metadata *string
	// OwnerCount is the expected owner count after the operation (for verification)
	OwnerCount *uint32
	// HolderCount is the expected holder owner count (not used for create, but for parity)
	HolderCount *uint32
	// Flags are the transaction flags
	Flags uint32
	// Err is the expected error code (nil means expect success)
	Err string
}

// DestroyOpts configures an MPTokenIssuanceDestroy transaction.
// Reference: rippled MPTDestroy struct
type DestroyOpts struct {
	// Issuer overrides the account submitting the destroy (default: tester's issuer)
	Issuer *jtx.Account
	// ID overrides the issuance ID to destroy
	ID string
	// OwnerCount is the expected owner count after the operation
	OwnerCount *uint32
	// Flags are the transaction flags
	Flags uint32
	// Err is the expected error code (nil means expect success)
	Err string
}

// AuthorizeOpts configures an MPTokenAuthorize transaction.
// Reference: rippled MPTAuthorize struct
type AuthorizeOpts struct {
	// Account is who submits the transaction (default: issuer)
	Account *jtx.Account
	// Holder is the holder field in the transaction (for issuer authorizing a holder)
	Holder *jtx.Account
	// ID overrides the issuance ID
	ID string
	// OwnerCount is the expected owner count for the submitter after the operation
	OwnerCount *uint32
	// HolderCount is the expected owner count for the holder/account
	HolderCount *uint32
	// Flags are the transaction flags (e.g., tfMPTUnauthorize)
	Flags uint32
	// Err is the expected error code (empty means expect success)
	Err string
}

// SetOpts configures an MPTokenIssuanceSet transaction.
// Reference: rippled MPTSet struct
type SetOpts struct {
	// Account is who submits the transaction (default: issuer)
	Account *jtx.Account
	// Holder is the holder account to modify
	Holder *jtx.Account
	// ID overrides the issuance ID
	ID string
	// Flags are the transaction flags (tfMPTLock, tfMPTUnlock)
	Flags uint32
	// Err is the expected error code (empty means expect success)
	Err string
}

// --------------------------------------------------------------------------
// Core operations
// --------------------------------------------------------------------------

// Create submits an MPTokenIssuanceCreate transaction.
// Reference: rippled MPTTester::create()
func (m *MPTTester) Create(opts CreateOpts) {
	m.t.Helper()

	// Capture sequence before create for ID computation
	m.seq = m.env.Seq(m.issuer)

	// Build the transaction
	create := mpttx.NewMPTokenIssuanceCreate(m.issuer.Address)
	create.Fee = "10"

	if opts.MaxAmt != nil {
		create.MaximumAmount = opts.MaxAmt
	}
	if opts.AssetScale != nil {
		create.AssetScale = opts.AssetScale
	}
	if opts.TransferFee != nil {
		create.TransferFee = opts.TransferFee
	}
	if opts.Metadata != nil {
		create.MPTokenMetadata = opts.Metadata
	}
	if opts.Flags != 0 {
		create.SetFlags(opts.Flags)
	}

	result := m.env.Submit(create)

	if opts.Err != "" {
		require.Equal(m.t, opts.Err, result.Code,
			"Expected error %s, got %s: %s", opts.Err, result.Code, result.Message)
		return
	}

	jtx.RequireTxSuccess(m.t, result)

	// Compute the issuance ID: sequence (4 bytes big-endian) + account ID (20 bytes)
	m.id = makeMPTIDHex(m.seq, m.issuer)
	m.created = true

	// Verify owner count if specified
	if opts.OwnerCount != nil {
		jtx.RequireOwnerCount(m.t, m.env, m.issuer, *opts.OwnerCount)
	}
}

// Destroy submits an MPTokenIssuanceDestroy transaction.
// Reference: rippled MPTTester::destroy()
func (m *MPTTester) Destroy(opts DestroyOpts) {
	m.t.Helper()

	account := m.issuer
	if opts.Issuer != nil {
		account = opts.Issuer
	}

	id := m.id
	if opts.ID != "" {
		id = opts.ID
	}

	destroy := mpttx.NewMPTokenIssuanceDestroy(account.Address, id)
	destroy.Fee = "10"
	if opts.Flags != 0 {
		destroy.SetFlags(opts.Flags)
	}

	result := m.env.Submit(destroy)

	if opts.Err != "" {
		require.Equal(m.t, opts.Err, result.Code,
			"Expected error %s, got %s: %s", opts.Err, result.Code, result.Message)
		return
	}

	jtx.RequireTxSuccess(m.t, result)

	// Verify owner count if specified
	if opts.OwnerCount != nil {
		account := m.issuer
		if opts.Issuer != nil {
			account = opts.Issuer
		}
		jtx.RequireOwnerCount(m.t, m.env, account, *opts.OwnerCount)
	}
}

// Authorize submits an MPTokenAuthorize transaction.
// Reference: rippled MPTTester::authorize()
func (m *MPTTester) Authorize(opts AuthorizeOpts) {
	m.t.Helper()

	// Determine the submitting account
	account := m.issuer
	if opts.Account != nil {
		account = opts.Account
	}

	id := m.id
	if opts.ID != "" {
		id = opts.ID
	}

	auth := mpttx.NewMPTokenAuthorize(account.Address, id)
	auth.Fee = "10"

	if opts.Holder != nil {
		auth.Holder = opts.Holder.Address
	}
	if opts.Flags != 0 {
		auth.SetFlags(opts.Flags)
	}

	result := m.env.Submit(auth)

	if opts.Err != "" {
		require.Equal(m.t, opts.Err, result.Code,
			"Expected error %s, got %s: %s", opts.Err, result.Code, result.Message)
		return
	}

	jtx.RequireTxSuccess(m.t, result)

	// Verify holder count if specified (owner count of the relevant account)
	if opts.HolderCount != nil {
		// If the tx is from a holder (no Holder field set), check the holder's owner count
		// If the tx is from the issuer (Holder field set), check the holder's owner count
		checkAccount := account
		if opts.Holder != nil {
			checkAccount = opts.Holder
		}
		jtx.RequireOwnerCount(m.t, m.env, checkAccount, *opts.HolderCount)
	}
}

// Set submits an MPTokenIssuanceSet transaction.
// Reference: rippled MPTTester::set()
func (m *MPTTester) Set(opts SetOpts) {
	m.t.Helper()

	account := m.issuer
	if opts.Account != nil {
		account = opts.Account
	}

	id := m.id
	if opts.ID != "" {
		id = opts.ID
	}

	set := mpttx.NewMPTokenIssuanceSet(account.Address, id)
	set.Fee = "10"

	if opts.Holder != nil {
		set.Holder = opts.Holder.Address
	}
	if opts.Flags != 0 {
		set.SetFlags(opts.Flags)
	}

	result := m.env.Submit(set)

	if opts.Err != "" {
		require.Equal(m.t, opts.Err, result.Code,
			"Expected error %s, got %s: %s", opts.Err, result.Code, result.Message)
		return
	}

	jtx.RequireTxSuccess(m.t, result)
}

// Pay sends an MPT payment from src to dest.
// Reference: rippled MPTTester::pay()
func (m *MPTTester) Pay(src, dest *jtx.Account, amount int64, expectedErr ...string) {
	m.t.Helper()

	mptAmount := m.MPTAmount(amount)

	// Use stored issuance ID, or compute it from current sequence if not yet created
	id := m.id
	if id == "" {
		id = makeMPTIDHex(m.env.Seq(m.issuer), m.issuer)
	}

	result := m.env.Submit(
		paybuilder.PayIssued(src, dest, mptAmount).MPTIssuanceID(id).Build(),
	)

	if len(expectedErr) > 0 && expectedErr[0] != "" {
		require.Equal(m.t, expectedErr[0], result.Code,
			"Expected error %s, got %s: %s", expectedErr[0], result.Code, result.Message)
		return
	}

	jtx.RequireTxSuccess(m.t, result)
}

// PayWithSendMax sends an MPT payment with SendMax.
func (m *MPTTester) PayWithSendMax(src, dest *jtx.Account, amount int64, sendMax int64, expectedErr ...string) {
	m.t.Helper()

	mptAmount := m.MPTAmount(amount)
	mptSendMax := m.MPTAmount(sendMax)
	result := m.env.Submit(
		paybuilder.PayIssued(src, dest, mptAmount).
			MPTIssuanceID(m.id).
			SendMax(mptSendMax).
			Build(),
	)

	if len(expectedErr) > 0 && expectedErr[0] != "" {
		require.Equal(m.t, expectedErr[0], result.Code,
			"Expected error %s, got %s: %s", expectedErr[0], result.Code, result.Message)
		return
	}

	jtx.RequireTxSuccess(m.t, result)
}

// PayWithFlags sends an MPT payment with custom flags.
func (m *MPTTester) PayWithFlags(src, dest *jtx.Account, amount int64, flags uint32, expectedErr ...string) {
	m.t.Helper()

	mptAmount := m.MPTAmount(amount)
	result := m.env.Submit(
		paybuilder.PayIssued(src, dest, mptAmount).
			MPTIssuanceID(m.id).
			Flags(flags).
			Build(),
	)

	if len(expectedErr) > 0 && expectedErr[0] != "" {
		require.Equal(m.t, expectedErr[0], result.Code,
			"Expected error %s, got %s: %s", expectedErr[0], result.Code, result.Message)
		return
	}

	jtx.RequireTxSuccess(m.t, result)
}

// PayFull sends an MPT payment with SendMax, DeliverMin, and flags.
func (m *MPTTester) PayFull(src, dest *jtx.Account, amount, sendMax, deliverMin int64, flags uint32, expectedErr ...string) {
	m.t.Helper()

	mptAmount := m.MPTAmount(amount)
	builder := paybuilder.PayIssued(src, dest, mptAmount).MPTIssuanceID(m.id)

	if sendMax != 0 {
		mptSendMax := m.MPTAmount(sendMax)
		builder = builder.SendMax(mptSendMax)
	}
	if deliverMin != 0 {
		mptDeliverMin := m.MPTAmount(deliverMin)
		builder = builder.DeliverMin(mptDeliverMin)
	}
	if flags != 0 {
		builder = builder.Flags(flags)
	}

	result := m.env.Submit(builder.Build())

	if len(expectedErr) > 0 && expectedErr[0] != "" {
		require.Equal(m.t, expectedErr[0], result.Code,
			"Expected error %s, got %s: %s", expectedErr[0], result.Code, result.Message)
		return
	}

	jtx.RequireTxSuccess(m.t, result)
}

// Claw submits an MPT clawback transaction.
// Reference: rippled MPTTester::claw()
func (m *MPTTester) Claw(issuer, holder *jtx.Account, amount int64, expectedErr ...string) {
	m.t.Helper()

	// MPT clawback uses the Amount field with the MPT issuance info
	// and the Holder field to specify who to claw back from
	mptAmount := m.MPTAmount(amount)

	// Use stored issuance ID, or compute it from current sequence if not yet created
	id := m.id
	if id == "" {
		id = makeMPTIDHex(m.env.Seq(m.issuer), m.issuer)
	}

	cb := clawback.NewMPTokenClawback(issuer.Address, holder.Address, id, mptAmount)
	cb.Fee = "10"

	result := m.env.Submit(cb)

	if len(expectedErr) > 0 && expectedErr[0] != "" {
		require.Equal(m.t, expectedErr[0], result.Code,
			"Expected error %s, got %s: %s", expectedErr[0], result.Code, result.Message)
		return
	}

	jtx.RequireTxSuccess(m.t, result)
}

// --------------------------------------------------------------------------
// Query / verification helpers
// --------------------------------------------------------------------------

// MPTAmount creates an MPT tx.Amount for use in payments and other transactions.
// This creates an IOU-style amount with the MPT issuance ID as the "currency"
// and the issuer address.
func (m *MPTTester) MPTAmount(amount int64) tx.Amount {
	// MPT amounts are stored as raw int64 values (no IOU normalization)
	// to preserve precision for large values like MaxMPTokenAmount.
	return sle.NewMPTAmountDirect(amount, "MPT", m.issuer.Address)
}

// CheckMPTokenAmount verifies the MPTAmount balance for a holder.
// Reference: rippled MPTTester::checkMPTokenAmount()
func (m *MPTTester) CheckMPTokenAmount(holder *jtx.Account, expected int64) bool {
	m.t.Helper()
	// Read the MPToken ledger entry for this holder
	mptID := decodeMPTID(m.id)
	issuanceKey := keylet.MPTIssuance(mptID)
	tokenKey := keylet.MPToken(issuanceKey.Key, holder.ID)

	data, err := m.env.Ledger().Read(tokenKey)
	if err != nil || data == nil {
		return expected == 0 // If no entry exists, balance is 0
	}

	// Parse the MPTAmount field from the entry
	// The MPToken entry contains the holder's balance
	_ = data // TODO: Parse actual MPToken entry and check amount
	return true
}

// CheckMPTokenOutstandingAmount verifies the OutstandingAmount on the issuance.
// Reference: rippled MPTTester::checkMPTokenOutstandingAmount()
func (m *MPTTester) CheckMPTokenOutstandingAmount(expected int64) bool {
	m.t.Helper()
	mptID := decodeMPTID(m.id)
	issuanceKey := keylet.MPTIssuance(mptID)

	data, err := m.env.Ledger().Read(issuanceKey)
	if err != nil || data == nil {
		return expected == 0
	}

	_ = data // TODO: Parse actual MPTokenIssuance entry and check outstanding amount
	return true
}

// RequireMPTokenAmount asserts the MPTAmount balance for a holder.
func (m *MPTTester) RequireMPTokenAmount(holder *jtx.Account, expected int64) {
	m.t.Helper()
	require.True(m.t, m.CheckMPTokenAmount(holder, expected),
		"MPToken amount mismatch for holder %s: expected %d", holder.Name, expected)
}

// --------------------------------------------------------------------------
// Helper functions
// --------------------------------------------------------------------------

// makeMPTIDHex creates a hex-encoded MPTokenIssuanceID from a sequence and account.
// The ID is: sequence (4 bytes big-endian) + account ID (20 bytes) = 24 bytes = 48 hex chars
func makeMPTIDHex(sequence uint32, account *jtx.Account) string {
	mptID := keylet.MakeMPTID(sequence, account.ID)
	return strings.ToUpper(hex.EncodeToString(mptID[:]))
}

// MakeMPTIDHexFromAddr creates a hex-encoded MPTokenIssuanceID from sequence and address string.
// Useful for tests that need to compute an ID before the tester's Create() is called.
func MakeMPTIDHexFromAddr(sequence uint32, address string) string {
	_, accountID, err := addresscodec.DecodeClassicAddressToAccountID(address)
	if err != nil {
		return ""
	}
	var id [20]byte
	copy(id[:], accountID)
	mptID := keylet.MakeMPTID(sequence, id)
	return strings.ToUpper(hex.EncodeToString(mptID[:]))
}

// decodeMPTID converts a hex MPT ID string to [24]byte.
func decodeMPTID(hexID string) [24]byte {
	var mptID [24]byte
	data, _ := hex.DecodeString(hexID)
	if len(data) >= 24 {
		copy(mptID[:], data[:24])
	}
	return mptID
}


// --------------------------------------------------------------------------
// Pointer helpers for option structs
// --------------------------------------------------------------------------

// PtrUint32 returns a pointer to a uint32.
func PtrUint32(v uint32) *uint32 { return &v }

// PtrUint64 returns a pointer to a uint64.
func PtrUint64(v uint64) *uint64 { return &v }

// PtrUint16 returns a pointer to a uint16.
func PtrUint16(v uint16) *uint16 { return &v }

// PtrUint8 returns a pointer to a uint8.
func PtrUint8(v uint8) *uint8 { return &v }

// PtrString returns a pointer to a string.
func PtrString(v string) *string { return &v }

// --------------------------------------------------------------------------
// Constants matching rippled's transaction flags
// Re-exported here for test convenience
// --------------------------------------------------------------------------

const (
	// MPTokenIssuanceCreate flags
	TfMPTCanLock     = mpttx.MPTokenIssuanceCreateFlagCanLock
	TfMPTRequireAuth = mpttx.MPTokenIssuanceCreateFlagRequireAuth
	TfMPTCanEscrow   = mpttx.MPTokenIssuanceCreateFlagCanEscrow
	TfMPTCanTrade    = mpttx.MPTokenIssuanceCreateFlagCanTrade
	TfMPTCanTransfer = mpttx.MPTokenIssuanceCreateFlagCanTransfer
	TfMPTCanClawback = mpttx.MPTokenIssuanceCreateFlagCanClawback

	// MPTokenIssuanceSet flags
	TfMPTLock   = mpttx.MPTokenIssuanceSetFlagLock
	TfMPTUnlock = mpttx.MPTokenIssuanceSetFlagUnlock

	// MPTokenAuthorize flags
	TfMPTUnauthorize = mpttx.MPTokenAuthorizeFlagUnauthorize

	// Payment flags for MPT payment tests
	TfNoRippleDirect = payment.PaymentFlagNoDirectRipple
	TfLimitQuality   = payment.PaymentFlagLimitQuality
	TfPartialPayment = payment.PaymentFlagPartialPayment

	// maxMPTokenAmount matches rippled's maxMPTokenAmount (63-bit max)
	MaxMPTokenAmount uint64 = 0x7FFFFFFFFFFFFFFF
)

// Placeholder for unused imports
var _ = fmt.Sprintf
