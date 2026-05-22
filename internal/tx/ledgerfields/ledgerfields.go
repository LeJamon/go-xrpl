//go:generate go run ./cmd/ledgerfieldsgen .

// Package ledgerfields provides typed, per-entry-type representations of XRPL
// ledger entries (Serializable Ledger Entries, "SLE"). Each generated struct
// fully mirrors one ledger-entry type's on-wire field set as defined in
// rippled's ledger_entries.macro / LedgerFormats.cpp.
//
// Two responsibilities:
//
//   - Metadata hot path: decode a ledger-entry blob into a fixed-size struct
//     and emit only the fields that should appear in PreviousFields /
//     FinalFields / NewFields. No intermediate map allocation per affected
//     entry. Used by internal/tx/apply_state_table.
//
//   - Typed serialization: every struct also exposes ToMap, Encode, and Hash.
//     Encode round-trips Decode byte-for-byte through binarycodec; Hash
//     returns the canonical SHAMap account-state leaf hash
//     (sha512Half(HashPrefixLeafNode || data || index)). These methods are
//     a typed alternative to the hand-built map[string]any pattern across
//     internal/ledger/state/*.go; migrating those callsites is a follow-up.
//
// Entry types not yet covered by a typed implementation fall through to the
// generic map-based path; coverage can be extended type-by-type without
// touching the apply_state_table wiring.
package ledgerfields

// Entry is the runtime abstraction over a typed ledger-entry decoder.
//
// A single Entry instance represents one decoded blob. Decode populates the
// instance from binary data; the Emit* methods write the appropriate field
// subset into the metadata output maps. Pairs of instances (original +
// current) are compared via EmitPreviousFields and EmitFinalFields.
type Entry interface {
	// Decode parses binary ledger-entry data into the typed struct. It must
	// reset prior state before decoding.
	Decode(data []byte) error

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
	// receiver; mismatched types are treated as "all fields changed".
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

type constructor func() Entry

var registry = map[string]constructor{}

// Register binds an entry-type name (e.g. "AccountRoot") to a constructor.
// Called from generated init() functions.
func Register(entryType string, c constructor) {
	registry[entryType] = c
}

// New returns a fresh Entry for the given ledger-entry-type name, or nil if
// no typed implementation is registered. apply_state_table falls back to the
// generic map-based path on nil.
func New(entryType string) Entry {
	if disabled {
		return nil
	}
	if c, ok := registry[entryType]; ok {
		return c()
	}
	return nil
}

// disabled is a debug toggle used by benchmarks to compare typed vs generic
// paths. Never set in production code.
var disabled = false

// SetDisabledForBenchmarks toggles the typed path off for A/B benchmarking.
// Returns the previous value so the caller can restore it.
func SetDisabledForBenchmarks(d bool) bool {
	prev := disabled
	disabled = d
	return prev
}

// HasTyped reports whether a typed implementation exists for entryType.
func HasTyped(entryType string) bool {
	_, ok := registry[entryType]
	return ok
}

// ErrUnknownField is returned by a generated Decode method when the binary
// blob carries a field whose (typeCode, fieldCode) pair isn't declared in
// the entry's spec. Per the issue's correctness contract — if the wire
// format diverges from what we expect, refuse to produce metadata rather
// than silently dropping the field. apply_state_table propagates the
// error; the surrounding transaction apply will fail loudly.
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
