package entry

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
)

// Type represents a ledger entry type.
type Type uint16

// Pseudo-types used only as keylet constraints.
const (
	TypeAny   Type = 0
	TypeChild Type = 0x1CD2
)

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
	TypeContract      Type = 0x0063 // Deprecated contract objects
	TypeDirectoryNode Type = 0x0064 // Directory nodes

	// System Singletons
	TypeAmendments   Type = 0x0066 // Protocol amendments (singleton)
	TypeGeneratorMap Type = 0x0067 // Deprecated generator maps
	TypeLedgerHashes Type = 0x0068 // Historical hashes (singleton)

	// Cross-Chain Bridge
	TypeBridge   Type = 0x0069 // Sidechain bridges
	TypeNickname Type = 0x006e // Deprecated nickname objects

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
	TypeOracle             Type = 0x0080 // Price oracles
	TypeCredential         Type = 0x0081 // Verifiable credentials
	TypePermissionedDomain Type = 0x0082 // Permissioned domain objects
	TypeDelegate           Type = 0x0083 // Delegated permissions

	// Vault
	TypeVault Type = 0x0084 // Asset vaults

	// Lending protocol
	TypeLoanBroker Type = 0x0088 // Loan brokers
	TypeLoan       Type = 0x0089 // Loans

	// Sponsorship
	TypeSponsorship Type = 0x0090 // Sponsor/sponsee relationship
)

// String returns the concrete ledger entry type name. Pseudo-types are unknown.
func (t Type) String() string {
	switch t {
	case TypeContract:
		return "Contract"
	case TypeGeneratorMap:
		return "GeneratorMap"
	case TypeNickname:
		return "Nickname"
	}
	name, err := definitions.Get().LedgerEntryTypeName(int32(t))
	if err != nil {
		return fmt.Sprintf("Unknown(0x%04x)", uint16(t))
	}
	return name
}
