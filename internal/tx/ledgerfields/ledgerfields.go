//go:generate go run ./cmd/ledgerfieldsgen .

// Package ledgerfields provides typed, per-entry-type representations of XRPL
// ledger entries used on the metadata-construction hot path.
//
// The generic path in internal/tx/apply_state_table.go decodes every ledger
// entry blob into a fresh map[string]any (twice, once for the original and
// once for the current state of a modified entry) and then iterates that map
// to build PreviousFields and FinalFields. That allocates one map header plus
// boxed values for every field of every affected entry on every transaction.
//
// The typed path defined here decodes the binary blob directly into a
// fixed-size, per-type struct and emits only the fields that should appear in
// the metadata maps. No intermediate decode map is allocated, no per-field
// `any` boxing happens at decode time, and field-equality checks turn into
// typed comparisons instead of fmt.Sprintf("%v", ...) string allocations.
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

// constructor is the per-entry-type factory registered at init time.
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

// HasTyped reports whether a typed implementation exists for entryType. Used
// to gate the typed branch in apply_state_table without allocating an Entry.
func HasTyped(entryType string) bool {
	_, ok := registry[entryType]
	return ok
}
