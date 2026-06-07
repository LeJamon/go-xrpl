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
			{Name: "VaultID"},
			{Name: "WalletSize"},
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
			{Name: "AdditionalBooks"},
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
	// Cold-path entry types. Field lists come from rippled's
	// include/xrpl/protocol/detail/ledger_entries.macro. The per-field Meta
	// follows rippled's global sMD_* table (sfields.macro) — every entry
	// here uses defaults except PreviousTxnID/Seq (DeleteFinal) and the
	// few sMD_Never cases that match what the global tx.fieldMetadata map
	// records (RootIndex=Always, Indexes=Never).
	{
		Name: "NFTokenOffer",
		Fields: []Field{
			// rippled's macro uses sfOwner; go-xrpl's serializer
			// (internal/tx/nftoken/nftoken_serialize.go) emits sfAccount.
			// Include both so the typed Decode handles either source.
			{Name: "Account"},
			{Name: "Owner"},
			{Name: "NFTokenID"},
			{Name: "Amount"},
			{Name: "OwnerNode"},
			{Name: "NFTokenOfferNode"},
			{Name: "Destination"},
			{Name: "Expiration"},
			{Name: "Flags"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "Check",
		Fields: []Field{
			{Name: "Account"},
			{Name: "Destination"},
			{Name: "SendMax"},
			{Name: "Sequence"},
			{Name: "OwnerNode"},
			{Name: "DestinationNode"},
			{Name: "Expiration"},
			{Name: "InvoiceID"},
			{Name: "SourceTag"},
			{Name: "DestinationTag"},
			{Name: "Flags"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "DID",
		Fields: []Field{
			{Name: "Account"},
			{Name: "DIDDocument"},
			{Name: "URI"},
			{Name: "Data"},
			{Name: "OwnerNode"},
			{Name: "Flags"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "NegativeUNL",
		Fields: []Field{
			{Name: "Flags"},
			{Name: "DisabledValidators"},
			{Name: "ValidatorToDisable"},
			{Name: "ValidatorToReEnable"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "NFTokenPage",
		Fields: []Field{
			{Name: "PreviousPageMin"},
			{Name: "NextPageMin"},
			{Name: "NFTokens"},
			{Name: "Flags"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "SignerList",
		Fields: []Field{
			// Account is go-xrpl-specific (rippled's macro doesn't list it;
			// the serializer in internal/ledger/state/signer_list.go emits
			// it for owner-account tracking).
			{Name: "Account"},
			{Name: "OwnerNode"},
			{Name: "SignerQuorum"},
			{Name: "SignerEntries"},
			{Name: "SignerListID"},
			{Name: "Flags"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "Ticket",
		Fields: []Field{
			{Name: "Account"},
			{Name: "OwnerNode"},
			{Name: "TicketSequence"},
			{Name: "Flags"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "Amendments",
		Fields: []Field{
			{Name: "Flags"},
			{Name: "Amendments"},
			{Name: "Majorities"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "LedgerHashes",
		Fields: []Field{
			{Name: "FirstLedgerSequence"},
			{Name: "LastLedgerSequence"},
			{Name: "Hashes"},
		},
	},
	{
		Name: "Bridge",
		Fields: []Field{
			{Name: "Account"},
			{Name: "SignatureReward"},
			{Name: "MinAccountCreateAmount"},
			{Name: "XChainBridge"},
			{Name: "XChainClaimID"},
			{Name: "XChainAccountCreateCount"},
			{Name: "XChainAccountClaimCount"},
			{Name: "OwnerNode"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "DepositPreauth",
		Fields: []Field{
			{Name: "Account"},
			{Name: "Authorize"},
			{Name: "OwnerNode"},
			{Name: "AuthorizeCredentials"},
			{Name: "Flags"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "XChainOwnedClaimID",
		Fields: []Field{
			{Name: "Account"},
			{Name: "XChainBridge"},
			{Name: "XChainClaimID"},
			{Name: "OtherChainSource"},
			{Name: "XChainClaimAttestations"},
			{Name: "SignatureReward"},
			{Name: "OwnerNode"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "FeeSettings",
		Fields: []Field{
			{Name: "BaseFee"},
			{Name: "ReferenceFeeUnits"},
			{Name: "ReserveBase"},
			{Name: "ReserveIncrement"},
			{Name: "BaseFeeDrops"},
			{Name: "ReserveBaseDrops"},
			{Name: "ReserveIncrementDrops"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "XChainOwnedCreateAccountClaimID",
		Fields: []Field{
			{Name: "Account"},
			{Name: "XChainBridge"},
			{Name: "XChainAccountCreateCount"},
			{Name: "XChainCreateAccountAttestations"},
			{Name: "OwnerNode"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "Escrow",
		Fields: []Field{
			{Name: "Account"},
			{Name: "Destination"},
			{Name: "Amount"},
			{Name: "Condition"},
			{Name: "CancelAfter"},
			{Name: "FinishAfter"},
			{Name: "FinishFunction"},
			{Name: "Data"},
			{Name: "SourceTag"},
			{Name: "DestinationTag"},
			{Name: "OwnerNode"},
			{Name: "DestinationNode"},
			{Name: "TransferRate"},
			{Name: "IssuerNode"},
			{Name: "Flags"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "PayChannel",
		Fields: []Field{
			{Name: "Account"},
			{Name: "Destination"},
			{Name: "Amount"},
			{Name: "Balance"},
			{Name: "PublicKey"},
			{Name: "SettleDelay"},
			{Name: "Expiration"},
			{Name: "CancelAfter"},
			{Name: "SourceTag"},
			{Name: "DestinationTag"},
			{Name: "OwnerNode"},
			{Name: "DestinationNode"},
			{Name: "Flags"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "AMM",
		Fields: []Field{
			{Name: "Account"},
			{Name: "TradingFee"},
			{Name: "VoteSlots"},
			{Name: "AuctionSlot"},
			{Name: "LPTokenBalance"},
			{Name: "Asset"},
			{Name: "Asset2"},
			{Name: "OwnerNode"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "MPTokenIssuance",
		Fields: []Field{
			{Name: "Issuer"},
			{Name: "Sequence"},
			{Name: "TransferFee"},
			{Name: "OwnerNode"},
			{Name: "AssetScale"},
			{Name: "MaximumAmount"},
			{Name: "OutstandingAmount"},
			{Name: "LockedAmount"},
			{Name: "MPTokenMetadata"},
			{Name: "DomainID"},
			{Name: "Flags"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "MPToken",
		Fields: []Field{
			{Name: "Account"},
			// Issuer + Sequence are go-xrpl extras (not in rippled's
			// ltMPTOKEN macro) emitted by
			// internal/ledger/state/mptoken_entry.go's serializer.
			{Name: "Issuer"},
			{Name: "Sequence"},
			{Name: "MPTokenIssuanceID"},
			{Name: "MPTAmount"},
			{Name: "LockedAmount"},
			{Name: "OwnerNode"},
			{Name: "Flags"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "Oracle",
		Fields: []Field{
			{Name: "Owner"},
			{Name: "Provider"},
			{Name: "PriceDataSeries"},
			{Name: "AssetClass"},
			{Name: "LastUpdateTime"},
			{Name: "URI"},
			{Name: "OwnerNode"},
			{Name: "Flags"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "Credential",
		Fields: []Field{
			{Name: "Subject"},
			{Name: "Issuer"},
			{Name: "CredentialType"},
			{Name: "Expiration"},
			{Name: "URI"},
			{Name: "IssuerNode"},
			{Name: "SubjectNode"},
			{Name: "Flags"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "PermissionedDomain",
		Fields: []Field{
			{Name: "Owner"},
			{Name: "Sequence"},
			{Name: "AcceptedCredentials"},
			{Name: "OwnerNode"},
			{Name: "Flags"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "Delegate",
		Fields: []Field{
			{Name: "Account"},
			{Name: "Authorize"},
			{Name: "Permissions"},
			{Name: "OwnerNode"},
			{Name: "Flags"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
	{
		Name: "Vault",
		Fields: []Field{
			{Name: "Sequence"},
			{Name: "OwnerNode"},
			{Name: "Owner"},
			{Name: "Account"},
			{Name: "Data"},
			{Name: "Asset"},
			{Name: "AssetsTotal"},
			{Name: "AssetsAvailable"},
			{Name: "AssetsMaximum"},
			{Name: "LossUnrealized"},
			{Name: "ShareMPTID"},
			{Name: "WithdrawalPolicy"},
			{Name: "PreviousTxnID", Meta: MetaDeleteFinal},
			{Name: "PreviousTxnLgrSeq", Meta: MetaDeleteFinal},
		},
	},
}
