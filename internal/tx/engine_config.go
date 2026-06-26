package tx

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/feetrack"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/protocol"
)

// Validation constants matching rippled
const (
	// MaxMemoSize is the maximum total serialized size of memos (in bytes)
	MaxMemoSize = 1024

	// MaxMemoTypeSize is the maximum size of MemoType field (in bytes)
	MaxMemoTypeSize = 256

	// MaxMemoDataSize is the maximum size of MemoData field (in bytes)
	MaxMemoDataSize = 1024

	// LegacyNetworkIDThreshold is the threshold for legacy network IDs
	// Networks with ID <= this value are legacy networks
	LegacyNetworkIDThreshold = 1024

	// DefaultMaxFee is the maximum legal fee amount matching rippled's INITIAL_XRP.
	// Reference: rippled SystemParameters.h isLegalAmount() — fee <= INITIAL_XRP
	DefaultMaxFee = 100_000_000_000_000_000 // 100 billion XRP in drops

	// QualityOne is the identity transfer rate (1e9). Alias for protocol.QualityOne.
	QualityOne = protocol.QualityOne
)

// ApplyFlags controls transaction application behavior during consensus.
// Reference: rippled ApplyView.h ApplyFlags enum
type ApplyFlags uint32

const (
	TapNONE      ApplyFlags = 0x00
	TapFAIL_HARD ApplyFlags = 0x10  // Local tx with fail_hard flag
	TapRETRY     ApplyFlags = 0x20  // Not the tx's last pass — tec from preclaim is not applied
	TapUNLIMITED ApplyFlags = 0x400 // Privileged source
)

// EngineConfig holds configuration for the transaction engine
type EngineConfig struct {
	// BaseFee is the current base fee in drops
	BaseFee uint64

	// ReserveBase is the base reserve in drops
	ReserveBase uint64

	// ReserveIncrement is the owner reserve increment in drops
	ReserveIncrement uint64

	// LedgerSequence is the current ledger sequence
	LedgerSequence uint32

	// SkipSignatureVerification skips signature checks (for testing/standalone)
	SkipSignatureVerification bool

	// Standalone indicates if running in standalone mode (relaxes some validation)
	Standalone bool

	// NetworkID is the network identifier for this node
	// Networks with ID > 1024 require NetworkID in transactions
	// Networks with ID <= 1024 are legacy networks and cannot have NetworkID in transactions
	NetworkID uint32

	// MaxFee is the maximum allowed fee in drops. When zero, preflight falls
	// back to DefaultMaxFee (100 billion XRP in drops, matching rippled's
	// INITIAL_XRP). Transactions with fees exceeding this are rejected in
	// preflight with temBAD_FEE.
	MaxFee uint64

	// ParentCloseTime is the close time of the parent ledger (in Ripple epoch seconds)
	// This is used for checking offer/escrow expiration
	ParentCloseTime uint32

	// ParentHash is the hash of the parent ledger.
	// Used by pseudoAccountAddress for deterministic AMM account derivation.
	// Reference: rippled View.cpp pseudoAccountAddress uses view.info().parentHash
	ParentHash [32]byte

	// Rules contains the amendment rules for this ledger.
	// If nil, defaults to all amendments enabled (for backwards compatibility).
	Rules *amendment.Rules

	// OpenLedger controls whether fee adequacy is checked.
	// When true, the engine verifies that the transaction fee meets the
	// minimum required fee (including tx-type-specific overrides like
	// AccountDelete's owner reserve). When false, fee adequacy is
	// skipped — only basic fee validity (non-negative, legal amount,
	// sufficient balance) is checked.
	// Reference: rippled Transactor.cpp checkFee — "Only check fee is
	// sufficient when the ledger is open."
	OpenLedger bool

	// ViewOpen mirrors rippled's view.open() for the open-ledger apply path
	// that targets an OpenView yet leaves OpenLedger/EnforceLoadFee unset
	// (the per-tx Submit and held/local replay applies run with tapNONE and
	// fee adequacy disabled). It carries the view-openness signal that
	// internal-failure TER guards consult; it does not affect fee handling.
	// The closed-view consensus build path leaves it false.
	ViewOpen bool

	// ApplyFlags controls transaction application behavior.
	// TapRETRY means this is not the tx's last pass: tec results from
	// preclaim are not applied (likelyToClaimFee = false), allowing the
	// tx to be retried on the next pass.
	// Reference: rippled Transactor.cpp / BuildLedger.cpp
	ApplyFlags ApplyFlags

	// Logger is the logger to use for this engine instance.
	// If nil, xrpllog.Discard() is used — safe for tests and zero-value construction.
	Logger xrpllog.Logger

	// FeeTrack is the node-local LoadFeeTrack snapshot. When set and the
	// ledger is open, checkFee scales the per-tx base fee by the local /
	// cluster / global load factor (scaleFeeLoad) before the fee-adequacy
	// comparison, mirroring rippled's Transactor::minimumFee. When nil,
	// the open-ledger floor is the raw base fee — feetrack.ScaleFeeLoad
	// returns its input unchanged for a nil tracker, so paths that do not
	// plumb it keep their prior behaviour. Consulted when OpenLedger is true,
	// or (for open-ledger applies flagged OpenLedger=false) when EnforceLoadFee
	// is set — rippled gates minimumFee on ctx.view.open().
	// Reference: rippled Transactor.cpp minimumFee → scaleFeeLoad,
	// LoadFeeTrack.cpp:85.
	FeeTrack *feetrack.LoadFeeTrack

	// EnforceLoadFee makes checkFee apply the load-scaled fee floor even when
	// OpenLedger is false, but only while the load factor is elevated above the
	// reference fee. It marks an apply that targets the OPEN ledger yet runs
	// with the base-fee floor disabled (the TxQ direct-apply / clear-queue /
	// accept paths, which rippled invokes with tapNONE). Those paths must still
	// honour rippled's open-ledger floor when server load spikes — view.open()
	// is true there — without re-enabling the base-fee floor that the OpenLedger
	// flag controls (so fee=0 / already-validated txns are unaffected at normal
	// load). Genuinely closed-ledger applies leave this false and never scale.
	EnforceLoadFee bool
}

// GetRules returns the amendment rules for this apply. Rules must be plumbed
// from the parent ledger's Amendments SLE; a nil Rules panics for the same
// reason Engine.rules() does — a silent AllSupportedRules fallback treats every
// amendment as enabled regardless of on-chain state, desyncing the engine from
// the ledger (the #401/#418 wedge). This is the single Rules fallback policy:
// Engine.rules() and ApplyContext.Rules() route through the same no-fallback
// rule. Tests must set Rules explicitly (amendment.AllSupportedRules() or
// EmptyRules()).
func (c EngineConfig) GetRules() *amendment.Rules {
	if c.Rules == nil {
		panic("tx.EngineConfig: Rules is nil — every apply path must plumb " +
			"amendment.Rules from the parent ledger's Amendments SLE. Tests " +
			"should set Rules: amendment.AllSupportedRules() or EmptyRules() " +
			"explicitly.")
	}
	return c.Rules
}

// IsViewOpen reports whether this apply targets the open ledger, mirroring
// rippled's view.open(). It is true on the direct open-ledger submission path
// (OpenLedger), on the TxQ apply/accept paths that run with OpenLedger=false
// yet are marked by EnforceLoadFee, and on the per-tx Submit / held-tx replay
// applies marked by ViewOpen. It is false only on the closed-view consensus
// build path. Internal-failure TER guards consult it to pick the
// telFAILED_PROCESSING (open) vs tecFAILED_PROCESSING (closed) variant.
func (c EngineConfig) IsViewOpen() bool {
	return c.OpenLedger || c.EnforceLoadFee || c.ViewOpen
}

// LedgerView provides read/write access to ledger state
type LedgerView interface {
	// Read reads a ledger entry
	Read(k keylet.Keylet) ([]byte, error)

	// Exists checks if an entry exists
	Exists(k keylet.Keylet) (bool, error)

	// Insert adds a new entry
	Insert(k keylet.Keylet, data []byte) error

	// Update modifies an existing entry
	Update(k keylet.Keylet, data []byte) error

	// Erase removes an entry
	Erase(k keylet.Keylet) error

	// AdjustDropsDestroyed records destroyed XRP
	AdjustDropsDestroyed(drops drops.XRPAmount)

	// ForEach iterates over all state entries
	// If fn returns false, iteration stops early
	ForEach(fn func(key [32]byte, data []byte) bool) error

	// Succ returns the first entry with key > the given key.
	// Returns (key, data, true, nil) if found, or ([32]byte{}, nil, false, nil) if not.
	// Reference: rippled ReadView::succ() used for efficient ordered traversal.
	Succ(key [32]byte) ([32]byte, []byte, bool, error)

	// TxExists returns true if a transaction with the given hash has already been
	// applied to the current open ledger. Used by invariant checkers and duplicate
	// transaction detection.
	// Reference: rippled ReadView::txExists()
	TxExists(txID [32]byte) bool

	// Rules returns the amendment rules for this view.
	// Returns nil if rules are not available.
	Rules() *amendment.Rules

	// LedgerSeq returns the building ledger's sequence number.
	// Reference: rippled ReadView::seq().
	LedgerSeq() uint32
}

// ComputeTransactionHash computes the hash of a transaction.
// The hash is SHA512Half of the "TXN\x00" prefix + serialized transaction.
func ComputeTransactionHash(tx Transaction) ([32]byte, error) {
	return computeTransactionHash(tx)
}

// computeTransactionHash computes the hash of a transaction
// The hash is SHA512Half of the "TXN\x00" prefix + serialized transaction
func computeTransactionHash(tx Transaction) ([32]byte, error) {
	var hash [32]byte
	var txBytes []byte

	// Use raw bytes if available (from parsing), otherwise re-serialize
	if rawBytes := tx.GetRawBytes(); len(rawBytes) > 0 {
		txBytes = rawBytes
	} else {
		// Serialize the transaction using Flatten
		txMap, err := tx.Flatten()
		if err != nil {
			return hash, err
		}

		// Encode to binary using the binary codec
		hexStr, err := binarycodec.Encode(txMap)
		if err != nil {
			return hash, err
		}

		txBytes, err = hex.DecodeString(hexStr)
		if err != nil {
			return hash, err
		}
	}

	// Prefix is "TXN\x00" = 0x54584E00
	prefix := []byte{0x54, 0x58, 0x4E, 0x00} //nolint:prealloc // prealloc: static 4-byte composite literal followed by a single append
	data := append(prefix, txBytes...)

	hash = common.Sha512Half(data)
	return hash, nil
}
