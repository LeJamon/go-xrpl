package tx

import "github.com/LeJamon/go-xrpl/protocol"

// Type represents a transaction type code. It aliases protocol.TxType so the
// transaction type table lives in a single leaf package shared with the
// invariants checker.
type Type = protocol.TxType

// Transaction type codes, aliased from the protocol package.
const (
	TypeInvalid = protocol.TxTypeInvalid

	TypePayment                      = protocol.TxTypePayment
	TypeEscrowCreate                 = protocol.TxTypeEscrowCreate
	TypeEscrowFinish                 = protocol.TxTypeEscrowFinish
	TypeAccountSet                   = protocol.TxTypeAccountSet
	TypeEscrowCancel                 = protocol.TxTypeEscrowCancel
	TypeRegularKeySet                = protocol.TxTypeRegularKeySet
	TypeNickNameSet                  = protocol.TxTypeNickNameSet
	TypeOfferCreate                  = protocol.TxTypeOfferCreate
	TypeOfferCancel                  = protocol.TxTypeOfferCancel
	TypeContract                     = protocol.TxTypeContract
	TypeTicketCreate                 = protocol.TxTypeTicketCreate
	TypeSpinalTap                    = protocol.TxTypeSpinalTap
	TypeSignerListSet                = protocol.TxTypeSignerListSet
	TypePaymentChannelCreate         = protocol.TxTypePaymentChannelCreate
	TypePaymentChannelFund           = protocol.TxTypePaymentChannelFund
	TypePaymentChannelClaim          = protocol.TxTypePaymentChannelClaim
	TypeCheckCreate                  = protocol.TxTypeCheckCreate
	TypeCheckCash                    = protocol.TxTypeCheckCash
	TypeCheckCancel                  = protocol.TxTypeCheckCancel
	TypeDepositPreauth               = protocol.TxTypeDepositPreauth
	TypeTrustSet                     = protocol.TxTypeTrustSet
	TypeAccountDelete                = protocol.TxTypeAccountDelete
	TypeHookSet                      = protocol.TxTypeHookSet
	TypeNFTokenMint                  = protocol.TxTypeNFTokenMint
	TypeNFTokenBurn                  = protocol.TxTypeNFTokenBurn
	TypeNFTokenCreateOffer           = protocol.TxTypeNFTokenCreateOffer
	TypeNFTokenCancelOffer           = protocol.TxTypeNFTokenCancelOffer
	TypeNFTokenAcceptOffer           = protocol.TxTypeNFTokenAcceptOffer
	TypeClawback                     = protocol.TxTypeClawback
	TypeAMMClawback                  = protocol.TxTypeAMMClawback
	TypeAMMCreate                    = protocol.TxTypeAMMCreate
	TypeAMMDeposit                   = protocol.TxTypeAMMDeposit
	TypeAMMWithdraw                  = protocol.TxTypeAMMWithdraw
	TypeAMMVote                      = protocol.TxTypeAMMVote
	TypeAMMBid                       = protocol.TxTypeAMMBid
	TypeAMMDelete                    = protocol.TxTypeAMMDelete
	TypeXChainCreateClaimID          = protocol.TxTypeXChainCreateClaimID
	TypeXChainCommit                 = protocol.TxTypeXChainCommit
	TypeXChainClaim                  = protocol.TxTypeXChainClaim
	TypeXChainAccountCreateCommit    = protocol.TxTypeXChainAccountCreateCommit
	TypeXChainAddClaimAttestation    = protocol.TxTypeXChainAddClaimAttestation
	TypeXChainAddAccountCreateAttest = protocol.TxTypeXChainAddAccountCreateAttest
	TypeXChainModifyBridge           = protocol.TxTypeXChainModifyBridge
	TypeXChainCreateBridge           = protocol.TxTypeXChainCreateBridge
	TypeDIDSet                       = protocol.TxTypeDIDSet
	TypeDIDDelete                    = protocol.TxTypeDIDDelete
	TypeOracleSet                    = protocol.TxTypeOracleSet
	TypeOracleDelete                 = protocol.TxTypeOracleDelete
	TypeLedgerStateFix               = protocol.TxTypeLedgerStateFix
	TypeMPTokenIssuanceCreate        = protocol.TxTypeMPTokenIssuanceCreate
	TypeMPTokenIssuanceDestroy       = protocol.TxTypeMPTokenIssuanceDestroy
	TypeMPTokenIssuanceSet           = protocol.TxTypeMPTokenIssuanceSet
	TypeMPTokenAuthorize             = protocol.TxTypeMPTokenAuthorize
	TypeCredentialCreate             = protocol.TxTypeCredentialCreate
	TypeCredentialAccept             = protocol.TxTypeCredentialAccept
	TypeCredentialDelete             = protocol.TxTypeCredentialDelete
	TypeNFTokenModify                = protocol.TxTypeNFTokenModify
	TypePermissionedDomainSet        = protocol.TxTypePermissionedDomainSet
	TypePermissionedDomainDelete     = protocol.TxTypePermissionedDomainDelete
	TypeDelegateSet                  = protocol.TxTypeDelegateSet
	TypeVaultCreate                  = protocol.TxTypeVaultCreate
	TypeVaultSet                     = protocol.TxTypeVaultSet
	TypeVaultDelete                  = protocol.TxTypeVaultDelete
	TypeVaultDeposit                 = protocol.TxTypeVaultDeposit
	TypeVaultWithdraw                = protocol.TxTypeVaultWithdraw
	TypeVaultClawback                = protocol.TxTypeVaultClawback
	TypeBatch                        = protocol.TxTypeBatch

	TypeAmendment = protocol.TxTypeAmendment
	TypeFee       = protocol.TxTypeFee
	TypeUNLModify = protocol.TxTypeUNLModify
)

// TypeFromName returns the transaction type for a given name.
func TypeFromName(name string) (Type, bool) {
	return protocol.TxTypeFromName(name)
}
