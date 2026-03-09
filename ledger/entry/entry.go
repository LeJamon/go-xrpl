package entry

import (
	"fmt"
)

// Type represents a ledger entry type
type Type uint16

// All known ledger entry types
// Reference: rippled/include/xrpl/protocol/detail/ledger_entries.macro
const (
	// NFT/Token Objects
	TypeNFTokenOffer Type = 0x0037 // NFT trading offers
	TypeCheck        Type = 0x0043 // Check objects

	// Identity & UNL
	TypeDID         Type = 0x0049 // Decentralized Identifiers
	TypeNegativeUNL Type = 0x004e // Negative UNL state (singleton)

	// NFT Pages
	TypeNFTokenPage Type = 0x0050 // NFT collections

	// Signing & Tickets
	TypeSignerList Type = 0x0053 // Multi-signing lists
	TypeTicket     Type = 0x0054 // Sequence tickets

	// Account & Directory
	TypeAccountRoot   Type = 0x0061 // Account objects
	TypeDirectoryNode Type = 0x0064 // Directory nodes

	// System Singletons
	TypeAmendments   Type = 0x0066 // Protocol amendments (singleton)
	TypeLedgerHashes Type = 0x0068 // Historical hashes (singleton)

	// Cross-Chain Bridge
	TypeBridge Type = 0x0069 // Sidechain bridges

	// DEX & Trust
	TypeOffer          Type = 0x006f // DEX offers
	TypeDepositPreauth Type = 0x0070 // Deposit preauthorization

	// Cross-Chain Claims
	TypeXChainOwnedClaimID              Type = 0x0071 // Cross-chain claims
	TypeRippleState                     Type = 0x0072 // Trust lines
	TypeFeeSettings                     Type = 0x0073 // Network fees (singleton)
	TypeXChainOwnedCreateAccountClaimID Type = 0x0074 // Cross-chain account creation claims

	// Escrow & Payment Channels
	TypeEscrow     Type = 0x0075 // Escrow objects
	TypePayChannel Type = 0x0078 // Payment channels

	// AMM
	TypeAMM Type = 0x0079 // Automated Market Maker pools

	// Multi-Purpose Tokens
	TypeMPTokenIssuance Type = 0x007e // MPT issuances
	TypeMPToken         Type = 0x007f // MPT holdings

	// Oracle, Credentials, Permissions
	TypeOracle            Type = 0x0080 // Price oracles
	TypeCredential        Type = 0x0081 // Verifiable credentials
	TypePermissionedDomain Type = 0x0082 // Permissioned domain objects
	TypeDelegate          Type = 0x0083 // Delegated permissions

	// Vault
	TypeVault Type = 0x0084 // Asset vaults
)

// String returns the string representation of the Type
func (t Type) String() string {
	switch t {
	case TypeNFTokenOffer:
		return "NFTokenOffer"
	case TypeCheck:
		return "Check"
	case TypeDID:
		return "DID"
	case TypeNegativeUNL:
		return "NegativeUNL"
	case TypeNFTokenPage:
		return "NFTokenPage"
	case TypeSignerList:
		return "SignerList"
	case TypeTicket:
		return "Ticket"
	case TypeAccountRoot:
		return "AccountRoot"
	case TypeDirectoryNode:
		return "DirectoryNode"
	case TypeAmendments:
		return "Amendments"
	case TypeLedgerHashes:
		return "LedgerHashes"
	case TypeBridge:
		return "Bridge"
	case TypeOffer:
		return "Offer"
	case TypeDepositPreauth:
		return "DepositPreauth"
	case TypeXChainOwnedClaimID:
		return "XChainOwnedClaimID"
	case TypeRippleState:
		return "RippleState"
	case TypeFeeSettings:
		return "FeeSettings"
	case TypeXChainOwnedCreateAccountClaimID:
		return "XChainOwnedCreateAccountClaimID"
	case TypeEscrow:
		return "Escrow"
	case TypePayChannel:
		return "PayChannel"
	case TypeAMM:
		return "AMM"
	case TypeMPTokenIssuance:
		return "MPTokenIssuance"
	case TypeMPToken:
		return "MPToken"
	case TypeOracle:
		return "Oracle"
	case TypeCredential:
		return "Credential"
	case TypePermissionedDomain:
		return "PermissionedDomain"
	case TypeDelegate:
		return "Delegate"
	case TypeVault:
		return "Vault"
	default:
		return fmt.Sprintf("Unknown(%#x)", uint16(t))
	}
}

// Entry defines the interface for all ledger entries
type Entry interface {
	Type() Type
	Validate() error
	Hash() ([32]byte, error)
}
