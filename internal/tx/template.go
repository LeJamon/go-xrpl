package tx

import "github.com/LeJamon/go-xrpl/internal/tx/ter"

// fieldStyle is a templated field's presence requirement (rippled's SOEStyle).
type fieldStyle uint8

const (
	soeREQUIRED fieldStyle = iota
	soeOPTIONAL
	soeDEFAULT
)

// commonFields are the fields permitted on every transaction type, regardless
// of the per-type template. They correspond to rippled's TxFormats commonFields
// set that is merged into every transaction format.
var commonFields = map[string]fieldStyle{
	"TransactionType":    soeREQUIRED,
	"Flags":              soeOPTIONAL,
	"SourceTag":          soeOPTIONAL,
	"Account":            soeREQUIRED,
	"Sequence":           soeREQUIRED,
	"PreviousTxnID":      soeOPTIONAL,
	"LastLedgerSequence": soeOPTIONAL,
	"AccountTxnID":       soeOPTIONAL,
	"Fee":                soeREQUIRED,
	"OperationLimit":     soeOPTIONAL,
	"Memos":              soeOPTIONAL,
	"SigningPubKey":      soeREQUIRED,
	"TicketSequence":     soeOPTIONAL,
	"TxnSignature":       soeOPTIONAL,
	"Signers":            soeOPTIONAL,
	"NetworkID":          soeOPTIONAL,
	"Delegate":           soeOPTIONAL,
}

// txTemplates holds the per-transaction-type field allowlist (the unique fields
// of each transaction format). A field is allowed on a transaction if it is in
// commonFields or in this type's template; any other codec-known field is
// rejected at parse time, matching rippled's applyTemplate which throws for a
// field "found in disallowed location".
var txTemplates = map[Type]map[string]fieldStyle{
	TypePayment: {
		"Destination":    soeREQUIRED,
		"Amount":         soeREQUIRED,
		"SendMax":        soeOPTIONAL,
		"Paths":          soeDEFAULT,
		"InvoiceID":      soeOPTIONAL,
		"DestinationTag": soeOPTIONAL,
		"DeliverMin":     soeOPTIONAL,
		"CredentialIDs":  soeOPTIONAL,
		"DomainID":       soeOPTIONAL,
	},
	TypeEscrowCreate: {
		"Destination":    soeREQUIRED,
		"Amount":         soeREQUIRED,
		"Condition":      soeOPTIONAL,
		"CancelAfter":    soeOPTIONAL,
		"FinishAfter":    soeOPTIONAL,
		"DestinationTag": soeOPTIONAL,
	},
	TypeEscrowFinish: {
		"Owner":         soeREQUIRED,
		"OfferSequence": soeREQUIRED,
		"Fulfillment":   soeOPTIONAL,
		"Condition":     soeOPTIONAL,
		"CredentialIDs": soeOPTIONAL,
	},
	TypeAccountSet: {
		"EmailHash":     soeOPTIONAL,
		"WalletLocator": soeOPTIONAL,
		"WalletSize":    soeOPTIONAL,
		"MessageKey":    soeOPTIONAL,
		"Domain":        soeOPTIONAL,
		"TransferRate":  soeOPTIONAL,
		"SetFlag":       soeOPTIONAL,
		"ClearFlag":     soeOPTIONAL,
		"TickSize":      soeOPTIONAL,
		"NFTokenMinter": soeOPTIONAL,
	},
	TypeEscrowCancel: {
		"Owner":         soeREQUIRED,
		"OfferSequence": soeREQUIRED,
	},
	TypeRegularKeySet: {
		"RegularKey": soeOPTIONAL,
	},
	TypeOfferCreate: {
		"TakerPays":     soeREQUIRED,
		"TakerGets":     soeREQUIRED,
		"Expiration":    soeOPTIONAL,
		"OfferSequence": soeOPTIONAL,
		"DomainID":      soeOPTIONAL,
	},
	TypeOfferCancel: {
		"OfferSequence": soeREQUIRED,
	},
	TypeTicketCreate: {
		"TicketCount": soeREQUIRED,
	},
	TypeSignerListSet: {
		"SignerQuorum":  soeREQUIRED,
		"SignerEntries": soeOPTIONAL,
	},
	TypePaymentChannelCreate: {
		"Destination":    soeREQUIRED,
		"Amount":         soeREQUIRED,
		"SettleDelay":    soeREQUIRED,
		"PublicKey":      soeREQUIRED,
		"CancelAfter":    soeOPTIONAL,
		"DestinationTag": soeOPTIONAL,
	},
	TypePaymentChannelFund: {
		"Channel":    soeREQUIRED,
		"Amount":     soeREQUIRED,
		"Expiration": soeOPTIONAL,
	},
	TypePaymentChannelClaim: {
		"Channel":       soeREQUIRED,
		"Amount":        soeOPTIONAL,
		"Balance":       soeOPTIONAL,
		"Signature":     soeOPTIONAL,
		"PublicKey":     soeOPTIONAL,
		"CredentialIDs": soeOPTIONAL,
	},
	TypeCheckCreate: {
		"Destination":    soeREQUIRED,
		"SendMax":        soeREQUIRED,
		"Expiration":     soeOPTIONAL,
		"DestinationTag": soeOPTIONAL,
		"InvoiceID":      soeOPTIONAL,
	},
	TypeCheckCash: {
		"CheckID":    soeREQUIRED,
		"Amount":     soeOPTIONAL,
		"DeliverMin": soeOPTIONAL,
	},
	TypeCheckCancel: {
		"CheckID": soeREQUIRED,
	},
	TypeDepositPreauth: {
		"Authorize":              soeOPTIONAL,
		"Unauthorize":            soeOPTIONAL,
		"AuthorizeCredentials":   soeOPTIONAL,
		"UnauthorizeCredentials": soeOPTIONAL,
	},
	TypeTrustSet: {
		"LimitAmount": soeOPTIONAL,
		"QualityIn":   soeOPTIONAL,
		"QualityOut":  soeOPTIONAL,
	},
	TypeAccountDelete: {
		"Destination":    soeREQUIRED,
		"DestinationTag": soeOPTIONAL,
		"CredentialIDs":  soeOPTIONAL,
	},
	TypeNFTokenMint: {
		"NFTokenTaxon": soeREQUIRED,
		"TransferFee":  soeOPTIONAL,
		"Issuer":       soeOPTIONAL,
		"URI":          soeOPTIONAL,
		"Amount":       soeOPTIONAL,
		"Destination":  soeOPTIONAL,
		"Expiration":   soeOPTIONAL,
	},
	TypeNFTokenBurn: {
		"NFTokenID": soeREQUIRED,
		"Owner":     soeOPTIONAL,
	},
	TypeNFTokenCreateOffer: {
		"NFTokenID":   soeREQUIRED,
		"Amount":      soeREQUIRED,
		"Destination": soeOPTIONAL,
		"Owner":       soeOPTIONAL,
		"Expiration":  soeOPTIONAL,
	},
	TypeNFTokenCancelOffer: {
		"NFTokenOffers": soeREQUIRED,
	},
	TypeNFTokenAcceptOffer: {
		"NFTokenBuyOffer":  soeOPTIONAL,
		"NFTokenSellOffer": soeOPTIONAL,
		"NFTokenBrokerFee": soeOPTIONAL,
	},
	TypeClawback: {
		"Amount": soeREQUIRED,
		"Holder": soeOPTIONAL,
	},
	TypeAMMClawback: {
		"Holder": soeREQUIRED,
		"Asset":  soeREQUIRED,
		"Asset2": soeREQUIRED,
		"Amount": soeOPTIONAL,
	},
	TypeAMMCreate: {
		"Amount":     soeREQUIRED,
		"Amount2":    soeREQUIRED,
		"TradingFee": soeREQUIRED,
	},
	TypeAMMDeposit: {
		"Asset":      soeREQUIRED,
		"Asset2":     soeREQUIRED,
		"Amount":     soeOPTIONAL,
		"Amount2":    soeOPTIONAL,
		"EPrice":     soeOPTIONAL,
		"LPTokenOut": soeOPTIONAL,
		"TradingFee": soeOPTIONAL,
	},
	TypeAMMWithdraw: {
		"Asset":     soeREQUIRED,
		"Asset2":    soeREQUIRED,
		"Amount":    soeOPTIONAL,
		"Amount2":   soeOPTIONAL,
		"EPrice":    soeOPTIONAL,
		"LPTokenIn": soeOPTIONAL,
	},
	TypeAMMVote: {
		"Asset":      soeREQUIRED,
		"Asset2":     soeREQUIRED,
		"TradingFee": soeREQUIRED,
	},
	TypeAMMBid: {
		"Asset":        soeREQUIRED,
		"Asset2":       soeREQUIRED,
		"BidMin":       soeOPTIONAL,
		"BidMax":       soeOPTIONAL,
		"AuthAccounts": soeOPTIONAL,
	},
	TypeAMMDelete: {
		"Asset":  soeREQUIRED,
		"Asset2": soeREQUIRED,
	},
	TypeXChainCreateClaimID: {
		"XChainBridge":     soeREQUIRED,
		"SignatureReward":  soeREQUIRED,
		"OtherChainSource": soeREQUIRED,
	},
	TypeXChainCommit: {
		"XChainBridge":          soeREQUIRED,
		"XChainClaimID":         soeREQUIRED,
		"Amount":                soeREQUIRED,
		"OtherChainDestination": soeOPTIONAL,
	},
	TypeXChainClaim: {
		"XChainBridge":   soeREQUIRED,
		"XChainClaimID":  soeREQUIRED,
		"Destination":    soeREQUIRED,
		"DestinationTag": soeOPTIONAL,
		"Amount":         soeREQUIRED,
	},
	TypeXChainAccountCreateCommit: {
		"XChainBridge":    soeREQUIRED,
		"Destination":     soeREQUIRED,
		"Amount":          soeREQUIRED,
		"SignatureReward": soeREQUIRED,
	},
	TypeXChainAddClaimAttestation: {
		"XChainBridge":             soeREQUIRED,
		"AttestationSignerAccount": soeREQUIRED,
		"PublicKey":                soeREQUIRED,
		"Signature":                soeREQUIRED,
		"OtherChainSource":         soeREQUIRED,
		"Amount":                   soeREQUIRED,
		"AttestationRewardAccount": soeREQUIRED,
		"WasLockingChainSend":      soeREQUIRED,
		"XChainClaimID":            soeREQUIRED,
		"Destination":              soeOPTIONAL,
	},
	TypeXChainAddAccountCreateAttest: {
		"XChainBridge":             soeREQUIRED,
		"AttestationSignerAccount": soeREQUIRED,
		"PublicKey":                soeREQUIRED,
		"Signature":                soeREQUIRED,
		"OtherChainSource":         soeREQUIRED,
		"Amount":                   soeREQUIRED,
		"AttestationRewardAccount": soeREQUIRED,
		"WasLockingChainSend":      soeREQUIRED,
		"XChainAccountCreateCount": soeREQUIRED,
		"Destination":              soeREQUIRED,
		"SignatureReward":          soeREQUIRED,
	},
	TypeXChainModifyBridge: {
		"XChainBridge":           soeREQUIRED,
		"SignatureReward":        soeOPTIONAL,
		"MinAccountCreateAmount": soeOPTIONAL,
	},
	TypeXChainCreateBridge: {
		"XChainBridge":           soeREQUIRED,
		"SignatureReward":        soeREQUIRED,
		"MinAccountCreateAmount": soeOPTIONAL,
	},
	TypeDIDSet: {
		"DIDDocument": soeOPTIONAL,
		"URI":         soeOPTIONAL,
		"Data":        soeOPTIONAL,
	},
	TypeDIDDelete: {},
	TypeOracleSet: {
		"OracleDocumentID": soeREQUIRED,
		"Provider":         soeOPTIONAL,
		"URI":              soeOPTIONAL,
		"AssetClass":       soeOPTIONAL,
		"LastUpdateTime":   soeREQUIRED,
		"PriceDataSeries":  soeREQUIRED,
	},
	TypeOracleDelete: {
		"OracleDocumentID": soeREQUIRED,
	},
	TypeLedgerStateFix: {
		"LedgerFixType": soeREQUIRED,
		"Owner":         soeOPTIONAL,
	},
	TypeMPTokenIssuanceCreate: {
		"AssetScale":      soeOPTIONAL,
		"TransferFee":     soeOPTIONAL,
		"MaximumAmount":   soeOPTIONAL,
		"MPTokenMetadata": soeOPTIONAL,
		"DomainID":        soeOPTIONAL,
	},
	TypeMPTokenIssuanceDestroy: {
		"MPTokenIssuanceID": soeREQUIRED,
	},
	TypeMPTokenIssuanceSet: {
		"MPTokenIssuanceID": soeREQUIRED,
		"Holder":            soeOPTIONAL,
		"DomainID":          soeOPTIONAL,
	},
	TypeMPTokenAuthorize: {
		"MPTokenIssuanceID": soeREQUIRED,
		"Holder":            soeOPTIONAL,
	},
	TypeCredentialCreate: {
		"Subject":        soeREQUIRED,
		"CredentialType": soeREQUIRED,
		"Expiration":     soeOPTIONAL,
		"URI":            soeOPTIONAL,
	},
	TypeCredentialAccept: {
		"Issuer":         soeREQUIRED,
		"CredentialType": soeREQUIRED,
	},
	TypeCredentialDelete: {
		"Subject":        soeOPTIONAL,
		"Issuer":         soeOPTIONAL,
		"CredentialType": soeREQUIRED,
	},
	TypeNFTokenModify: {
		"NFTokenID": soeREQUIRED,
		"Owner":     soeOPTIONAL,
		"URI":       soeOPTIONAL,
	},
	TypePermissionedDomainSet: {
		"DomainID":            soeOPTIONAL,
		"AcceptedCredentials": soeREQUIRED,
	},
	TypePermissionedDomainDelete: {
		"DomainID": soeREQUIRED,
	},
	TypeDelegateSet: {
		"Authorize":   soeREQUIRED,
		"Permissions": soeREQUIRED,
	},
	TypeVaultCreate: {
		"Asset":            soeREQUIRED,
		"AssetsMaximum":    soeOPTIONAL,
		"MPTokenMetadata":  soeOPTIONAL,
		"DomainID":         soeOPTIONAL,
		"WithdrawalPolicy": soeOPTIONAL,
		"Data":             soeOPTIONAL,
	},
	TypeVaultSet: {
		"VaultID":       soeREQUIRED,
		"AssetsMaximum": soeOPTIONAL,
		"DomainID":      soeOPTIONAL,
		"Data":          soeOPTIONAL,
	},
	TypeVaultDelete: {
		"VaultID": soeREQUIRED,
	},
	TypeVaultDeposit: {
		"VaultID": soeREQUIRED,
		"Amount":  soeREQUIRED,
	},
	TypeVaultWithdraw: {
		"VaultID":        soeREQUIRED,
		"Amount":         soeREQUIRED,
		"Destination":    soeOPTIONAL,
		"DestinationTag": soeOPTIONAL,
	},
	TypeVaultClawback: {
		"VaultID": soeREQUIRED,
		"Holder":  soeREQUIRED,
		"Amount":  soeOPTIONAL,
	},
	TypeBatch: {
		"RawTransactions": soeREQUIRED,
		"BatchSigners":    soeOPTIONAL,
	},
	TypeAmendment: {
		"LedgerSequence": soeREQUIRED,
		"Amendment":      soeREQUIRED,
	},
	TypeFee: {
		"LedgerSequence":        soeOPTIONAL,
		"BaseFee":               soeOPTIONAL,
		"ReferenceFeeUnits":     soeOPTIONAL,
		"ReserveBase":           soeOPTIONAL,
		"ReserveIncrement":      soeOPTIONAL,
		"BaseFeeDrops":          soeOPTIONAL,
		"ReserveBaseDrops":      soeOPTIONAL,
		"ReserveIncrementDrops": soeOPTIONAL,
	},
	TypeUNLModify: {
		"UNLModifyDisabling": soeREQUIRED,
		"LedgerSequence":     soeREQUIRED,
		"UNLModifyValidator": soeREQUIRED,
	},
}

// checkTemplate enforces a transaction type's field allowlist on the set of
// decoded field names, rejecting any codec-known field that is neither a common
// field nor part of the type's template. This mirrors rippled's STTx template
// application, which throws when a field is "found in disallowed location",
// preventing such a transaction from ever reaching apply.
func checkTemplate(txType Type, fields map[string]bool) error {
	template, ok := txTemplates[txType]
	if !ok {
		// Unknown / unregistered transaction type: no template to enforce.
		// Type resolution is handled separately by the registry.
		return nil
	}
	for name := range fields {
		if _, ok := commonFields[name]; ok {
			continue
		}
		if _, ok := template[name]; ok {
			continue
		}
		return ter.Errorf(ter.TemMALFORMED,
			"field %q is not allowed for transaction type %s", name, txType)
	}
	return nil
}
