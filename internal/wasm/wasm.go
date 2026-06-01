// Package wasm executes WebAssembly smart-contract bytecode for the XRPL
// SmartEscrow feature, matching rippled's wasmi-based engine.
//
// Consensus parity requires the exact wasmi engine rippled uses: the
// per-instruction fuel model is consensus-critical. The engine links libwasmi
// (the wasmi/1.0.9 Conan package, fetched from the XRPLF remote) via cgo; see
// internal/wasm/wasmi/build.sh. The cgo engine is built only with the `wasmi`
// build tag; otherwise Run is a stub reporting ErrCGODisabled.
//
// Host functions are exposed to contracts through the HostFunctions interface.
// The engine is deliberately XRPL-agnostic: interface methods exchange raw
// bytes and integers, and the ledger-backed implementation lives in the host
// subpackage. The engine marshals each call's arguments to and from the
// contract's linear memory.
package wasm

import "errors"

// ErrCGODisabled is returned by the stub engine, which is built unless both cgo
// is enabled and the `wasmi` build tag is set. WASM execution requires the
// native wasmi library.
var ErrCGODisabled = errors.New("wasm: execution unavailable (build with cgo and -tags wasmi)")

// ErrExecution is returned when WASM execution fails (compile, instantiate,
// trap, or out of gas). It mirrors rippled mapping every such failure to
// tecFAILED_PROCESSING.
var ErrExecution = errors.New("wasm: execution failed")

// Result is the outcome of a successful WASM run: the i32 the entry function
// returned, and the gas (fuel) it consumed.
type Result struct {
	Result int32
	Cost   int64
}

// GasUnlimited, passed as the gas limit, runs with the maximum fuel budget.
// It mirrors rippled's gasLimit == -1 sentinel.
const GasUnlimited int64 = -1

// HostFunctionError mirrors rippled's HostFunctions::HostFunctionError. A host
// function returns one of these (as a negative i32) to the contract on failure.
type HostFunctionError int32

const (
	HfSuccess             HostFunctionError = 0
	HfInternal            HostFunctionError = -1
	HfFieldNotFound       HostFunctionError = -2
	HfBufferTooSmall      HostFunctionError = -3
	HfNoArray             HostFunctionError = -4
	HfNotLeafField        HostFunctionError = -5
	HfLocatorMalformed    HostFunctionError = -6
	HfSlotOutRange        HostFunctionError = -7
	HfSlotsFull           HostFunctionError = -8
	HfEmptySlot           HostFunctionError = -9
	HfLedgerObjNotFound   HostFunctionError = -10
	HfDecoding            HostFunctionError = -11
	HfDataFieldTooLarge   HostFunctionError = -12
	HfPointerOutOfBounds  HostFunctionError = -13
	HfNoMemExported       HostFunctionError = -14
	HfInvalidParams       HostFunctionError = -15
	HfInvalidAccount      HostFunctionError = -16
	HfInvalidField        HostFunctionError = -17
	HfIndexOutOfBounds    HostFunctionError = -18
	HfFloatInputMalformed HostFunctionError = -19
	HfFloatComputeError   HostFunctionError = -20
	HfNoRuntime           HostFunctionError = -21
	HfOutOfGas            HostFunctionError = -22
)

// HostFunctions is the set of operations a contract's execution context exposes
// to WASM imports. It mirrors rippled's HostFunctions virtual interface and
// grows as more host functions are ported. Byte-slice arguments are read from
// (and results written back to) the contract's linear memory by the engine;
// implementations work in plain Go types.
//
// A method returns its value alongside HfSuccess, or a zero value alongside a
// negative HostFunctionError. The error is surfaced to the contract as the
// import's i32 return.
type HostFunctions interface {
	// GetLedgerSqn returns the sequence of the ledger being built.
	GetLedgerSqn() (uint32, HostFunctionError)

	// Keylets derive the 32-byte ledger index of an object. account and other
	// AccountID arguments are 20 bytes; currency is 20 bytes; mptid is 24 bytes;
	// asset arguments are a currency (20 bytes) optionally followed by an issuer
	// (20 bytes).
	AccountKeylet(account []byte) ([]byte, HostFunctionError)
	AMMKeylet(asset1, asset2 []byte) ([]byte, HostFunctionError)
	CheckKeylet(account []byte, seq uint32) ([]byte, HostFunctionError)
	CredentialKeylet(subject, issuer, credentialType []byte) ([]byte, HostFunctionError)
	DelegateKeylet(account, authorize []byte) ([]byte, HostFunctionError)
	DepositPreauthKeylet(account, authorize []byte) ([]byte, HostFunctionError)
	DIDKeylet(account []byte) ([]byte, HostFunctionError)
	EscrowKeylet(account []byte, seq uint32) ([]byte, HostFunctionError)
	LineKeylet(account1, account2, currency []byte) ([]byte, HostFunctionError)
	MPTIssuanceKeylet(issuer []byte, seq uint32) ([]byte, HostFunctionError)
	MPTokenKeylet(mptid, holder []byte) ([]byte, HostFunctionError)
	NFTOfferKeylet(account []byte, seq uint32) ([]byte, HostFunctionError)
	OfferKeylet(account []byte, seq uint32) ([]byte, HostFunctionError)
	OracleKeylet(account []byte, documentID uint32) ([]byte, HostFunctionError)
	PaychanKeylet(account, destination []byte, seq uint32) ([]byte, HostFunctionError)
	PermissionedDomainKeylet(account []byte, seq uint32) ([]byte, HostFunctionError)
	SignersKeylet(account []byte) ([]byte, HostFunctionError)
	TicketKeylet(account []byte, ticketSeq uint32) ([]byte, HostFunctionError)
	VaultKeylet(account []byte, seq uint32) ([]byte, HostFunctionError)

	// Ledger-header queries.
	GetParentLedgerTime() (uint32, HostFunctionError)
	GetParentLedgerHash() ([]byte, HostFunctionError)
	GetBaseFee() (uint32, HostFunctionError)
	// IsAmendmentEnabled reports whether an amendment is enabled. data is either
	// a 32-byte amendment id or an amendment name.
	IsAmendmentEnabled(data []byte) (int32, HostFunctionError)

	// Crypto and contract data.
	ComputeSha512Half(data []byte) ([]byte, HostFunctionError)
	CheckSignature(message, signature, pubkey []byte) (int32, HostFunctionError)
	// UpdateData stores the escrow's mutable data, returning the byte count.
	UpdateData(data []byte) (int32, HostFunctionError)

	// Tracing logs to the journal and has no ledger effect; each returns 0.
	Trace(msg, data []byte, asHex bool) (int32, HostFunctionError)
	TraceNum(msg []byte, num int64) (int32, HostFunctionError)
	TraceAccount(msg, account []byte) (int32, HostFunctionError)
	TraceFloat(msg, value []byte) (int32, HostFunctionError)
	TraceAmount(msg, amount []byte) (int32, HostFunctionError)

	// NFT field accessors decode a 32-byte NFTokenID.
	GetNFTFlags(nftID []byte) (int32, HostFunctionError)
	GetNFTTransferFee(nftID []byte) (int32, HostFunctionError)
	GetNFTTaxon(nftID []byte) (uint32, HostFunctionError)
	GetNFTSerial(nftID []byte) (uint32, HostFunctionError)
	GetNFTIssuer(nftID []byte) ([]byte, HostFunctionError)
	// GetNFT returns the URI of an NFToken owned by account.
	GetNFT(account, nftID []byte) ([]byte, HostFunctionError)

	// Field getters. A field is identified by code = (typeCode<<16)|fieldCode.
	// cacheIdx is a 1-based slot filled by CacheLedgerObj.
	GetTxField(code int32) ([]byte, HostFunctionError)
	GetCurrentLedgerObjField(code int32) ([]byte, HostFunctionError)
	GetLedgerObjField(cacheIdx, code int32) ([]byte, HostFunctionError)
	GetTxArrayLen(code int32) (int32, HostFunctionError)
	GetCurrentLedgerObjArrayLen(code int32) (int32, HostFunctionError)
	GetLedgerObjArrayLen(cacheIdx, code int32) (int32, HostFunctionError)
	CacheLedgerObj(objID []byte, cacheIdx int32) (int32, HostFunctionError)
}

// paramKind enumerates the WASM value types an entry-function parameter carries.
type paramKind int

const (
	kindI32 paramKind = iota
	kindI64
)

// Param is an input passed to the entry function. The escrow fixtures use only
// integer parameters; byte parameters reach contracts through linear memory.
type Param struct {
	kind paramKind
	i32  int32
	i64  int64
}

// I32 builds an i32 entry-function parameter.
func I32(v int32) Param { return Param{kind: kindI32, i32: v} }

// I64 builds an i64 entry-function parameter.
func I64(v int64) Param { return Param{kind: kindI64, i64: v} }
