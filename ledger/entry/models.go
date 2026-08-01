//go:generate go run ./cmd/entrygen .

// Package entry provides typed, per-entry-type representations of XRPL
// ledger entries (Serializable Ledger Entries, "SLE"). Each generated struct
// fully mirrors one ledger-entry type's on-wire field set as defined in
// rippled's ledger_entries.macro / LedgerFormats.cpp.
//
// The package has two responsibilities:
//
//   - Metadata hot path: decode a ledger-entry blob into a fixed-size struct
//     and emit only the fields that should appear in PreviousFields /
//     FinalFields / NewFields. No intermediate map allocation per affected
//     entry. All supported ledger-entry types must be registered because
//     transaction metadata construction depends on this typed path.
//
//   - Typed serialization: every struct also exposes ToMap, Encode, and Hash.
//     Encode round-trips Decode byte-for-byte through binarycodec; Hash
//     returns the canonical SHAMap account-state leaf hash
//     (sha512Half(HashPrefixLeafNode || data || index)). These methods are
//     a typed alternative to hand-built map[string]any serializers.
package entry

import "github.com/LeJamon/go-xrpl/protocol"

// Entry is the runtime abstraction over a typed ledger-entry decoder.
//
// A single Entry instance represents one decoded blob. Decode populates the
// instance from binary data; the Emit* methods write the appropriate field
// subset into the metadata output maps. Pairs of instances (original +
// current) are compared via EmitPreviousFields and EmitFinalFields.
type Entry interface {
	// Type returns the concrete ledger-entry type.
	Type() Type

	// Decode parses binary ledger-entry data into the typed struct. It must
	// reset prior state before decoding.
	Decode(data []byte) error
	decodeLegacy(data []byte) error

	// EmitNewFields writes the fields that should appear in
	// AffectedNode.NewFields for a CreatedNode (sMD_Create | sMD_Always,
	// excluding default values).
	EmitNewFields(out map[string]any)

	// EmitFinalFields writes the fields that should appear in
	// AffectedNode.FinalFields for a ModifiedNode (sMD_Always | sMD_ChangeNew).
	EmitFinalFields(out map[string]any)

	// EmitPreviousFields writes the fields that should appear in
	// AffectedNode.PreviousFields for a ModifiedNode (sMD_ChangeOrig and
	// changed-vs-current). prev must be the same concrete type as the
	// receiver; mismatched or nil values emit no fields.
	EmitPreviousFields(prev Entry, out map[string]any)

	// EmitChangeOrigFields writes the names of every present field carrying
	// sMD_ChangeOrig (MetaDefault) on the receiver. The empty-PreviousFields
	// heuristic in internal/tx/apply_state_table uses this to detect
	// rippled's STI_NOTPRESENT-in-prevs emission without false positives
	// from sMD_Always-only fields (which appear in FinalFields but not in
	// rippled's prevs loop).
	EmitChangeOrigFields(out map[string]any)

	// EmitDeleteFinalFields writes the fields that should appear in
	// AffectedNode.FinalFields for a DeletedNode (sMD_Always | sMD_DeleteFinal).
	EmitDeleteFinalFields(out map[string]any)

	// EmitDeletePreviousFields writes the fields from the original state that
	// changed before deletion (sMD_ChangeOrig, present in both, differing).
	EmitDeletePreviousFields(prev Entry, out map[string]any)

	// PreviousTxn returns the PreviousTxnID (hex) and PreviousTxnLgrSeq
	// threaded onto the AffectedNode itself, drawn from the receiver. Empty
	// id / zero seq means the field is absent.
	PreviousTxn() (id string, seq uint32)
}

// DecodeLegacy decodes a historical go-xrpl ledger entry using the explicit
// compatibility rules declared by the generated schema. Consensus and live
// ledger paths must use Entry.Decode, which enforces rippled's current ledger
// template.
func DecodeLegacy(entry Entry, data []byte) error {
	return entry.decodeLegacy(data)
}

// New returns a fresh typed entry for typ.
func New(typ Type) Entry {
	switch typ {
	case TypeAccountRoot:
		return new(AccountRoot)
	case TypeAmendments:
		return new(Amendments)
	case TypeAMM:
		return new(AMM)
	case TypeBridge:
		return new(Bridge)
	case TypeCheck:
		return new(Check)
	case TypeCredential:
		return new(Credential)
	case TypeDelegate:
		return new(Delegate)
	case TypeDepositPreauth:
		return new(DepositPreauth)
	case TypeDID:
		return new(DID)
	case TypeDirectoryNode:
		return new(DirectoryNode)
	case TypeEscrow:
		return new(Escrow)
	case TypeFeeSettings:
		return new(FeeSettings)
	case TypeLedgerHashes:
		return new(LedgerHashes)
	case TypeLoan:
		return new(Loan)
	case TypeLoanBroker:
		return new(LoanBroker)
	case TypeMPToken:
		return new(MPToken)
	case TypeMPTokenIssuance:
		return new(MPTokenIssuance)
	case TypeNegativeUNL:
		return new(NegativeUNL)
	case TypeNFTokenOffer:
		return new(NFTokenOffer)
	case TypeNFTokenPage:
		return new(NFTokenPage)
	case TypeOffer:
		return new(Offer)
	case TypeOracle:
		return new(Oracle)
	case TypePayChannel:
		return new(PayChannel)
	case TypePermissionedDomain:
		return new(PermissionedDomain)
	case TypeRippleState:
		return new(RippleState)
	case TypeSignerList:
		return new(SignerList)
	case TypeTicket:
		return new(Ticket)
	case TypeVault:
		return new(Vault)
	case TypeXChainOwnedClaimID:
		return new(XChainOwnedClaimID)
	case TypeXChainOwnedCreateAccountClaimID:
		return new(XChainOwnedCreateAccountClaimID)
	default:
		return nil
	}
}

// NewByName resolves a canonical entry name and returns its typed model.
func NewByName(name string) Entry {
	info, ok := protocol.LedgerEntryTypeByName(name)
	if !ok || info.Deprecated {
		return nil
	}
	return New(info.Type)
}

// HasTyped reports whether typ has a generated model.
func HasTyped(typ Type) bool {
	return New(typ) != nil
}

// HasTypedName reports whether name resolves to a generated model.
func HasTypedName(name string) bool {
	return NewByName(name) != nil
}

// ErrUnknownField reports a serialized field that is not declared by an
// entry's schema.
type ErrUnknownField struct {
	EntryType string
	TypeCode  int
	FieldCode int
}

func (e *ErrUnknownField) Error() string {
	return "ledgerfields: " + e.EntryType + ": unknown field (typeCode=" + itoa(e.TypeCode) + ", fieldCode=" + itoa(e.FieldCode) + ")"
}

func newErrUnknownField(entryType string, typeCode, fieldCode int) error {
	return &ErrUnknownField{EntryType: entryType, TypeCode: typeCode, FieldCode: fieldCode}
}

// itoa is a tiny zero-import int→string converter used only by the
// ErrUnknownField message. Avoids dragging strconv into a package that
// otherwise has no use for it.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
