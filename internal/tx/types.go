package tx

import "fmt"

// Type represents a transaction type code
type Type uint16

const RippleEpoch int64 = 946684800

// All transaction type codes from rippled
const (
	TypeInvalid Type = 0xFFFF // Invalid/unknown type

	// Standard transaction types
	TypePayment                      Type = 0  // ttPAYMENT
	TypeEscrowCreate                 Type = 1  // ttESCROW_CREATE
	TypeEscrowFinish                 Type = 2  // ttESCROW_FINISH
	TypeAccountSet                   Type = 3  // ttACCOUNT_SET
	TypeEscrowCancel                 Type = 4  // ttESCROW_CANCEL
	TypeRegularKeySet                Type = 5  // ttREGULAR_KEY_SET
	TypeNickNameSet                  Type = 6  // ttNICKNAME_SET (deprecated)
	TypeOfferCreate                  Type = 7  // ttOFFER_CREATE
	TypeOfferCancel                  Type = 8  // ttOFFER_CANCEL
	TypeContract                     Type = 9  // ttCONTRACT (deprecated)
	TypeTicketCreate                 Type = 10 // ttTICKET_CREATE
	TypeSpinalTap                    Type = 11 // Reserved, never used
	TypeSignerListSet                Type = 12 // ttSIGNER_LIST_SET
	TypePaymentChannelCreate         Type = 13 // ttPAYCHAN_CREATE
	TypePaymentChannelFund           Type = 14 // ttPAYCHAN_FUND
	TypePaymentChannelClaim          Type = 15 // ttPAYCHAN_CLAIM
	TypeCheckCreate                  Type = 16 // ttCHECK_CREATE
	TypeCheckCash                    Type = 17 // ttCHECK_CASH
	TypeCheckCancel                  Type = 18 // ttCHECK_CANCEL
	TypeDepositPreauth               Type = 19 // ttDEPOSIT_PREAUTH
	TypeTrustSet                     Type = 20 // ttTRUST_SET
	TypeAccountDelete                Type = 21 // ttACCOUNT_DELETE
	TypeHookSet                      Type = 22 // ttHOOK_SET (reserved)
	TypeNFTokenMint                  Type = 25 // ttNFTOKEN_MINT
	TypeNFTokenBurn                  Type = 26 // ttNFTOKEN_BURN
	TypeNFTokenCreateOffer           Type = 27 // ttNFTOKEN_CREATE_OFFER
	TypeNFTokenCancelOffer           Type = 28 // ttNFTOKEN_CANCEL_OFFER
	TypeNFTokenAcceptOffer           Type = 29 // ttNFTOKEN_ACCEPT_OFFER
	TypeClawback                     Type = 30 // ttCLAWBACK
	TypeAMMClawback                  Type = 31 // ttAMM_CLAWBACK
	TypeAMMCreate                    Type = 35 // ttAMM_CREATE
	TypeAMMDeposit                   Type = 36 // ttAMM_DEPOSIT
	TypeAMMWithdraw                  Type = 37 // ttAMM_WITHDRAW
	TypeAMMVote                      Type = 38 // ttAMM_VOTE
	TypeAMMBid                       Type = 39 // ttAMM_BID
	TypeAMMDelete                    Type = 40 // ttAMM_DELETE
	TypeXChainCreateClaimID          Type = 41 // ttXCHAIN_CREATE_CLAIM_ID
	TypeXChainCommit                 Type = 42 // ttXCHAIN_COMMIT
	TypeXChainClaim                  Type = 43 // ttXCHAIN_CLAIM
	TypeXChainAccountCreateCommit    Type = 44 // ttXCHAIN_ACCOUNT_CREATE_COMMIT
	TypeXChainAddClaimAttestation    Type = 45 // ttXCHAIN_ADD_CLAIM_ATTESTATION
	TypeXChainAddAccountCreateAttest Type = 46 // ttXCHAIN_ADD_ACCOUNT_CREATE_ATTESTATION
	TypeXChainModifyBridge           Type = 47 // ttXCHAIN_MODIFY_BRIDGE
	TypeXChainCreateBridge           Type = 48 // ttXCHAIN_CREATE_BRIDGE
	TypeDIDSet                       Type = 49 // ttDID_SET
	TypeDIDDelete                    Type = 50 // ttDID_DELETE
	TypeOracleSet                    Type = 51 // ttORACLE_SET
	TypeOracleDelete                 Type = 52 // ttORACLE_DELETE
	TypeLedgerStateFix               Type = 53 // ttLEDGER_STATE_FIX
	TypeMPTokenIssuanceCreate        Type = 54 // ttMPTOKEN_ISSUANCE_CREATE
	TypeMPTokenIssuanceDestroy       Type = 55 // ttMPTOKEN_ISSUANCE_DESTROY
	TypeMPTokenIssuanceSet           Type = 56 // ttMPTOKEN_ISSUANCE_SET
	TypeMPTokenAuthorize             Type = 57 // ttMPTOKEN_AUTHORIZE
	TypeCredentialCreate             Type = 58 // ttCREDENTIAL_CREATE
	TypeCredentialAccept             Type = 59 // ttCREDENTIAL_ACCEPT
	TypeCredentialDelete             Type = 60 // ttCREDENTIAL_DELETE
	TypeNFTokenModify                Type = 61 // ttNFTOKEN_MODIFY
	TypePermissionedDomainSet        Type = 62 // ttPERMISSIONED_DOMAIN_SET
	TypePermissionedDomainDelete     Type = 63 // ttPERMISSIONED_DOMAIN_DELETE
	TypeDelegateSet                  Type = 64 // ttDELEGATE_SET
	TypeVaultCreate                  Type = 65 // ttVAULT_CREATE
	TypeVaultSet                     Type = 66 // ttVAULT_SET
	TypeVaultDelete                  Type = 67 // ttVAULT_DELETE
	TypeVaultDeposit                 Type = 68 // ttVAULT_DEPOSIT
	TypeVaultWithdraw                Type = 69 // ttVAULT_WITHDRAW
	TypeVaultClawback                Type = 70 // ttVAULT_CLAWBACK
	TypeBatch                        Type = 71 // ttBATCH

	// System-generated transaction types (pseudo-transactions)
	TypeAmendment Type = 100 // ttAMENDMENT
	TypeFee       Type = 101 // ttFEE
	TypeUNLModify Type = 102 // ttUNL_MODIFY
)

// String returns the string name of the transaction type
func (t Type) String() string {
	switch t {
	case TypePayment:
		return "Payment"
	case TypeEscrowCreate:
		return "EscrowCreate"
	case TypeEscrowFinish:
		return "EscrowFinish"
	case TypeAccountSet:
		return "AccountSet"
	case TypeEscrowCancel:
		return "EscrowCancel"
	case TypeRegularKeySet:
		return "SetRegularKey"
	case TypeOfferCreate:
		return "OfferCreate"
	case TypeOfferCancel:
		return "OfferCancel"
	case TypeTicketCreate:
		return "TicketCreate"
	case TypeSignerListSet:
		return "SignerListSet"
	case TypePaymentChannelCreate:
		return "PaymentChannelCreate"
	case TypePaymentChannelFund:
		return "PaymentChannelFund"
	case TypePaymentChannelClaim:
		return "PaymentChannelClaim"
	case TypeCheckCreate:
		return "CheckCreate"
	case TypeCheckCash:
		return "CheckCash"
	case TypeCheckCancel:
		return "CheckCancel"
	case TypeDepositPreauth:
		return "DepositPreauth"
	case TypeTrustSet:
		return "TrustSet"
	case TypeAccountDelete:
		return "AccountDelete"
	case TypeNFTokenMint:
		return "NFTokenMint"
	case TypeNFTokenBurn:
		return "NFTokenBurn"
	case TypeNFTokenCreateOffer:
		return "NFTokenCreateOffer"
	case TypeNFTokenCancelOffer:
		return "NFTokenCancelOffer"
	case TypeNFTokenAcceptOffer:
		return "NFTokenAcceptOffer"
	case TypeClawback:
		return "Clawback"
	case TypeAMMClawback:
		return "AMMClawback"
	case TypeAMMCreate:
		return "AMMCreate"
	case TypeAMMDeposit:
		return "AMMDeposit"
	case TypeAMMWithdraw:
		return "AMMWithdraw"
	case TypeAMMVote:
		return "AMMVote"
	case TypeAMMBid:
		return "AMMBid"
	case TypeAMMDelete:
		return "AMMDelete"
	case TypeXChainCreateClaimID:
		return "XChainCreateClaimID"
	case TypeXChainCommit:
		return "XChainCommit"
	case TypeXChainClaim:
		return "XChainClaim"
	case TypeXChainAccountCreateCommit:
		return "XChainAccountCreateCommit"
	case TypeXChainAddClaimAttestation:
		return "XChainAddClaimAttestation"
	case TypeXChainAddAccountCreateAttest:
		return "XChainAddAccountCreateAttestation"
	case TypeXChainModifyBridge:
		return "XChainModifyBridge"
	case TypeXChainCreateBridge:
		return "XChainCreateBridge"
	case TypeDIDSet:
		return "DIDSet"
	case TypeDIDDelete:
		return "DIDDelete"
	case TypeOracleSet:
		return "OracleSet"
	case TypeOracleDelete:
		return "OracleDelete"
	case TypeLedgerStateFix:
		return "LedgerStateFix"
	case TypeMPTokenIssuanceCreate:
		return "MPTokenIssuanceCreate"
	case TypeMPTokenIssuanceDestroy:
		return "MPTokenIssuanceDestroy"
	case TypeMPTokenIssuanceSet:
		return "MPTokenIssuanceSet"
	case TypeMPTokenAuthorize:
		return "MPTokenAuthorize"
	case TypeCredentialCreate:
		return "CredentialCreate"
	case TypeCredentialAccept:
		return "CredentialAccept"
	case TypeCredentialDelete:
		return "CredentialDelete"
	case TypeNFTokenModify:
		return "NFTokenModify"
	case TypePermissionedDomainSet:
		return "PermissionedDomainSet"
	case TypePermissionedDomainDelete:
		return "PermissionedDomainDelete"
	case TypeDelegateSet:
		return "DelegateSet"
	case TypeVaultCreate:
		return "VaultCreate"
	case TypeVaultSet:
		return "VaultSet"
	case TypeVaultDelete:
		return "VaultDelete"
	case TypeVaultDeposit:
		return "VaultDeposit"
	case TypeVaultWithdraw:
		return "VaultWithdraw"
	case TypeVaultClawback:
		return "VaultClawback"
	case TypeBatch:
		return "Batch"
	case TypeAmendment:
		return "EnableAmendment"
	case TypeFee:
		return "SetFee"
	case TypeUNLModify:
		return "UNLModify"
	default:
		return fmt.Sprintf("Unknown(%d)", t)
	}
}

// typeNameMap maps transaction type names to their codes
var typeNameMap = map[string]Type{
	"Payment":                           TypePayment,
	"EscrowCreate":                      TypeEscrowCreate,
	"EscrowFinish":                      TypeEscrowFinish,
	"AccountSet":                        TypeAccountSet,
	"EscrowCancel":                      TypeEscrowCancel,
	"SetRegularKey":                     TypeRegularKeySet,
	"OfferCreate":                       TypeOfferCreate,
	"OfferCancel":                       TypeOfferCancel,
	"TicketCreate":                      TypeTicketCreate,
	"SignerListSet":                     TypeSignerListSet,
	"PaymentChannelCreate":              TypePaymentChannelCreate,
	"PaymentChannelFund":                TypePaymentChannelFund,
	"PaymentChannelClaim":               TypePaymentChannelClaim,
	"CheckCreate":                       TypeCheckCreate,
	"CheckCash":                         TypeCheckCash,
	"CheckCancel":                       TypeCheckCancel,
	"DepositPreauth":                    TypeDepositPreauth,
	"TrustSet":                          TypeTrustSet,
	"AccountDelete":                     TypeAccountDelete,
	"NFTokenMint":                       TypeNFTokenMint,
	"NFTokenBurn":                       TypeNFTokenBurn,
	"NFTokenCreateOffer":                TypeNFTokenCreateOffer,
	"NFTokenCancelOffer":                TypeNFTokenCancelOffer,
	"NFTokenAcceptOffer":                TypeNFTokenAcceptOffer,
	"Clawback":                          TypeClawback,
	"AMMClawback":                       TypeAMMClawback,
	"AMMCreate":                         TypeAMMCreate,
	"AMMDeposit":                        TypeAMMDeposit,
	"AMMWithdraw":                       TypeAMMWithdraw,
	"AMMVote":                           TypeAMMVote,
	"AMMBid":                            TypeAMMBid,
	"AMMDelete":                         TypeAMMDelete,
	"XChainCreateClaimID":               TypeXChainCreateClaimID,
	"XChainCommit":                      TypeXChainCommit,
	"XChainClaim":                       TypeXChainClaim,
	"XChainAccountCreateCommit":         TypeXChainAccountCreateCommit,
	"XChainAddClaimAttestation":         TypeXChainAddClaimAttestation,
	"XChainAddAccountCreateAttestation": TypeXChainAddAccountCreateAttest,
	"XChainModifyBridge":                TypeXChainModifyBridge,
	"XChainCreateBridge":                TypeXChainCreateBridge,
	"DIDSet":                            TypeDIDSet,
	"DIDDelete":                         TypeDIDDelete,
	"OracleSet":                         TypeOracleSet,
	"OracleDelete":                      TypeOracleDelete,
	"LedgerStateFix":                    TypeLedgerStateFix,
	"MPTokenIssuanceCreate":             TypeMPTokenIssuanceCreate,
	"MPTokenIssuanceDestroy":            TypeMPTokenIssuanceDestroy,
	"MPTokenIssuanceSet":                TypeMPTokenIssuanceSet,
	"MPTokenAuthorize":                  TypeMPTokenAuthorize,
	"CredentialCreate":                  TypeCredentialCreate,
	"CredentialAccept":                  TypeCredentialAccept,
	"CredentialDelete":                  TypeCredentialDelete,
	"NFTokenModify":                     TypeNFTokenModify,
	"PermissionedDomainSet":             TypePermissionedDomainSet,
	"PermissionedDomainDelete":          TypePermissionedDomainDelete,
	"DelegateSet":                       TypeDelegateSet,
	"VaultCreate":                       TypeVaultCreate,
	"VaultSet":                          TypeVaultSet,
	"VaultDelete":                       TypeVaultDelete,
	"VaultDeposit":                      TypeVaultDeposit,
	"VaultWithdraw":                     TypeVaultWithdraw,
	"VaultClawback":                     TypeVaultClawback,
	"Batch":                             TypeBatch,
	"EnableAmendment":                   TypeAmendment,
	"SetFee":                            TypeFee,
	"UNLModify":                         TypeUNLModify,
}

// TypeFromName returns the transaction type for a given name
func TypeFromName(name string) (Type, bool) {
	t, ok := typeNameMap[name]
	return t, ok
}

// IsPseudoTransaction returns true if this is a system-generated transaction
func (t Type) IsPseudoTransaction() bool {
	return t == TypeAmendment || t == TypeFee || t == TypeUNLModify
}

// IsDeprecated returns true if this transaction type is deprecated
func (t Type) IsDeprecated() bool {
	return t == TypeNickNameSet || t == TypeContract || t == TypeSpinalTap || t == TypeHookSet
}
