package tx

import (
	"errors"
	"sort"

	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

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
		"BookDirectory": soeOPTIONAL,
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
	TypeLoanBrokerSet: {
		"VaultID":              soeREQUIRED,
		"LoanBrokerID":         soeOPTIONAL,
		"Data":                 soeOPTIONAL,
		"ManagementFeeRate":    soeOPTIONAL,
		"DebtMaximum":          soeOPTIONAL,
		"CoverRateMinimum":     soeOPTIONAL,
		"CoverRateLiquidation": soeOPTIONAL,
	},
	TypeLoanBrokerDelete: {
		"LoanBrokerID": soeREQUIRED,
	},
	TypeLoanBrokerCoverDeposit: {
		"LoanBrokerID": soeREQUIRED,
		"Amount":       soeREQUIRED,
	},
	TypeLoanBrokerCoverWithdraw: {
		"LoanBrokerID":   soeREQUIRED,
		"Amount":         soeREQUIRED,
		"Destination":    soeOPTIONAL,
		"DestinationTag": soeOPTIONAL,
	},
	TypeLoanBrokerCoverClawback: {
		"LoanBrokerID": soeOPTIONAL,
		"Amount":       soeOPTIONAL,
	},
	TypeLoanSet: {
		"LoanBrokerID":            soeREQUIRED,
		"Data":                    soeOPTIONAL,
		"Counterparty":            soeOPTIONAL,
		"CounterpartySignature":   soeOPTIONAL,
		"LoanOriginationFee":      soeOPTIONAL,
		"LoanServiceFee":          soeOPTIONAL,
		"LatePaymentFee":          soeOPTIONAL,
		"ClosePaymentFee":         soeOPTIONAL,
		"OverpaymentFee":          soeOPTIONAL,
		"InterestRate":            soeOPTIONAL,
		"LateInterestRate":        soeOPTIONAL,
		"CloseInterestRate":       soeOPTIONAL,
		"OverpaymentInterestRate": soeOPTIONAL,
		"PrincipalRequested":      soeREQUIRED,
		"PaymentTotal":            soeOPTIONAL,
		"PaymentInterval":         soeOPTIONAL,
		"GracePeriod":             soeOPTIONAL,
	},
	TypeLoanDelete: {
		"LoanID": soeREQUIRED,
	},
	TypeLoanManage: {
		"LoanID": soeREQUIRED,
	},
	TypeLoanPay: {
		"LoanID": soeREQUIRED,
		"Amount": soeREQUIRED,
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

var commonRequiredFields = []string{
	"TransactionType",
	"Account",
	"Sequence",
	"Fee",
	"SigningPubKey",
}

var txRequiredFields = map[Type][]string{
	TypePayment:                      {"Destination", "Amount"},
	TypeEscrowCreate:                 {"Destination", "Amount"},
	TypeEscrowFinish:                 {"Owner", "OfferSequence"},
	TypeEscrowCancel:                 {"Owner", "OfferSequence"},
	TypeOfferCreate:                  {"TakerPays", "TakerGets"},
	TypeOfferCancel:                  {"OfferSequence"},
	TypeTicketCreate:                 {"TicketCount"},
	TypeSignerListSet:                {"SignerQuorum"},
	TypePaymentChannelCreate:         {"Destination", "Amount", "SettleDelay", "PublicKey"},
	TypePaymentChannelFund:           {"Channel", "Amount"},
	TypePaymentChannelClaim:          {"Channel"},
	TypeCheckCreate:                  {"Destination", "SendMax"},
	TypeCheckCash:                    {"CheckID"},
	TypeCheckCancel:                  {"CheckID"},
	TypeAccountDelete:                {"Destination"},
	TypeNFTokenMint:                  {"NFTokenTaxon"},
	TypeNFTokenBurn:                  {"NFTokenID"},
	TypeNFTokenCreateOffer:           {"NFTokenID", "Amount"},
	TypeNFTokenCancelOffer:           {"NFTokenOffers"},
	TypeClawback:                     {"Amount"},
	TypeAMMClawback:                  {"Holder", "Asset", "Asset2"},
	TypeAMMCreate:                    {"Amount", "Amount2", "TradingFee"},
	TypeAMMDeposit:                   {"Asset", "Asset2"},
	TypeAMMWithdraw:                  {"Asset", "Asset2"},
	TypeAMMVote:                      {"Asset", "Asset2", "TradingFee"},
	TypeAMMBid:                       {"Asset", "Asset2"},
	TypeAMMDelete:                    {"Asset", "Asset2"},
	TypeXChainCreateClaimID:          {"XChainBridge", "SignatureReward", "OtherChainSource"},
	TypeXChainCommit:                 {"XChainBridge", "XChainClaimID", "Amount"},
	TypeXChainClaim:                  {"XChainBridge", "XChainClaimID", "Destination", "Amount"},
	TypeXChainAccountCreateCommit:    {"XChainBridge", "Destination", "Amount", "SignatureReward"},
	TypeXChainAddClaimAttestation:    {"XChainBridge", "AttestationSignerAccount", "PublicKey", "Signature", "OtherChainSource", "Amount", "AttestationRewardAccount", "WasLockingChainSend", "XChainClaimID"},
	TypeXChainAddAccountCreateAttest: {"XChainBridge", "AttestationSignerAccount", "PublicKey", "Signature", "OtherChainSource", "Amount", "AttestationRewardAccount", "WasLockingChainSend", "XChainAccountCreateCount", "Destination", "SignatureReward"},
	TypeXChainModifyBridge:           {"XChainBridge"},
	TypeXChainCreateBridge:           {"XChainBridge", "SignatureReward"},
	TypeOracleSet:                    {"OracleDocumentID", "LastUpdateTime", "PriceDataSeries"},
	TypeOracleDelete:                 {"OracleDocumentID"},
	TypeLedgerStateFix:               {"LedgerFixType"},
	TypeMPTokenIssuanceDestroy:       {"MPTokenIssuanceID"},
	TypeMPTokenIssuanceSet:           {"MPTokenIssuanceID"},
	TypeMPTokenAuthorize:             {"MPTokenIssuanceID"},
	TypeCredentialCreate:             {"Subject", "CredentialType"},
	TypeCredentialAccept:             {"Issuer", "CredentialType"},
	TypeCredentialDelete:             {"CredentialType"},
	TypeNFTokenModify:                {"NFTokenID"},
	TypePermissionedDomainSet:        {"AcceptedCredentials"},
	TypePermissionedDomainDelete:     {"DomainID"},
	TypeDelegateSet:                  {"Authorize", "Permissions"},
	TypeVaultCreate:                  {"Asset"},
	TypeVaultSet:                     {"VaultID"},
	TypeVaultDelete:                  {"VaultID"},
	TypeVaultDeposit:                 {"VaultID", "Amount"},
	TypeVaultWithdraw:                {"VaultID", "Amount"},
	TypeVaultClawback:                {"VaultID", "Holder"},
	TypeBatch:                        {"RawTransactions"},
	TypeLoanBrokerSet:                {"VaultID"},
	TypeLoanBrokerDelete:             {"LoanBrokerID"},
	TypeLoanBrokerCoverDeposit:       {"LoanBrokerID", "Amount"},
	TypeLoanBrokerCoverWithdraw:      {"LoanBrokerID", "Amount"},
	TypeLoanSet:                      {"LoanBrokerID", "PrincipalRequested"},
	TypeLoanDelete:                   {"LoanID"},
	TypeLoanManage:                   {"LoanID"},
	TypeLoanPay:                      {"LoanID", "Amount"},
	TypeAmendment:                    {"LedgerSequence", "Amendment"},
	TypeUNLModify:                    {"UNLModifyDisabling", "LedgerSequence", "UNLModifyValidator"},
}

// FormatField is one field of a transaction SOTemplate, exported for the
// server_definitions RPC TRANSACTION_FORMATS section. Style is rippled's
// SOEStyle int (0=required, 1=optional, 2=default).
type FormatField struct {
	Name  string
	Style int
}

// FormatCommonFields returns the fields common to every transaction type
// (rippled TxFormats::getCommonFields()), sorted by name for deterministic
// output.
func FormatCommonFields() []FormatField {
	return sortedFormatFields(commonFields)
}

// FormatTemplates returns each transaction type's unique fields (common fields
// excluded) keyed by the type's canonical name, for TRANSACTION_FORMATS.
func FormatTemplates() map[string][]FormatField {
	out := make(map[string][]FormatField, len(txTemplates))
	for t, tmpl := range txTemplates {
		out[t.String()] = sortedFormatFields(tmpl)
	}
	return out
}

func sortedFormatFields(m map[string]fieldStyle) []FormatField {
	out := make([]FormatField, 0, len(m))
	for name, style := range m {
		out = append(out, FormatField{Name: name, Style: int(style)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ValidateTemplateFields applies the structural portion of rippled's STTx
// template: required-field presence, explicit-default prohibition, and the
// per-type field allowlist. It does not run transaction validation or preflight.
func ValidateTemplateFields(txType Type, values map[string]any) error {
	if _, ok := txTemplates[txType]; !ok {
		return nil
	}
	fields := make(map[string]bool, len(values))
	for name := range values {
		fields[name] = true
	}
	for _, name := range txRequiredFields[txType] {
		if !fields[name] {
			return errors.New("Field '" + name + "' is required but missing.")
		}
	}
	for _, name := range commonRequiredFields {
		if !fields[name] {
			return errors.New("Field '" + name + "' is required but missing.")
		}
	}
	for name, style := range txTemplates[txType] {
		if style == soeDEFAULT && fields[name] && isExplicitDefault(values[name]) {
			return errors.New("Field '" + name + "' may not be explicitly set to default.")
		}
	}

	return validateTemplateAllowlist(txType, fields)
}

func isExplicitDefault(value any) bool {
	if value == nil {
		return true
	}
	switch typed := value.(type) {
	case []any:
		return len(typed) == 0
	case []map[string]any:
		return len(typed) == 0
	}
	return false
}

func validateTemplateAllowlist(txType Type, fields map[string]bool) error {
	template, ok := txTemplates[txType]
	if !ok {
		return nil
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := commonFields[name]; ok {
			continue
		}
		if _, ok := template[name]; ok {
			continue
		}
		return errors.New("Field '" + name + "' found in disallowed location.")
	}
	return nil
}

// checkTemplate preserves the transaction-engine TER contract for decoded
// fields that appear outside their transaction template.
func checkTemplate(txType Type, fields map[string]bool) error {
	if err := validateTemplateAllowlist(txType, fields); err != nil {
		return ter.Errorf(ter.TemMALFORMED, "%s", err)
	}
	return nil
}
