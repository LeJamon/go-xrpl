// Package host implements the WASM HostFunctions interface over a ledger view,
// for the SmartEscrow feature. It is plain Go (no cgo): the engine marshals
// arguments to and from contract linear memory and calls these methods with
// byte slices and integers. Most methods are thin adapters over goXRPL's
// keylet, crypto, and codec packages.
package host

import (
	"github.com/LeJamon/go-xrpl/internal/wasm"
	"github.com/LeJamon/go-xrpl/keylet"
)

// View is the read-only ledger context a contract's host functions operate
// over. It grows as more host functions are ported; the keylet derivations need
// nothing from it.
type View interface {
	// LedgerSeq is the sequence of the ledger being built.
	LedgerSeq() uint32
	// ParentCloseTime is the close time of the parent ledger, in seconds since
	// the Ripple epoch.
	ParentCloseTime() uint32
	// ParentHash is the parent ledger's hash.
	ParentHash() [32]byte
	// BaseFee is the network base fee in drops.
	BaseFee() uint32
	// AmendmentEnabled reports whether the amendment with the given id is active.
	AmendmentEnabled(id [32]byte) bool
	// TxBytes is the serialized transaction being executed.
	TxBytes() []byte
	// CurrentObjBytes is the serialized ledger object the contract runs against
	// (for escrow, the escrow being finished).
	CurrentObjBytes() []byte
	// ReadSLE returns the serialized ledger entry at the given index, if present.
	ReadSLE(index [32]byte) ([]byte, bool)
	// FindNFTURI returns the URI of the NFToken owned by account, if found.
	FindNFTURI(account [20]byte, nftID [32]byte) ([]byte, bool)
}

// maxCache is the number of ledger-object cache slots, matching rippled.
const maxCache = 256

// Env implements wasm.HostFunctions for an escrow finish execution against a
// ledger view.
type Env struct {
	view  View
	data  []byte
	cache [maxCache][]byte
}

// New builds a host environment over the given ledger view.
func New(view View) *Env { return &Env{view: view} }

// Data returns the bytes a contract set with update_data, or nil.
func (e *Env) Data() []byte { return e.data }

// GetLedgerSqn returns the sequence of the ledger being built.
func (e *Env) GetLedgerSqn() (uint32, wasm.HostFunctionError) {
	if e.view == nil {
		return 0, wasm.HfNoRuntime
	}
	return e.view.LedgerSeq(), wasm.HfSuccess
}

// account converts a 20-byte slice to an AccountID, rejecting wrong lengths.
func account(b []byte) ([20]byte, wasm.HostFunctionError) {
	var a [20]byte
	if len(b) != 20 {
		return a, wasm.HfInvalidAccount
	}
	copy(a[:], b)
	return a, wasm.HfSuccess
}

// keyBytes copies a keylet's 32-byte ledger index out as a fresh slice.
func keyBytes(k keylet.Keylet) []byte {
	b := make([]byte, 32)
	copy(b, k.Key[:])
	return b
}
