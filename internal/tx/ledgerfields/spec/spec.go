// Package spec is the declarative source of truth for the per-entry-type
// typed metadata decoders generated under internal/tx/ledgerfields. Each
// Entry lists the fields that appear on its ledger blob (the same set that
// rippled's ledger_entries.macro carries) along with the metadata behavior
// for fields that diverge from the sMD_Default rule. cmd/ledgerfieldsgen
// reads this list, looks up each field's XRPL type and ordinal from
// codec/binarycodec/definitions, and emits one Go file per entry type.
package spec

// Meta classifies a field's contribution to metadata. The values match the
// rippled sMD_* flag table at include/xrpl/protocol/detail/sfields.macro
// after collapsing flag combinations into the four cases we actually need.
type Meta uint8

const (
	// MetaDefault means the field participates in PreviousFields when it
	// changes, FinalFields on modify, NewFields on create, and FinalFields
	// on delete. This is the rule for the overwhelming majority of fields.
	MetaDefault Meta = iota

	// MetaAlways means the field is always emitted in FinalFields on
	// modify and NewFields on create, regardless of value or whether it
	// changed. Used for RootIndex.
	MetaAlways

	// MetaDeleteFinal means the field only appears in FinalFields when the
	// entry is deleted. Used for PreviousTxnID / PreviousTxnLgrSeq, which
	// are threaded by ApplyStateTable and must not leak into modify
	// metadata.
	MetaDeleteFinal

	// MetaNever means the field never appears in metadata. Used for
	// LedgerEntryType (decoded only to skip the header) and Indexes
	// (DirectoryNode page contents; rippled excludes them for size).
	MetaNever
)

// Field describes one entry on a typed ledger-entry struct.
type Field struct {
	// Name is the canonical XRPL field name. It must match a FIELDS entry
	// in codec/binarycodec/definitions/definitions.json so the generator
	// can resolve the field's XRPL type and ordinal.
	Name string

	// Meta is the per-field metadata behavior. Zero value (MetaDefault)
	// covers most fields.
	Meta Meta
}

// Entry describes one ledger-entry type's typed metadata layout.
type Entry struct {
	// Name is the canonical XRPL ledger-entry-type name, e.g. "AccountRoot".
	Name string

	// Fields lists every field carried by this entry type, in any order.
	// The generator orders them by ordinal in the emitted Decode switch.
	Fields []Field
}

// Specs is the full set of entry types covered by typed decoders. Order
// here drives the order of the emitted files (one .go per entry).
var Specs = []Entry{
	{
		Name: "AccountRoot",
		Fields: []Field{
			{Name: "Account"},
			{Name: "Balance"},
			{Name: "Sequence"},
			{Name: "OwnerCount"},
			{Name: "Flags"},
			{Name: "RegularKey"},
			{Name: "Domain"},
			{Name: "EmailHash"},
			{Name: "MessageKey"},
			{Name: "TransferRate"},
			{Name: "TickSize"},
			{Name: "NFTokenMinter"},
			{Name: "MintedNFTokens"},
			{Name: "BurnedNFTokens"},
			{Name: "FirstNFTokenSequence"},
			{Name: "AccountTxnID"},
			{Name: "WalletLocator"},
			{Name: "TicketCount"},
			{Name: "AMMID"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "Offer",
		Fields: []Field{
			{Name: "Account"},
			{Name: "Sequence"},
			{Name: "TakerPays"},
			{Name: "TakerGets"},
			{Name: "BookDirectory"},
			{Name: "BookNode"},
			{Name: "OwnerNode"},
			{Name: "Expiration"},
			{Name: "Flags"},
			{Name: "DomainID"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "DirectoryNode",
		Fields: []Field{
			{Name: "Flags"},
			{Name: "RootIndex", Meta: MetaAlways},
			{Name: "Indexes", Meta: MetaNever},
			{Name: "IndexNext"},
			{Name: "IndexPrevious"},
			{Name: "Owner"},
			{Name: "TakerPaysCurrency"},
			{Name: "TakerPaysIssuer"},
			{Name: "TakerGetsCurrency"},
			{Name: "TakerGetsIssuer"},
			{Name: "ExchangeRate"},
			{Name: "NFTokenID"},
			{Name: "DomainID"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "RippleState",
		Fields: []Field{
			{Name: "Flags"},
			{Name: "Balance"},
			{Name: "LowLimit"},
			{Name: "HighLimit"},
			{Name: "LowNode"},
			{Name: "HighNode"},
			{Name: "LowQualityIn"},
			{Name: "LowQualityOut"},
			{Name: "HighQualityIn"},
			{Name: "HighQualityOut"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
}
