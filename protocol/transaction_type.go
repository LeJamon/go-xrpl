package protocol

import "fmt"

// TxType is an XRPL transaction type code (the tt* enumeration).
type TxType uint16

// Transaction type codes.
const (
	TxTypeInvalid TxType = 0xFFFF // Invalid/unknown type

	TxTypePayment                      TxType = 0  // ttPAYMENT
	TxTypeEscrowCreate                 TxType = 1  // ttESCROW_CREATE
	TxTypeEscrowFinish                 TxType = 2  // ttESCROW_FINISH
	TxTypeAccountSet                   TxType = 3  // ttACCOUNT_SET
	TxTypeEscrowCancel                 TxType = 4  // ttESCROW_CANCEL
	TxTypeRegularKeySet                TxType = 5  // ttREGULAR_KEY_SET
	TxTypeNickNameSet                  TxType = 6  // ttNICKNAME_SET (deprecated)
	TxTypeOfferCreate                  TxType = 7  // ttOFFER_CREATE
	TxTypeOfferCancel                  TxType = 8  // ttOFFER_CANCEL
	TxTypeContract                     TxType = 9  // ttCONTRACT (deprecated)
	TxTypeTicketCreate                 TxType = 10 // ttTICKET_CREATE
	TxTypeSpinalTap                    TxType = 11 // Reserved, never used
	TxTypeSignerListSet                TxType = 12 // ttSIGNER_LIST_SET
	TxTypePaymentChannelCreate         TxType = 13 // ttPAYCHAN_CREATE
	TxTypePaymentChannelFund           TxType = 14 // ttPAYCHAN_FUND
	TxTypePaymentChannelClaim          TxType = 15 // ttPAYCHAN_CLAIM
	TxTypeCheckCreate                  TxType = 16 // ttCHECK_CREATE
	TxTypeCheckCash                    TxType = 17 // ttCHECK_CASH
	TxTypeCheckCancel                  TxType = 18 // ttCHECK_CANCEL
	TxTypeDepositPreauth               TxType = 19 // ttDEPOSIT_PREAUTH
	TxTypeTrustSet                     TxType = 20 // ttTRUST_SET
	TxTypeAccountDelete                TxType = 21 // ttACCOUNT_DELETE
	TxTypeHookSet                      TxType = 22 // ttHOOK_SET (reserved)
	TxTypeNFTokenMint                  TxType = 25 // ttNFTOKEN_MINT
	TxTypeNFTokenBurn                  TxType = 26 // ttNFTOKEN_BURN
	TxTypeNFTokenCreateOffer           TxType = 27 // ttNFTOKEN_CREATE_OFFER
	TxTypeNFTokenCancelOffer           TxType = 28 // ttNFTOKEN_CANCEL_OFFER
	TxTypeNFTokenAcceptOffer           TxType = 29 // ttNFTOKEN_ACCEPT_OFFER
	TxTypeClawback                     TxType = 30 // ttCLAWBACK
	TxTypeAMMClawback                  TxType = 31 // ttAMM_CLAWBACK
	TxTypeAMMCreate                    TxType = 35 // ttAMM_CREATE
	TxTypeAMMDeposit                   TxType = 36 // ttAMM_DEPOSIT
	TxTypeAMMWithdraw                  TxType = 37 // ttAMM_WITHDRAW
	TxTypeAMMVote                      TxType = 38 // ttAMM_VOTE
	TxTypeAMMBid                       TxType = 39 // ttAMM_BID
	TxTypeAMMDelete                    TxType = 40 // ttAMM_DELETE
	TxTypeXChainCreateClaimID          TxType = 41 // ttXCHAIN_CREATE_CLAIM_ID
	TxTypeXChainCommit                 TxType = 42 // ttXCHAIN_COMMIT
	TxTypeXChainClaim                  TxType = 43 // ttXCHAIN_CLAIM
	TxTypeXChainAccountCreateCommit    TxType = 44 // ttXCHAIN_ACCOUNT_CREATE_COMMIT
	TxTypeXChainAddClaimAttestation    TxType = 45 // ttXCHAIN_ADD_CLAIM_ATTESTATION
	TxTypeXChainAddAccountCreateAttest TxType = 46 // ttXCHAIN_ADD_ACCOUNT_CREATE_ATTESTATION
	TxTypeXChainModifyBridge           TxType = 47 // ttXCHAIN_MODIFY_BRIDGE
	TxTypeXChainCreateBridge           TxType = 48 // ttXCHAIN_CREATE_BRIDGE
	TxTypeDIDSet                       TxType = 49 // ttDID_SET
	TxTypeDIDDelete                    TxType = 50 // ttDID_DELETE
	TxTypeOracleSet                    TxType = 51 // ttORACLE_SET
	TxTypeOracleDelete                 TxType = 52 // ttORACLE_DELETE
	TxTypeLedgerStateFix               TxType = 53 // ttLEDGER_STATE_FIX
	TxTypeMPTokenIssuanceCreate        TxType = 54 // ttMPTOKEN_ISSUANCE_CREATE
	TxTypeMPTokenIssuanceDestroy       TxType = 55 // ttMPTOKEN_ISSUANCE_DESTROY
	TxTypeMPTokenIssuanceSet           TxType = 56 // ttMPTOKEN_ISSUANCE_SET
	TxTypeMPTokenAuthorize             TxType = 57 // ttMPTOKEN_AUTHORIZE
	TxTypeCredentialCreate             TxType = 58 // ttCREDENTIAL_CREATE
	TxTypeCredentialAccept             TxType = 59 // ttCREDENTIAL_ACCEPT
	TxTypeCredentialDelete             TxType = 60 // ttCREDENTIAL_DELETE
	TxTypeNFTokenModify                TxType = 61 // ttNFTOKEN_MODIFY
	TxTypePermissionedDomainSet        TxType = 62 // ttPERMISSIONED_DOMAIN_SET
	TxTypePermissionedDomainDelete     TxType = 63 // ttPERMISSIONED_DOMAIN_DELETE
	TxTypeDelegateSet                  TxType = 64 // ttDELEGATE_SET
	TxTypeVaultCreate                  TxType = 65 // ttVAULT_CREATE
	TxTypeVaultSet                     TxType = 66 // ttVAULT_SET
	TxTypeVaultDelete                  TxType = 67 // ttVAULT_DELETE
	TxTypeVaultDeposit                 TxType = 68 // ttVAULT_DEPOSIT
	TxTypeVaultWithdraw                TxType = 69 // ttVAULT_WITHDRAW
	TxTypeVaultClawback                TxType = 70 // ttVAULT_CLAWBACK
	TxTypeBatch                        TxType = 71 // ttBATCH

	// System-generated transaction types (pseudo-transactions).
	TxTypeAmendment TxType = 100 // ttAMENDMENT
	TxTypeFee       TxType = 101 // ttFEE
	TxTypeUNLModify TxType = 102 // ttUNL_MODIFY
)

// String returns the canonical name of the transaction type.
func (t TxType) String() string {
	switch t {
	case TxTypePayment:
		return "Payment"
	case TxTypeEscrowCreate:
		return "EscrowCreate"
	case TxTypeEscrowFinish:
		return "EscrowFinish"
	case TxTypeAccountSet:
		return "AccountSet"
	case TxTypeEscrowCancel:
		return "EscrowCancel"
	case TxTypeRegularKeySet:
		return "SetRegularKey"
	case TxTypeOfferCreate:
		return "OfferCreate"
	case TxTypeOfferCancel:
		return "OfferCancel"
	case TxTypeTicketCreate:
		return "TicketCreate"
	case TxTypeSignerListSet:
		return "SignerListSet"
	case TxTypePaymentChannelCreate:
		return "PaymentChannelCreate"
	case TxTypePaymentChannelFund:
		return "PaymentChannelFund"
	case TxTypePaymentChannelClaim:
		return "PaymentChannelClaim"
	case TxTypeCheckCreate:
		return "CheckCreate"
	case TxTypeCheckCash:
		return "CheckCash"
	case TxTypeCheckCancel:
		return "CheckCancel"
	case TxTypeDepositPreauth:
		return "DepositPreauth"
	case TxTypeTrustSet:
		return "TrustSet"
	case TxTypeAccountDelete:
		return "AccountDelete"
	case TxTypeNFTokenMint:
		return "NFTokenMint"
	case TxTypeNFTokenBurn:
		return "NFTokenBurn"
	case TxTypeNFTokenCreateOffer:
		return "NFTokenCreateOffer"
	case TxTypeNFTokenCancelOffer:
		return "NFTokenCancelOffer"
	case TxTypeNFTokenAcceptOffer:
		return "NFTokenAcceptOffer"
	case TxTypeClawback:
		return "Clawback"
	case TxTypeAMMClawback:
		return "AMMClawback"
	case TxTypeAMMCreate:
		return "AMMCreate"
	case TxTypeAMMDeposit:
		return "AMMDeposit"
	case TxTypeAMMWithdraw:
		return "AMMWithdraw"
	case TxTypeAMMVote:
		return "AMMVote"
	case TxTypeAMMBid:
		return "AMMBid"
	case TxTypeAMMDelete:
		return "AMMDelete"
	case TxTypeXChainCreateClaimID:
		return "XChainCreateClaimID"
	case TxTypeXChainCommit:
		return "XChainCommit"
	case TxTypeXChainClaim:
		return "XChainClaim"
	case TxTypeXChainAccountCreateCommit:
		return "XChainAccountCreateCommit"
	case TxTypeXChainAddClaimAttestation:
		return "XChainAddClaimAttestation"
	case TxTypeXChainAddAccountCreateAttest:
		return "XChainAddAccountCreateAttestation"
	case TxTypeXChainModifyBridge:
		return "XChainModifyBridge"
	case TxTypeXChainCreateBridge:
		return "XChainCreateBridge"
	case TxTypeDIDSet:
		return "DIDSet"
	case TxTypeDIDDelete:
		return "DIDDelete"
	case TxTypeOracleSet:
		return "OracleSet"
	case TxTypeOracleDelete:
		return "OracleDelete"
	case TxTypeLedgerStateFix:
		return "LedgerStateFix"
	case TxTypeMPTokenIssuanceCreate:
		return "MPTokenIssuanceCreate"
	case TxTypeMPTokenIssuanceDestroy:
		return "MPTokenIssuanceDestroy"
	case TxTypeMPTokenIssuanceSet:
		return "MPTokenIssuanceSet"
	case TxTypeMPTokenAuthorize:
		return "MPTokenAuthorize"
	case TxTypeCredentialCreate:
		return "CredentialCreate"
	case TxTypeCredentialAccept:
		return "CredentialAccept"
	case TxTypeCredentialDelete:
		return "CredentialDelete"
	case TxTypeNFTokenModify:
		return "NFTokenModify"
	case TxTypePermissionedDomainSet:
		return "PermissionedDomainSet"
	case TxTypePermissionedDomainDelete:
		return "PermissionedDomainDelete"
	case TxTypeDelegateSet:
		return "DelegateSet"
	case TxTypeVaultCreate:
		return "VaultCreate"
	case TxTypeVaultSet:
		return "VaultSet"
	case TxTypeVaultDelete:
		return "VaultDelete"
	case TxTypeVaultDeposit:
		return "VaultDeposit"
	case TxTypeVaultWithdraw:
		return "VaultWithdraw"
	case TxTypeVaultClawback:
		return "VaultClawback"
	case TxTypeBatch:
		return "Batch"
	case TxTypeAmendment:
		return "EnableAmendment"
	case TxTypeFee:
		return "SetFee"
	case TxTypeUNLModify:
		return "UNLModify"
	default:
		return fmt.Sprintf("Unknown(%d)", t)
	}
}

// txTypeNameMap maps transaction type names to their codes.
var txTypeNameMap = map[string]TxType{
	"Payment":                           TxTypePayment,
	"EscrowCreate":                      TxTypeEscrowCreate,
	"EscrowFinish":                      TxTypeEscrowFinish,
	"AccountSet":                        TxTypeAccountSet,
	"EscrowCancel":                      TxTypeEscrowCancel,
	"SetRegularKey":                     TxTypeRegularKeySet,
	"OfferCreate":                       TxTypeOfferCreate,
	"OfferCancel":                       TxTypeOfferCancel,
	"TicketCreate":                      TxTypeTicketCreate,
	"SignerListSet":                     TxTypeSignerListSet,
	"PaymentChannelCreate":              TxTypePaymentChannelCreate,
	"PaymentChannelFund":                TxTypePaymentChannelFund,
	"PaymentChannelClaim":               TxTypePaymentChannelClaim,
	"CheckCreate":                       TxTypeCheckCreate,
	"CheckCash":                         TxTypeCheckCash,
	"CheckCancel":                       TxTypeCheckCancel,
	"DepositPreauth":                    TxTypeDepositPreauth,
	"TrustSet":                          TxTypeTrustSet,
	"AccountDelete":                     TxTypeAccountDelete,
	"NFTokenMint":                       TxTypeNFTokenMint,
	"NFTokenBurn":                       TxTypeNFTokenBurn,
	"NFTokenCreateOffer":                TxTypeNFTokenCreateOffer,
	"NFTokenCancelOffer":                TxTypeNFTokenCancelOffer,
	"NFTokenAcceptOffer":                TxTypeNFTokenAcceptOffer,
	"Clawback":                          TxTypeClawback,
	"AMMClawback":                       TxTypeAMMClawback,
	"AMMCreate":                         TxTypeAMMCreate,
	"AMMDeposit":                        TxTypeAMMDeposit,
	"AMMWithdraw":                       TxTypeAMMWithdraw,
	"AMMVote":                           TxTypeAMMVote,
	"AMMBid":                            TxTypeAMMBid,
	"AMMDelete":                         TxTypeAMMDelete,
	"XChainCreateClaimID":               TxTypeXChainCreateClaimID,
	"XChainCommit":                      TxTypeXChainCommit,
	"XChainClaim":                       TxTypeXChainClaim,
	"XChainAccountCreateCommit":         TxTypeXChainAccountCreateCommit,
	"XChainAddClaimAttestation":         TxTypeXChainAddClaimAttestation,
	"XChainAddAccountCreateAttestation": TxTypeXChainAddAccountCreateAttest,
	"XChainModifyBridge":                TxTypeXChainModifyBridge,
	"XChainCreateBridge":                TxTypeXChainCreateBridge,
	"DIDSet":                            TxTypeDIDSet,
	"DIDDelete":                         TxTypeDIDDelete,
	"OracleSet":                         TxTypeOracleSet,
	"OracleDelete":                      TxTypeOracleDelete,
	"LedgerStateFix":                    TxTypeLedgerStateFix,
	"MPTokenIssuanceCreate":             TxTypeMPTokenIssuanceCreate,
	"MPTokenIssuanceDestroy":            TxTypeMPTokenIssuanceDestroy,
	"MPTokenIssuanceSet":                TxTypeMPTokenIssuanceSet,
	"MPTokenAuthorize":                  TxTypeMPTokenAuthorize,
	"CredentialCreate":                  TxTypeCredentialCreate,
	"CredentialAccept":                  TxTypeCredentialAccept,
	"CredentialDelete":                  TxTypeCredentialDelete,
	"NFTokenModify":                     TxTypeNFTokenModify,
	"PermissionedDomainSet":             TxTypePermissionedDomainSet,
	"PermissionedDomainDelete":          TxTypePermissionedDomainDelete,
	"DelegateSet":                       TxTypeDelegateSet,
	"VaultCreate":                       TxTypeVaultCreate,
	"VaultSet":                          TxTypeVaultSet,
	"VaultDelete":                       TxTypeVaultDelete,
	"VaultDeposit":                      TxTypeVaultDeposit,
	"VaultWithdraw":                     TxTypeVaultWithdraw,
	"VaultClawback":                     TxTypeVaultClawback,
	"Batch":                             TxTypeBatch,
	"EnableAmendment":                   TxTypeAmendment,
	"SetFee":                            TxTypeFee,
	"UNLModify":                         TxTypeUNLModify,
}

// TxTypeFromName returns the transaction type for a given name.
func TxTypeFromName(name string) (TxType, bool) {
	t, ok := txTypeNameMap[name]
	return t, ok
}

// IsPseudoTransaction reports whether this is a system-generated transaction.
func (t TxType) IsPseudoTransaction() bool {
	return t == TxTypeAmendment || t == TxTypeFee || t == TxTypeUNLModify
}

// IsDeprecated reports whether this transaction type is deprecated.
func (t TxType) IsDeprecated() bool {
	return t == TxTypeNickNameSet || t == TxTypeContract || t == TxTypeSpinalTap || t == TxTypeHookSet
}
