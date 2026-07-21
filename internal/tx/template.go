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

type templateField struct {
	name  string
	style fieldStyle
}

// commonFields are the fields permitted on every transaction type, regardless
// of the per-type template. They correspond to rippled's TxFormats commonFields
// set that is merged into every transaction format.
var commonFields = []templateField{
	{name: "TransactionType", style: soeREQUIRED},
	{name: "Flags", style: soeOPTIONAL},
	{name: "SourceTag", style: soeOPTIONAL},
	{name: "Account", style: soeREQUIRED},
	{name: "Sequence", style: soeREQUIRED},
	{name: "PreviousTxnID", style: soeOPTIONAL},
	{name: "LastLedgerSequence", style: soeOPTIONAL},
	{name: "AccountTxnID", style: soeOPTIONAL},
	{name: "Fee", style: soeREQUIRED},
	{name: "OperationLimit", style: soeOPTIONAL},
	{name: "Memos", style: soeOPTIONAL},
	{name: "SigningPubKey", style: soeREQUIRED},
	{name: "TicketSequence", style: soeOPTIONAL},
	{name: "TxnSignature", style: soeOPTIONAL},
	{name: "Signers", style: soeOPTIONAL},
	{name: "NetworkID", style: soeOPTIONAL},
	{name: "Delegate", style: soeOPTIONAL},
	{name: "Sponsor", style: soeOPTIONAL},
	{name: "SponsorFlags", style: soeOPTIONAL},
	{name: "SponsorSignature", style: soeOPTIONAL},
}

// txTemplates holds the per-transaction-type field allowlist (the unique fields
// of each transaction format). A field is allowed on a transaction if it is in
// commonFields or in this type's template; any other codec-known field is
// rejected at parse time, matching rippled's applyTemplate which throws for a
// field "found in disallowed location".
var txTemplates = map[Type][]templateField{
	TypePayment: {
		{name: "Destination", style: soeREQUIRED},
		{name: "Amount", style: soeREQUIRED},
		{name: "SendMax", style: soeOPTIONAL},
		{name: "Paths", style: soeDEFAULT},
		{name: "InvoiceID", style: soeOPTIONAL},
		{name: "DestinationTag", style: soeOPTIONAL},
		{name: "DeliverMin", style: soeOPTIONAL},
		{name: "CredentialIDs", style: soeOPTIONAL},
		{name: "DomainID", style: soeOPTIONAL},
	},
	TypeEscrowCreate: {
		{name: "Destination", style: soeREQUIRED},
		{name: "Amount", style: soeREQUIRED},
		{name: "Condition", style: soeOPTIONAL},
		{name: "CancelAfter", style: soeOPTIONAL},
		{name: "FinishAfter", style: soeOPTIONAL},
		{name: "DestinationTag", style: soeOPTIONAL},
	},
	TypeEscrowFinish: {
		{name: "Owner", style: soeREQUIRED},
		{name: "OfferSequence", style: soeREQUIRED},
		{name: "Fulfillment", style: soeOPTIONAL},
		{name: "Condition", style: soeOPTIONAL},
		{name: "CredentialIDs", style: soeOPTIONAL},
	},
	TypeAccountSet: {
		{name: "EmailHash", style: soeOPTIONAL},
		{name: "WalletLocator", style: soeOPTIONAL},
		{name: "WalletSize", style: soeOPTIONAL},
		{name: "MessageKey", style: soeOPTIONAL},
		{name: "Domain", style: soeOPTIONAL},
		{name: "TransferRate", style: soeOPTIONAL},
		{name: "SetFlag", style: soeOPTIONAL},
		{name: "ClearFlag", style: soeOPTIONAL},
		{name: "TickSize", style: soeOPTIONAL},
		{name: "NFTokenMinter", style: soeOPTIONAL},
	},
	TypeEscrowCancel: {
		{name: "Owner", style: soeREQUIRED},
		{name: "OfferSequence", style: soeREQUIRED},
	},
	TypeRegularKeySet: {
		{name: "RegularKey", style: soeOPTIONAL},
	},
	TypeOfferCreate: {
		{name: "TakerPays", style: soeREQUIRED},
		{name: "TakerGets", style: soeREQUIRED},
		{name: "Expiration", style: soeOPTIONAL},
		{name: "OfferSequence", style: soeOPTIONAL},
		{name: "DomainID", style: soeOPTIONAL},
	},
	TypeOfferCancel: {
		{name: "OfferSequence", style: soeREQUIRED},
	},
	TypeTicketCreate: {
		{name: "TicketCount", style: soeREQUIRED},
	},
	TypeSignerListSet: {
		{name: "SignerQuorum", style: soeREQUIRED},
		{name: "SignerEntries", style: soeOPTIONAL},
	},
	TypePaymentChannelCreate: {
		{name: "Destination", style: soeREQUIRED},
		{name: "Amount", style: soeREQUIRED},
		{name: "SettleDelay", style: soeREQUIRED},
		{name: "PublicKey", style: soeREQUIRED},
		{name: "CancelAfter", style: soeOPTIONAL},
		{name: "DestinationTag", style: soeOPTIONAL},
	},
	TypePaymentChannelFund: {
		{name: "Channel", style: soeREQUIRED},
		{name: "Amount", style: soeREQUIRED},
		{name: "Expiration", style: soeOPTIONAL},
	},
	TypePaymentChannelClaim: {
		{name: "Channel", style: soeREQUIRED},
		{name: "Amount", style: soeOPTIONAL},
		{name: "Balance", style: soeOPTIONAL},
		{name: "Signature", style: soeOPTIONAL},
		{name: "PublicKey", style: soeOPTIONAL},
		{name: "CredentialIDs", style: soeOPTIONAL},
	},
	TypeCheckCreate: {
		{name: "Destination", style: soeREQUIRED},
		{name: "SendMax", style: soeREQUIRED},
		{name: "Expiration", style: soeOPTIONAL},
		{name: "DestinationTag", style: soeOPTIONAL},
		{name: "InvoiceID", style: soeOPTIONAL},
	},
	TypeCheckCash: {
		{name: "CheckID", style: soeREQUIRED},
		{name: "Amount", style: soeOPTIONAL},
		{name: "DeliverMin", style: soeOPTIONAL},
	},
	TypeCheckCancel: {
		{name: "CheckID", style: soeREQUIRED},
	},
	TypeDepositPreauth: {
		{name: "Authorize", style: soeOPTIONAL},
		{name: "Unauthorize", style: soeOPTIONAL},
		{name: "AuthorizeCredentials", style: soeOPTIONAL},
		{name: "UnauthorizeCredentials", style: soeOPTIONAL},
	},
	TypeTrustSet: {
		{name: "LimitAmount", style: soeOPTIONAL},
		{name: "QualityIn", style: soeOPTIONAL},
		{name: "QualityOut", style: soeOPTIONAL},
	},
	TypeAccountDelete: {
		{name: "Destination", style: soeREQUIRED},
		{name: "DestinationTag", style: soeOPTIONAL},
		{name: "CredentialIDs", style: soeOPTIONAL},
	},
	TypeNFTokenMint: {
		{name: "NFTokenTaxon", style: soeREQUIRED},
		{name: "TransferFee", style: soeOPTIONAL},
		{name: "Issuer", style: soeOPTIONAL},
		{name: "URI", style: soeOPTIONAL},
		{name: "Amount", style: soeOPTIONAL},
		{name: "Destination", style: soeOPTIONAL},
		{name: "Expiration", style: soeOPTIONAL},
	},
	TypeNFTokenBurn: {
		{name: "NFTokenID", style: soeREQUIRED},
		{name: "Owner", style: soeOPTIONAL},
	},
	TypeNFTokenCreateOffer: {
		{name: "NFTokenID", style: soeREQUIRED},
		{name: "Amount", style: soeREQUIRED},
		{name: "Destination", style: soeOPTIONAL},
		{name: "Owner", style: soeOPTIONAL},
		{name: "Expiration", style: soeOPTIONAL},
	},
	TypeNFTokenCancelOffer: {
		{name: "NFTokenOffers", style: soeREQUIRED},
	},
	TypeNFTokenAcceptOffer: {
		{name: "NFTokenBuyOffer", style: soeOPTIONAL},
		{name: "NFTokenSellOffer", style: soeOPTIONAL},
		{name: "NFTokenBrokerFee", style: soeOPTIONAL},
	},
	TypeClawback: {
		{name: "Amount", style: soeREQUIRED},
		{name: "Holder", style: soeOPTIONAL},
	},
	TypeAMMClawback: {
		{name: "Holder", style: soeREQUIRED},
		{name: "Asset", style: soeREQUIRED},
		{name: "Asset2", style: soeREQUIRED},
		{name: "Amount", style: soeOPTIONAL},
	},
	TypeAMMCreate: {
		{name: "Amount", style: soeREQUIRED},
		{name: "Amount2", style: soeREQUIRED},
		{name: "TradingFee", style: soeREQUIRED},
	},
	TypeAMMDeposit: {
		{name: "Asset", style: soeREQUIRED},
		{name: "Asset2", style: soeREQUIRED},
		{name: "Amount", style: soeOPTIONAL},
		{name: "Amount2", style: soeOPTIONAL},
		{name: "EPrice", style: soeOPTIONAL},
		{name: "LPTokenOut", style: soeOPTIONAL},
		{name: "TradingFee", style: soeOPTIONAL},
	},
	TypeAMMWithdraw: {
		{name: "Asset", style: soeREQUIRED},
		{name: "Asset2", style: soeREQUIRED},
		{name: "Amount", style: soeOPTIONAL},
		{name: "Amount2", style: soeOPTIONAL},
		{name: "EPrice", style: soeOPTIONAL},
		{name: "LPTokenIn", style: soeOPTIONAL},
	},
	TypeAMMVote: {
		{name: "Asset", style: soeREQUIRED},
		{name: "Asset2", style: soeREQUIRED},
		{name: "TradingFee", style: soeREQUIRED},
	},
	TypeAMMBid: {
		{name: "Asset", style: soeREQUIRED},
		{name: "Asset2", style: soeREQUIRED},
		{name: "BidMin", style: soeOPTIONAL},
		{name: "BidMax", style: soeOPTIONAL},
		{name: "AuthAccounts", style: soeOPTIONAL},
	},
	TypeAMMDelete: {
		{name: "Asset", style: soeREQUIRED},
		{name: "Asset2", style: soeREQUIRED},
	},
	TypeXChainCreateClaimID: {
		{name: "XChainBridge", style: soeREQUIRED},
		{name: "SignatureReward", style: soeREQUIRED},
		{name: "OtherChainSource", style: soeREQUIRED},
	},
	TypeXChainCommit: {
		{name: "XChainBridge", style: soeREQUIRED},
		{name: "XChainClaimID", style: soeREQUIRED},
		{name: "Amount", style: soeREQUIRED},
		{name: "OtherChainDestination", style: soeOPTIONAL},
	},
	TypeXChainClaim: {
		{name: "XChainBridge", style: soeREQUIRED},
		{name: "XChainClaimID", style: soeREQUIRED},
		{name: "Destination", style: soeREQUIRED},
		{name: "DestinationTag", style: soeOPTIONAL},
		{name: "Amount", style: soeREQUIRED},
	},
	TypeXChainAccountCreateCommit: {
		{name: "XChainBridge", style: soeREQUIRED},
		{name: "Destination", style: soeREQUIRED},
		{name: "Amount", style: soeREQUIRED},
		{name: "SignatureReward", style: soeREQUIRED},
	},
	TypeXChainAddClaimAttestation: {
		{name: "XChainBridge", style: soeREQUIRED},
		{name: "AttestationSignerAccount", style: soeREQUIRED},
		{name: "PublicKey", style: soeREQUIRED},
		{name: "Signature", style: soeREQUIRED},
		{name: "OtherChainSource", style: soeREQUIRED},
		{name: "Amount", style: soeREQUIRED},
		{name: "AttestationRewardAccount", style: soeREQUIRED},
		{name: "WasLockingChainSend", style: soeREQUIRED},
		{name: "XChainClaimID", style: soeREQUIRED},
		{name: "Destination", style: soeOPTIONAL},
	},
	TypeXChainAddAccountCreateAttest: {
		{name: "XChainBridge", style: soeREQUIRED},
		{name: "AttestationSignerAccount", style: soeREQUIRED},
		{name: "PublicKey", style: soeREQUIRED},
		{name: "Signature", style: soeREQUIRED},
		{name: "OtherChainSource", style: soeREQUIRED},
		{name: "Amount", style: soeREQUIRED},
		{name: "AttestationRewardAccount", style: soeREQUIRED},
		{name: "WasLockingChainSend", style: soeREQUIRED},
		{name: "XChainAccountCreateCount", style: soeREQUIRED},
		{name: "Destination", style: soeREQUIRED},
		{name: "SignatureReward", style: soeREQUIRED},
	},
	TypeXChainModifyBridge: {
		{name: "XChainBridge", style: soeREQUIRED},
		{name: "SignatureReward", style: soeOPTIONAL},
		{name: "MinAccountCreateAmount", style: soeOPTIONAL},
	},
	TypeXChainCreateBridge: {
		{name: "XChainBridge", style: soeREQUIRED},
		{name: "SignatureReward", style: soeREQUIRED},
		{name: "MinAccountCreateAmount", style: soeOPTIONAL},
	},
	TypeDIDSet: {
		{name: "DIDDocument", style: soeOPTIONAL},
		{name: "URI", style: soeOPTIONAL},
		{name: "Data", style: soeOPTIONAL},
	},
	TypeDIDDelete: {},
	TypeOracleSet: {
		{name: "OracleDocumentID", style: soeREQUIRED},
		{name: "Provider", style: soeOPTIONAL},
		{name: "URI", style: soeOPTIONAL},
		{name: "AssetClass", style: soeOPTIONAL},
		{name: "LastUpdateTime", style: soeREQUIRED},
		{name: "PriceDataSeries", style: soeREQUIRED},
	},
	TypeOracleDelete: {
		{name: "OracleDocumentID", style: soeREQUIRED},
	},
	TypeLedgerStateFix: {
		{name: "LedgerFixType", style: soeREQUIRED},
		{name: "Owner", style: soeOPTIONAL},
		{name: "BookDirectory", style: soeOPTIONAL},
	},
	TypeMPTokenIssuanceCreate: {
		{name: "AssetScale", style: soeOPTIONAL},
		{name: "TransferFee", style: soeOPTIONAL},
		{name: "MaximumAmount", style: soeOPTIONAL},
		{name: "MPTokenMetadata", style: soeOPTIONAL},
		{name: "DomainID", style: soeOPTIONAL},
		{name: "MutableFlags", style: soeOPTIONAL},
	},
	TypeMPTokenIssuanceDestroy: {
		{name: "MPTokenIssuanceID", style: soeREQUIRED},
	},
	TypeMPTokenIssuanceSet: {
		{name: "MPTokenIssuanceID", style: soeREQUIRED},
		{name: "Holder", style: soeOPTIONAL},
		{name: "DomainID", style: soeOPTIONAL},
		{name: "MPTokenMetadata", style: soeOPTIONAL},
		{name: "TransferFee", style: soeOPTIONAL},
		{name: "MutableFlags", style: soeOPTIONAL},
	},
	TypeMPTokenAuthorize: {
		{name: "MPTokenIssuanceID", style: soeREQUIRED},
		{name: "Holder", style: soeOPTIONAL},
	},
	TypeCredentialCreate: {
		{name: "Subject", style: soeREQUIRED},
		{name: "CredentialType", style: soeREQUIRED},
		{name: "Expiration", style: soeOPTIONAL},
		{name: "URI", style: soeOPTIONAL},
	},
	TypeCredentialAccept: {
		{name: "Issuer", style: soeREQUIRED},
		{name: "CredentialType", style: soeREQUIRED},
	},
	TypeCredentialDelete: {
		{name: "Subject", style: soeOPTIONAL},
		{name: "Issuer", style: soeOPTIONAL},
		{name: "CredentialType", style: soeREQUIRED},
	},
	TypeNFTokenModify: {
		{name: "NFTokenID", style: soeREQUIRED},
		{name: "Owner", style: soeOPTIONAL},
		{name: "URI", style: soeOPTIONAL},
	},
	TypePermissionedDomainSet: {
		{name: "DomainID", style: soeOPTIONAL},
		{name: "AcceptedCredentials", style: soeREQUIRED},
	},
	TypePermissionedDomainDelete: {
		{name: "DomainID", style: soeREQUIRED},
	},
	TypeDelegateSet: {
		{name: "Authorize", style: soeREQUIRED},
		{name: "Permissions", style: soeREQUIRED},
	},
	TypeVaultCreate: {
		{name: "Asset", style: soeREQUIRED},
		{name: "AssetsMaximum", style: soeOPTIONAL},
		{name: "MPTokenMetadata", style: soeOPTIONAL},
		{name: "DomainID", style: soeOPTIONAL},
		{name: "WithdrawalPolicy", style: soeOPTIONAL},
		{name: "Data", style: soeOPTIONAL},
		{name: "Scale", style: soeOPTIONAL},
	},
	TypeVaultSet: {
		{name: "VaultID", style: soeREQUIRED},
		{name: "AssetsMaximum", style: soeOPTIONAL},
		{name: "DomainID", style: soeOPTIONAL},
		{name: "Data", style: soeOPTIONAL},
	},
	TypeVaultDelete: {
		{name: "VaultID", style: soeREQUIRED},
	},
	TypeVaultDeposit: {
		{name: "VaultID", style: soeREQUIRED},
		{name: "Amount", style: soeREQUIRED},
	},
	TypeVaultWithdraw: {
		{name: "VaultID", style: soeREQUIRED},
		{name: "Amount", style: soeREQUIRED},
		{name: "Destination", style: soeOPTIONAL},
		{name: "DestinationTag", style: soeOPTIONAL},
	},
	TypeVaultClawback: {
		{name: "VaultID", style: soeREQUIRED},
		{name: "Holder", style: soeREQUIRED},
		{name: "Amount", style: soeOPTIONAL},
	},
	TypeBatch: {
		{name: "RawTransactions", style: soeREQUIRED},
		{name: "BatchSigners", style: soeOPTIONAL},
	},
	TypeLoanBrokerSet: {
		{name: "VaultID", style: soeREQUIRED},
		{name: "LoanBrokerID", style: soeOPTIONAL},
		{name: "Data", style: soeOPTIONAL},
		{name: "ManagementFeeRate", style: soeOPTIONAL},
		{name: "DebtMaximum", style: soeOPTIONAL},
		{name: "CoverRateMinimum", style: soeOPTIONAL},
		{name: "CoverRateLiquidation", style: soeOPTIONAL},
	},
	TypeLoanBrokerDelete: {
		{name: "LoanBrokerID", style: soeREQUIRED},
	},
	TypeLoanBrokerCoverDeposit: {
		{name: "LoanBrokerID", style: soeREQUIRED},
		{name: "Amount", style: soeREQUIRED},
	},
	TypeLoanBrokerCoverWithdraw: {
		{name: "LoanBrokerID", style: soeREQUIRED},
		{name: "Amount", style: soeREQUIRED},
		{name: "Destination", style: soeOPTIONAL},
		{name: "DestinationTag", style: soeOPTIONAL},
	},
	TypeLoanBrokerCoverClawback: {
		{name: "LoanBrokerID", style: soeOPTIONAL},
		{name: "Amount", style: soeOPTIONAL},
	},
	TypeLoanSet: {
		{name: "LoanBrokerID", style: soeREQUIRED},
		{name: "Data", style: soeOPTIONAL},
		{name: "Counterparty", style: soeOPTIONAL},
		{name: "CounterpartySignature", style: soeOPTIONAL},
		{name: "LoanOriginationFee", style: soeOPTIONAL},
		{name: "LoanServiceFee", style: soeOPTIONAL},
		{name: "LatePaymentFee", style: soeOPTIONAL},
		{name: "ClosePaymentFee", style: soeOPTIONAL},
		{name: "OverpaymentFee", style: soeOPTIONAL},
		{name: "InterestRate", style: soeOPTIONAL},
		{name: "LateInterestRate", style: soeOPTIONAL},
		{name: "CloseInterestRate", style: soeOPTIONAL},
		{name: "OverpaymentInterestRate", style: soeOPTIONAL},
		{name: "PrincipalRequested", style: soeREQUIRED},
		{name: "PaymentTotal", style: soeOPTIONAL},
		{name: "PaymentInterval", style: soeOPTIONAL},
		{name: "GracePeriod", style: soeOPTIONAL},
	},
	TypeLoanDelete: {
		{name: "LoanID", style: soeREQUIRED},
	},
	TypeLoanManage: {
		{name: "LoanID", style: soeREQUIRED},
	},
	TypeLoanPay: {
		{name: "LoanID", style: soeREQUIRED},
		{name: "Amount", style: soeREQUIRED},
	},
	TypeSponsorshipTransfer: {
		{name: "ObjectID", style: soeOPTIONAL},
		{name: "Sponsee", style: soeOPTIONAL},
	},
	TypeSponsorshipSet: {
		{name: "CounterpartySponsor", style: soeOPTIONAL},
		{name: "Sponsee", style: soeOPTIONAL},
		{name: "FeeAmount", style: soeOPTIONAL},
		{name: "MaxFee", style: soeOPTIONAL},
		{name: "RemainingOwnerCount", style: soeOPTIONAL},
	},
	TypeAmendment: {
		{name: "LedgerSequence", style: soeREQUIRED},
		{name: "Amendment", style: soeREQUIRED},
	},
	TypeFee: {
		{name: "LedgerSequence", style: soeOPTIONAL},
		{name: "BaseFee", style: soeOPTIONAL},
		{name: "ReferenceFeeUnits", style: soeOPTIONAL},
		{name: "ReserveBase", style: soeOPTIONAL},
		{name: "ReserveIncrement", style: soeOPTIONAL},
		{name: "BaseFeeDrops", style: soeOPTIONAL},
		{name: "ReserveBaseDrops", style: soeOPTIONAL},
		{name: "ReserveIncrementDrops", style: soeOPTIONAL},
	},
	TypeUNLModify: {
		{name: "UNLModifyDisabling", style: soeREQUIRED},
		{name: "LedgerSequence", style: soeREQUIRED},
		{name: "UNLModifyValidator", style: soeREQUIRED},
	},
}

var commonFieldStyles = indexTemplate(commonFields)
var txTemplateStyles = indexTemplates(txTemplates)
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

// FormatCommonFields returns the fields common to every transaction type in
// rippled's canonical declaration order.
func FormatCommonFields() []FormatField {
	return formatFields(commonFields)
}

// FormatTemplates returns each transaction type's unique fields (common fields
// excluded) keyed by the type's canonical name, for TRANSACTION_FORMATS.
func FormatTemplates() map[string][]FormatField {
	out := make(map[string][]FormatField, len(txTemplates))
	for t, tmpl := range txTemplates {
		out[t.String()] = formatFields(tmpl)
	}
	return out
}

func formatFields(fields []templateField) []FormatField {
	out := make([]FormatField, len(fields))
	for i, field := range fields {
		out[i] = FormatField{Name: field.name, Style: int(field.style)}
	}
	return out
}

func indexTemplate(fields []templateField) map[string]fieldStyle {
	index := make(map[string]fieldStyle, len(fields))
	for _, field := range fields {
		if _, exists := index[field.name]; exists {
			panic("duplicate transaction template field: " + field.name)
		}
		index[field.name] = field.style
	}
	return index
}

func indexTemplates(templates map[Type][]templateField) map[Type]map[string]fieldStyle {
	indexes := make(map[Type]map[string]fieldStyle, len(templates))
	for txType, fields := range templates {
		indexes[txType] = indexTemplate(fields)
	}
	return indexes
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
	for _, field := range txTemplates[txType] {
		if field.style == soeDEFAULT && fields[field.name] && isExplicitDefault(values[field.name]) {
			return errors.New("Field '" + field.name + "' may not be explicitly set to default.")
		}
	}

	return validateTemplateAllowlist(txType, fields)
}

// ValidateTransactionTemplateAllowlist rejects fields that are not legal for
// the concrete transaction type without requiring submission-layer defaults.
func ValidateTransactionTemplateAllowlist(transaction Transaction) error {
	values, err := transaction.Flatten()
	if err != nil {
		return err
	}
	fields := make(map[string]bool, len(values))
	for name := range values {
		fields[name] = true
	}
	return validateTemplateAllowlist(transaction.TxType(), fields)
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
	template, ok := txTemplateStyles[txType]
	if !ok {
		return nil
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := commonFieldStyles[name]; ok {
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
