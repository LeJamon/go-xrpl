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

// Style is rippled's SOEStyle for a field in a ledger-entry SOTemplate.
// Writers use it to distinguish required and optional present-zero values from
// soeDEFAULT values, which must not be serialized when they hold their type
// default.
type Style uint8

const (
	StyleUnset Style = iota
	StyleRequired
	StyleOptional
	StyleDefault
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

	// Style controls serialization presence and fresh-entry validation. Every
	// field is annotated explicitly from rippled's 3.2.0 ledger template.
	Style Style

	// DeferredRequired marks a required threading field whose default is
	// materialized by Encode before ApplyStateTable replaces it.
	DeferredRequired bool

	// DecodeOnly marks a field that the decoder must tolerate on incoming
	// blobs but that is never emitted under this name. It exists to read legacy
	// blobs that an earlier go-xrpl release wrote with a non-canonical field
	// rippled's template omits. DecodeAlias may preserve its value in a
	// canonical field; otherwise the decoder consumes and discards it.
	DecodeOnly bool

	// DecodeAlias names the canonical field that receives this legacy field's
	// decoded value. It is valid only on DecodeOnly fields whose XRPL type
	// matches the target. Encoding continues to emit only the canonical field.
	DecodeAlias string
}

// Entry describes one ledger-entry type's typed metadata layout.
type Entry struct {
	// Name is the canonical XRPL ledger-entry-type name, e.g. "AccountRoot".
	Name string

	// AllowBadCurrencyDecode preserves legacy RippleState blobs whose issued
	// amounts carry rippled's badCurrency sentinel. It applies only to binary
	// decode; fresh writers continue through the strict amount encoder.
	AllowBadCurrencyDecode bool

	// Fields lists every field carried by this entry type, in any order.
	// The generator orders them by ordinal in the emitted Decode switch.
	Fields []Field
}

// commonFields are serialized fields admitted by every ledger-entry template
// in LedgerFormats::getCommonFields. LedgerEntryType is injected directly by
// the generator, LedgerIndex is not part of stored SLE blobs, and Flags remains
// explicit in each entry spec because its required/default metadata behavior is
// already modeled there.
var commonFields = []Field{
	{Name: "Sponsor", Style: StyleOptional},
}

// AllFields returns the entry-specific fields plus the common serialized
// fields. Keeping the merge here ensures every generated decoder preserves a
// reserve sponsor rather than accepting it only on a hand-picked set of SLEs.
func (e Entry) AllFields() []Field {
	fields := make([]Field, 0, len(e.Fields)+len(commonFields))
	fields = append(fields, e.Fields...)

	present := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		present[field.Name] = struct{}{}
	}
	for _, field := range commonFields {
		if _, ok := present[field.Name]; ok {
			continue
		}
		fields = append(fields, field)
	}
	return fields
}

// Specs is the full set of entry types covered by typed decoders. Order
// here drives the order of the emitted files (one .go per entry).
var Specs = []Entry{
	{
		Name: "AccountRoot",
		Fields: []Field{
			{Name: "Account", Style: StyleRequired},
			{Name: "Balance", Style: StyleRequired},
			{Name: "Sequence", Style: StyleRequired},
			{Name: "OwnerCount", Style: StyleRequired},
			{Name: "SponsoredOwnerCount", Style: StyleDefault},
			{Name: "SponsoringOwnerCount", Style: StyleDefault},
			{Name: "SponsoringAccountCount", Style: StyleDefault},
			{Name: "Flags", Style: StyleRequired},
			{Name: "RegularKey", Style: StyleOptional},
			{Name: "Domain", Style: StyleOptional},
			{Name: "EmailHash", Style: StyleOptional},
			{Name: "MessageKey", Style: StyleOptional},
			{Name: "TransferRate", Style: StyleOptional},
			{Name: "TickSize", Style: StyleOptional},
			{Name: "NFTokenMinter", Style: StyleOptional},
			{Name: "MintedNFTokens", Style: StyleDefault},
			{Name: "BurnedNFTokens", Style: StyleDefault},
			{Name: "FirstNFTokenSequence", Style: StyleOptional},
			{Name: "AccountTxnID", Style: StyleOptional},
			{Name: "WalletLocator", Style: StyleOptional},
			{Name: "TicketCount", Style: StyleOptional},
			{Name: "AMMID", Style: StyleOptional},
			{Name: "VaultID", Style: StyleOptional},
			{Name: "LoanBrokerID", Style: StyleOptional},
			{Name: "WalletSize", Style: StyleOptional},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "Offer",
		Fields: []Field{
			{Name: "Account", Style: StyleRequired},
			{Name: "Sequence", Style: StyleRequired},
			{Name: "TakerPays", Style: StyleRequired},
			{Name: "TakerGets", Style: StyleRequired},
			{Name: "BookDirectory", Style: StyleRequired},
			{Name: "BookNode", Style: StyleRequired},
			{Name: "OwnerNode", Style: StyleRequired},
			{Name: "Expiration", Style: StyleOptional},
			{Name: "Flags", Style: StyleRequired},
			{Name: "DomainID", Style: StyleOptional},
			{Name: "AdditionalBooks", Style: StyleOptional},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "DirectoryNode",
		Fields: []Field{
			{Name: "Flags", Style: StyleRequired},
			{Name: "RootIndex", Meta: MetaAlways, Style: StyleRequired},
			{Name: "Indexes", Meta: MetaNever, Style: StyleRequired},
			{Name: "IndexNext", Style: StyleOptional},
			{Name: "IndexPrevious", Style: StyleOptional},
			{Name: "Owner", Style: StyleOptional},
			{Name: "TakerPaysCurrency", Style: StyleOptional},
			{Name: "TakerPaysIssuer", Style: StyleOptional},
			{Name: "TakerPaysMPT", Style: StyleOptional},
			{Name: "TakerGetsCurrency", Style: StyleOptional},
			{Name: "TakerGetsIssuer", Style: StyleOptional},
			{Name: "TakerGetsMPT", Style: StyleOptional},
			{Name: "ExchangeRate", Style: StyleOptional},
			{Name: "NFTokenID", Style: StyleOptional},
			{Name: "DomainID", Style: StyleOptional},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleOptional},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleOptional},
		},
	},
	{
		Name:                   "RippleState",
		AllowBadCurrencyDecode: true,
		Fields: []Field{
			{Name: "Flags", Style: StyleRequired},
			{Name: "Balance", Style: StyleRequired},
			{Name: "LowLimit", Style: StyleRequired},
			{Name: "HighLimit", Style: StyleRequired},
			{Name: "LowNode", Style: StyleOptional},
			{Name: "HighNode", Style: StyleOptional},
			{Name: "LowQualityIn", Style: StyleOptional},
			{Name: "LowQualityOut", Style: StyleOptional},
			{Name: "HighQualityIn", Style: StyleOptional},
			{Name: "HighQualityOut", Style: StyleOptional},
			{Name: "HighSponsor", Style: StyleOptional},
			{Name: "LowSponsor", Style: StyleOptional},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	// Cold-path entry types. Field lists come from rippled's
	// include/xrpl/protocol/detail/ledger_entries.macro. The per-field Meta
	// follows rippled's global sMD_* table (sfields.macro) — every entry
	// here uses defaults except PreviousTxnID/Seq (DeleteFinal) and the
	// few sMD_Never cases (RootIndex=Always, Indexes=Never).
	{
		Name: "NFTokenOffer",
		Fields: []Field{
			// rippled's macro (ledger_entries.macro ltNFTOKEN_OFFER) uses
			// sfOwner, matched by go-xrpl's serializer
			// (internal/tx/nftoken/nftoken_serialize.go).
			{Name: "Owner", Style: StyleRequired},
			// Older go-xrpl releases wrote sfAccount; retain its owner value while
			// canonicalizing subsequent writes to sfOwner.
			{Name: "Account", Style: StyleOptional, DecodeOnly: true, DecodeAlias: "Owner"},
			{Name: "NFTokenID", Style: StyleRequired},
			{Name: "Amount", Style: StyleRequired},
			{Name: "OwnerNode", Style: StyleRequired},
			{Name: "NFTokenOfferNode", Style: StyleRequired},
			{Name: "Destination", Style: StyleOptional},
			{Name: "Expiration", Style: StyleOptional},
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "Check",
		Fields: []Field{
			{Name: "Account", Style: StyleRequired},
			{Name: "Destination", Style: StyleRequired},
			{Name: "SendMax", Style: StyleRequired},
			{Name: "Sequence", Style: StyleRequired},
			{Name: "OwnerNode", Style: StyleRequired},
			{Name: "DestinationNode", Style: StyleRequired},
			{Name: "Expiration", Style: StyleOptional},
			{Name: "InvoiceID", Style: StyleOptional},
			{Name: "SourceTag", Style: StyleOptional},
			{Name: "DestinationTag", Style: StyleOptional},
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "DID",
		Fields: []Field{
			{Name: "Account", Style: StyleRequired},
			{Name: "DIDDocument", Style: StyleOptional},
			{Name: "URI", Style: StyleOptional},
			{Name: "Data", Style: StyleOptional},
			{Name: "OwnerNode", Style: StyleRequired},
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "NegativeUNL",
		Fields: []Field{
			{Name: "Flags", Style: StyleRequired},
			{Name: "DisabledValidators", Style: StyleOptional},
			{Name: "ValidatorToDisable", Style: StyleOptional},
			{Name: "ValidatorToReEnable", Style: StyleOptional},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleOptional},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleOptional},
		},
	},
	{
		Name: "NFTokenPage",
		Fields: []Field{
			{Name: "PreviousPageMin", Style: StyleOptional},
			{Name: "NextPageMin", Style: StyleOptional},
			{Name: "NFTokens", Style: StyleRequired},
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "SignerList",
		Fields: []Field{
			// rippled's ltSIGNER_LIST has no sfAccount (ledger_entries.macro:
			// 122-129); the field order mirrors that macro. Account is decoded
			// for tolerance only: go-xrpl releases before the write-path fix
			// stored an owner Account on the blob, so a node reading such a
			// legacy entry must still decode it (then re-encode without it).
			{Name: "Account", DecodeOnly: true, Style: StyleOptional},
			// sfOwner is written on creation once fixIncludeKeyletFields is
			// active, recording the keylet input (the list is keyed by owner).
			{Name: "Owner", Style: StyleOptional},
			{Name: "OwnerNode", Style: StyleRequired},
			{Name: "SignerQuorum", Style: StyleRequired},
			{Name: "SignerEntries", Style: StyleRequired},
			{Name: "SignerListID", Style: StyleRequired},
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "Ticket",
		Fields: []Field{
			{Name: "Account", Style: StyleRequired},
			{Name: "OwnerNode", Style: StyleRequired},
			{Name: "TicketSequence", Style: StyleRequired},
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "Amendments",
		Fields: []Field{
			{Name: "Flags", Style: StyleRequired},
			{Name: "Amendments", Style: StyleOptional},
			{Name: "Majorities", Style: StyleOptional},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleOptional},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleOptional},
		},
	},
	{
		Name: "LedgerHashes",
		Fields: []Field{
			// sfFlags is soeREQUIRED (commonFields) — the skip-list writer
			// serializes Flags=0 on every LedgerHashes; the typed decoder must accept it.
			{Name: "Flags", Style: StyleRequired},
			{Name: "FirstLedgerSequence", Style: StyleOptional},
			{Name: "LastLedgerSequence", Style: StyleOptional},
			{Name: "Hashes", Style: StyleRequired},
		},
	},
	{
		Name: "Bridge",
		Fields: []Field{
			{Name: "Account", Style: StyleRequired},
			{Name: "SignatureReward", Style: StyleRequired},
			{Name: "MinAccountCreateAmount", Style: StyleOptional},
			{Name: "XChainBridge", Style: StyleRequired},
			{Name: "XChainClaimID", Style: StyleRequired},
			{Name: "XChainAccountCreateCount", Style: StyleRequired},
			{Name: "XChainAccountClaimCount", Style: StyleRequired},
			{Name: "OwnerNode", Style: StyleRequired},
			// sfFlags is soeREQUIRED (commonFields) — serialized at its default 0
			// on every Bridge; the typed decoder must accept it.
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "DepositPreauth",
		Fields: []Field{
			{Name: "Account", Style: StyleRequired},
			{Name: "Authorize", Style: StyleOptional},
			{Name: "OwnerNode", Style: StyleRequired},
			{Name: "AuthorizeCredentials", Style: StyleOptional},
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "XChainOwnedClaimID",
		Fields: []Field{
			{Name: "Account", Style: StyleRequired},
			{Name: "XChainBridge", Style: StyleRequired},
			{Name: "XChainClaimID", Style: StyleRequired},
			{Name: "OtherChainSource", Style: StyleRequired},
			{Name: "XChainClaimAttestations", Style: StyleRequired},
			{Name: "SignatureReward", Style: StyleRequired},
			{Name: "OwnerNode", Style: StyleRequired},
			// sfFlags is soeREQUIRED (commonFields) — serialized at its default 0
			// on every XChainOwnedClaimID; the typed decoder must accept it.
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "FeeSettings",
		Fields: []Field{
			{Name: "BaseFee", Style: StyleOptional},
			{Name: "ReferenceFeeUnits", Style: StyleOptional},
			{Name: "ReserveBase", Style: StyleOptional},
			{Name: "ReserveIncrement", Style: StyleOptional},
			{Name: "BaseFeeDrops", Style: StyleOptional},
			{Name: "ReserveBaseDrops", Style: StyleOptional},
			{Name: "ReserveIncrementDrops", Style: StyleOptional},
			// sfFlags is soeREQUIRED (commonFields) — serialized at its default 0
			// on every FeeSettings; the typed decoder must accept it.
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleOptional},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleOptional},
		},
	},
	{
		Name: "XChainOwnedCreateAccountClaimID",
		Fields: []Field{
			{Name: "Account", Style: StyleRequired},
			{Name: "XChainBridge", Style: StyleRequired},
			{Name: "XChainAccountCreateCount", Style: StyleRequired},
			{Name: "XChainCreateAccountAttestations", Style: StyleRequired},
			{Name: "OwnerNode", Style: StyleRequired},
			// sfFlags is soeREQUIRED (commonFields) — serialized at its default 0
			// on every XChainOwnedCreateAccountClaimID; the typed decoder must accept it.
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "Escrow",
		Fields: []Field{
			{Name: "Account", Style: StyleRequired},
			// sfSequence records the keylet input (owner + creating sequence)
			// once fixIncludeKeyletFields is active.
			{Name: "Sequence", Style: StyleOptional},
			{Name: "Destination", Style: StyleRequired},
			{Name: "Amount", Style: StyleRequired},
			{Name: "Condition", Style: StyleOptional},
			{Name: "CancelAfter", Style: StyleOptional},
			{Name: "FinishAfter", Style: StyleOptional},
			{Name: "SourceTag", Style: StyleOptional},
			{Name: "DestinationTag", Style: StyleOptional},
			{Name: "OwnerNode", Style: StyleRequired},
			{Name: "DestinationNode", Style: StyleOptional},
			{Name: "TransferRate", Style: StyleOptional},
			{Name: "IssuerNode", Style: StyleOptional},
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "PayChannel",
		Fields: []Field{
			{Name: "Account", Style: StyleRequired},
			{Name: "Destination", Style: StyleRequired},
			// sfSequence records the keylet input (owner + destination +
			// creating sequence) once fixIncludeKeyletFields is active.
			{Name: "Sequence", Style: StyleOptional},
			{Name: "Amount", Style: StyleRequired},
			{Name: "Balance", Style: StyleRequired},
			{Name: "PublicKey", Style: StyleRequired},
			{Name: "SettleDelay", Style: StyleRequired},
			{Name: "Expiration", Style: StyleOptional},
			{Name: "CancelAfter", Style: StyleOptional},
			{Name: "SourceTag", Style: StyleOptional},
			{Name: "DestinationTag", Style: StyleOptional},
			{Name: "OwnerNode", Style: StyleRequired},
			{Name: "DestinationNode", Style: StyleOptional},
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "AMM",
		Fields: []Field{
			{Name: "Account", Style: StyleRequired},
			{Name: "TradingFee", Style: StyleDefault},
			{Name: "VoteSlots", Style: StyleOptional},
			{Name: "AuctionSlot", Style: StyleOptional},
			{Name: "LPTokenBalance", Style: StyleRequired},
			{Name: "Asset", Style: StyleRequired},
			{Name: "Asset2", Style: StyleRequired},
			{Name: "OwnerNode", Style: StyleRequired},
			// sfFlags is soeREQUIRED (commonFields) — serialized at its default 0
			// on every AMM; the typed decoder must accept it.
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleOptional},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleOptional},
		},
	},
	{
		Name: "MPTokenIssuance",
		Fields: []Field{
			{Name: "Issuer", Style: StyleRequired},
			{Name: "Sequence", Style: StyleRequired},
			{Name: "TransferFee", Style: StyleDefault},
			{Name: "OwnerNode", Style: StyleRequired},
			{Name: "AssetScale", Style: StyleDefault},
			{Name: "MaximumAmount", Style: StyleOptional},
			{Name: "OutstandingAmount", Style: StyleRequired},
			{Name: "LockedAmount", Style: StyleOptional},
			{Name: "MPTokenMetadata", Style: StyleOptional},
			{Name: "DomainID", Style: StyleOptional},
			{Name: "MutableFlags", Style: StyleDefault},
			{Name: "ReferenceHolding", Style: StyleOptional},
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "MPToken",
		Fields: []Field{
			{Name: "Account", Style: StyleRequired},
			{Name: "MPTokenIssuanceID", Style: StyleRequired},
			{Name: "MPTAmount", Style: StyleDefault},
			{Name: "LockedAmount", Style: StyleOptional},
			{Name: "OwnerNode", Style: StyleRequired},
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "Oracle",
		Fields: []Field{
			{Name: "Owner", Style: StyleRequired},
			// sfOracleDocumentID records the keylet input (owner + document id)
			// once fixIncludeKeyletFields is active.
			{Name: "OracleDocumentID", Style: StyleOptional},
			{Name: "Provider", Style: StyleRequired},
			{Name: "PriceDataSeries", Style: StyleRequired},
			{Name: "AssetClass", Style: StyleRequired},
			{Name: "LastUpdateTime", Style: StyleRequired},
			{Name: "URI", Style: StyleOptional},
			{Name: "OwnerNode", Style: StyleRequired},
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "Credential",
		Fields: []Field{
			{Name: "Subject", Style: StyleRequired},
			{Name: "Issuer", Style: StyleRequired},
			{Name: "CredentialType", Style: StyleRequired},
			{Name: "Expiration", Style: StyleOptional},
			{Name: "URI", Style: StyleOptional},
			{Name: "IssuerNode", Style: StyleRequired},
			{Name: "SubjectNode", Style: StyleOptional},
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "PermissionedDomain",
		Fields: []Field{
			{Name: "Owner", Style: StyleRequired},
			{Name: "Sequence", Style: StyleRequired},
			{Name: "AcceptedCredentials", Style: StyleRequired},
			{Name: "OwnerNode", Style: StyleRequired},
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "Delegate",
		Fields: []Field{
			{Name: "Account", Style: StyleRequired},
			{Name: "Authorize", Style: StyleRequired},
			{Name: "Permissions", Style: StyleRequired},
			{Name: "OwnerNode", Style: StyleRequired},
			{Name: "DestinationNode", Style: StyleOptional},
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "Vault",
		Fields: []Field{
			{Name: "Sequence", Style: StyleRequired},
			{Name: "OwnerNode", Style: StyleRequired},
			{Name: "Owner", Style: StyleRequired},
			{Name: "Account", Style: StyleRequired},
			{Name: "Data", Style: StyleOptional},
			{Name: "Asset", Style: StyleRequired},
			{Name: "AssetsTotal", Style: StyleDefault},
			{Name: "AssetsAvailable", Style: StyleDefault},
			{Name: "AssetsMaximum", Style: StyleDefault},
			{Name: "LossUnrealized", Style: StyleDefault},
			{Name: "ShareMPTID", Style: StyleRequired},
			{Name: "WithdrawalPolicy", Style: StyleRequired},
			{Name: "Scale", Style: StyleDefault},
			// sfFlags is soeREQUIRED (commonFields) — serialized at its default 0
			// on every Vault; the typed decoder must accept it.
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "LoanBroker",
		Fields: []Field{
			{Name: "Sequence", Style: StyleRequired},
			{Name: "OwnerNode", Style: StyleRequired},
			{Name: "VaultNode", Style: StyleRequired},
			{Name: "VaultID", Style: StyleRequired},
			{Name: "Account", Style: StyleRequired},
			{Name: "Owner", Style: StyleRequired},
			{Name: "LoanSequence", Style: StyleRequired},
			{Name: "Data", Style: StyleDefault},
			{Name: "ManagementFeeRate", Style: StyleDefault},
			{Name: "OwnerCount", Style: StyleDefault},
			{Name: "DebtTotal", Style: StyleDefault},
			{Name: "DebtMaximum", Style: StyleDefault},
			{Name: "CoverAvailable", Style: StyleDefault},
			{Name: "CoverRateMinimum", Style: StyleDefault},
			{Name: "CoverRateLiquidation", Style: StyleDefault},
			// sfFlags is soeREQUIRED (commonFields) — serialized at its default 0.
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "Loan",
		Fields: []Field{
			{Name: "OwnerNode", Style: StyleRequired},
			{Name: "LoanBrokerNode", Style: StyleRequired},
			{Name: "LoanBrokerID", Style: StyleRequired},
			{Name: "LoanSequence", Style: StyleRequired},
			{Name: "Borrower", Style: StyleRequired},
			{Name: "LoanOriginationFee", Style: StyleDefault},
			{Name: "LoanServiceFee", Style: StyleDefault},
			{Name: "LatePaymentFee", Style: StyleDefault},
			{Name: "ClosePaymentFee", Style: StyleDefault},
			{Name: "OverpaymentFee", Style: StyleDefault},
			{Name: "InterestRate", Style: StyleDefault},
			{Name: "LateInterestRate", Style: StyleDefault},
			{Name: "CloseInterestRate", Style: StyleDefault},
			{Name: "OverpaymentInterestRate", Style: StyleDefault},
			{Name: "StartDate", Style: StyleRequired},
			{Name: "PaymentInterval", Style: StyleRequired},
			{Name: "GracePeriod", Style: StyleDefault},
			{Name: "PreviousPaymentDueDate", Style: StyleDefault},
			{Name: "NextPaymentDueDate", Style: StyleDefault},
			{Name: "PaymentRemaining", Style: StyleDefault},
			{Name: "PeriodicPayment", Style: StyleRequired},
			{Name: "PrincipalOutstanding", Style: StyleDefault},
			{Name: "TotalValueOutstanding", Style: StyleDefault},
			{Name: "ManagementFeeOutstanding", Style: StyleDefault},
			{Name: "LoanScale", Style: StyleDefault},
			// sfFlags is soeREQUIRED (commonFields) — serialized at its default 0.
			{Name: "Flags", Style: StyleRequired},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
		},
	},
	{
		Name: "Sponsorship",
		Fields: []Field{
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal, Style: StyleRequired, DeferredRequired: true},
			{Name: "Owner", Style: StyleRequired},
			{Name: "Sponsee", Style: StyleRequired},
			{Name: "FeeAmount", Style: StyleOptional},
			{Name: "MaxFee", Style: StyleOptional},
			{Name: "RemainingOwnerCount", Style: StyleDefault},
			{Name: "OwnerNode", Style: StyleRequired},
			{Name: "SponseeNode", Style: StyleRequired},
			{Name: "Flags", Style: StyleRequired},
		},
	},
}
