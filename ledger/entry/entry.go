package entry

import "github.com/LeJamon/go-xrpl/protocol"

// Type represents a ledger entry type.
type Type = protocol.LedgerEntryType

// Pseudo-types used only as keylet constraints.
const (
	TypeAny   = protocol.LedgerEntryTypeAny
	TypeChild = protocol.LedgerEntryTypeChild
)

// All known ledger entry types
// Reference: rippled/include/xrpl/protocol/detail/ledger_entries.macro
const (
	// NFT/Token Objects
	TypeNFTokenOffer = protocol.LedgerEntryTypeNFTokenOffer
	TypeCheck        = protocol.LedgerEntryTypeCheck

	// Identity & UNL
	TypeDID         = protocol.LedgerEntryTypeDID
	TypeNegativeUNL = protocol.LedgerEntryTypeNegativeUNL

	// NFT Pages
	TypeNFTokenPage = protocol.LedgerEntryTypeNFTokenPage

	// Signing & Tickets
	TypeSignerList = protocol.LedgerEntryTypeSignerList
	TypeTicket     = protocol.LedgerEntryTypeTicket

	// Account & Directory
	TypeAccountRoot = protocol.LedgerEntryTypeAccountRoot
	// Deprecated: Contract ledger entries are unsupported legacy objects.
	TypeContract      = protocol.LedgerEntryTypeContract
	TypeDirectoryNode = protocol.LedgerEntryTypeDirectoryNode

	// System Singletons
	TypeAmendments = protocol.LedgerEntryTypeAmendments
	// Deprecated: GeneratorMap ledger entries are unsupported legacy objects.
	TypeGeneratorMap = protocol.LedgerEntryTypeGeneratorMap
	TypeLedgerHashes = protocol.LedgerEntryTypeLedgerHashes

	// Cross-Chain Bridge
	TypeBridge = protocol.LedgerEntryTypeBridge
	// Deprecated: Nickname ledger entries are unsupported legacy objects.
	TypeNickname = protocol.LedgerEntryTypeNickname

	// DEX & Trust
	TypeOffer          = protocol.LedgerEntryTypeOffer
	TypeDepositPreauth = protocol.LedgerEntryTypeDepositPreauth

	// Cross-Chain Claims
	TypeXChainOwnedClaimID              = protocol.LedgerEntryTypeXChainOwnedClaimID
	TypeRippleState                     = protocol.LedgerEntryTypeRippleState
	TypeFeeSettings                     = protocol.LedgerEntryTypeFeeSettings
	TypeXChainOwnedCreateAccountClaimID = protocol.LedgerEntryTypeXChainOwnedCreateAccountClaimID

	// Escrow & Payment Channels
	TypeEscrow     = protocol.LedgerEntryTypeEscrow
	TypePayChannel = protocol.LedgerEntryTypePayChannel

	// AMM
	TypeAMM = protocol.LedgerEntryTypeAMM

	// Multi-Purpose Tokens
	TypeMPTokenIssuance = protocol.LedgerEntryTypeMPTokenIssuance
	TypeMPToken         = protocol.LedgerEntryTypeMPToken

	// Oracle, Credentials, Permissions
	TypeOracle             = protocol.LedgerEntryTypeOracle
	TypeCredential         = protocol.LedgerEntryTypeCredential
	TypePermissionedDomain = protocol.LedgerEntryTypePermissionedDomain
	TypeDelegate           = protocol.LedgerEntryTypeDelegate

	// Vault
	TypeVault = protocol.LedgerEntryTypeVault

	// Lending protocol
	TypeLoanBroker = protocol.LedgerEntryTypeLoanBroker
	TypeLoan       = protocol.LedgerEntryTypeLoan

	// Sponsorship
	TypeSponsorship = protocol.LedgerEntryTypeSponsorship
)
