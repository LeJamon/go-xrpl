package keylet

import (
	"encoding/binary"

	"github.com/LeJamon/goXRPLd/internal/core/ledger/entry"
	crypto "github.com/LeJamon/goXRPLd/internal/crypto/common"
)

// Space identifiers for keylet generation
// These correspond to the LedgerNameSpace enum in rippled
const (
	spaceAccount    uint16 = 'a' // Account root
	spaceDirNode    uint16 = 'd' // Directory node
	spaceGenerator  uint16 = 'g' // Generator map (deprecated)
	spaceRippleDir  uint16 = 'r' // Trust line directory
	spaceOffer      uint16 = 'o' // Offer
	spaceOwnerDir   uint16 = 'O' // Owner directory
	spaceBookDir    uint16 = 'B' // Order book directory
	spaceSkip       uint16 = 's' // Skip list
	spaceEscrow     uint16 = 'u' // Escrow
	spaceAmendments uint16 = 'f' // Amendments (singleton)
	spaceFees       uint16 = 'e' // Fee settings (singleton)
	spaceTicket     uint16 = 'T' // Ticket
	spaceSignerList uint16 = 'S' // Signer list
	spaceCheck      uint16 = 'C' // Check
	spaceDepPreauth     uint16 = 'p' // Deposit preauthorization
	spaceDepPreauthCred uint16 = 'P' // Deposit preauthorization (credential-based)
	spaceNFTokenOff  uint16 = 'q' // NFToken offer
	spaceNFTokenPg   uint16 = 'P' // NFToken page
	spaceNFTBuyOffers  uint16 = 'h' // NFToken buy offers directory
	spaceNFTSellOffers uint16 = 'i' // NFToken sell offers directory (note: same as Bridge but different context)
	spaceAMM        uint16 = 'A' // AMM
	spaceBridge     uint16 = 'i' // XChain bridge
	spaceXCClaimID  uint16 = 'Q' // XChain claim ID
	spaceXCCreateAc uint16 = 'K' // XChain create account claim
	spaceDID        uint16 = 'I' // DID
	spaceOracle     uint16 = 'R' // Oracle
	spaceMPTIssu    uint16 = '~' // MPToken issuance
	spaceMPToken    uint16 = 't' // MPToken
	spaceCredential uint16 = 'D' // Credential
	spacePermDomain uint16 = 'b' // Permissioned domain
	spaceVault      uint16 = 'V' // Vault
)

// Keylet represents an addressable location in the ledger state.
// It combines a type identifier with a 256-bit key.
type Keylet struct {
	Type entry.Type
	Key  [32]byte
}

// indexHash computes a keylet key by hashing the space and provided data.
func indexHash(space uint16, data ...[]byte) [32]byte {
	// Prepend the space identifier as a 2-byte big-endian value
	spaceBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(spaceBytes, space)

	// Collect all inputs for hashing
	inputs := make([][]byte, 0, len(data)+1)
	inputs = append(inputs, spaceBytes)
	inputs = append(inputs, data...)

	return crypto.Sha512Half(inputs...)
}

// Account returns the keylet for an account root entry.
func Account(accountID [20]byte) Keylet {
	return Keylet{
		Type: entry.TypeAccountRoot,
		Key:  indexHash(spaceAccount, accountID[:]),
	}
}

// Fees returns the keylet for the singleton fee settings entry.
func Fees() Keylet {
	// Singleton - no additional data needed
	return Keylet{
		Type: entry.TypeFeeSettings,
		Key:  indexHash(spaceFees),
	}
}

// Amendments returns the keylet for the singleton amendments entry.
func Amendments() Keylet {
	// Singleton - no additional data needed
	return Keylet{
		Type: entry.TypeAmendments,
		Key:  indexHash(spaceAmendments),
	}
}

// LedgerHashes returns the keylet for the skip list / ledger hashes entry.
// This is the "rolling 256" skip list that tracks the most recent 256 ledger hashes.
func LedgerHashes() Keylet {
	return Keylet{
		Type: entry.TypeLedgerHashes,
		Key:  indexHash(spaceSkip),
	}
}

// LedgerHashesForSeq returns the keylet for a skip list entry that records
// every 256th ledger hash. This is updated only when (prevIndex & 0xff) == 0.
// The key is computed using (ledgerSeq >> 16) to group ledgers into chunks of 65536.
// Reference: rippled Indexes.cpp skip(LedgerIndex ledger)
func LedgerHashesForSeq(ledgerSeq uint32) Keylet {
	// Include ledgerSeq >> 16 in the hash
	seqBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(seqBytes, ledgerSeq>>16)
	return Keylet{
		Type: entry.TypeLedgerHashes,
		Key:  indexHash(spaceSkip, seqBytes),
	}
}

// Offer returns the keylet for an offer entry.
func Offer(accountID [20]byte, sequence uint32) Keylet {
	seqBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(seqBytes, sequence)
	return Keylet{
		Type: entry.TypeOffer,
		Key:  indexHash(spaceOffer, accountID[:], seqBytes),
	}
}

// OwnerDir returns the keylet for an owner directory entry.
func OwnerDir(accountID [20]byte) Keylet {
	return Keylet{
		Type: entry.TypeDirectoryNode,
		Key:  indexHash(spaceOwnerDir, accountID[:]),
	}
}

// OwnerDirPage returns the keylet for a specific page of an owner directory.
func OwnerDirPage(accountID [20]byte, page uint64) Keylet {
	rootKey := OwnerDir(accountID).Key
	if page == 0 {
		return Keylet{
			Type: entry.TypeDirectoryNode,
			Key:  rootKey,
		}
	}
	pageBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(pageBytes, page)
	return Keylet{
		Type: entry.TypeDirectoryNode,
		Key:  indexHash(spaceDirNode, rootKey[:], pageBytes),
	}
}

// Escrow returns the keylet for an escrow entry.
func Escrow(accountID [20]byte, sequence uint32) Keylet {
	seqBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(seqBytes, sequence)
	return Keylet{
		Type: entry.TypeEscrow,
		Key:  indexHash(spaceEscrow, accountID[:], seqBytes),
	}
}

// Check returns the keylet for a check entry.
func Check(accountID [20]byte, sequence uint32) Keylet {
	seqBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(seqBytes, sequence)
	return Keylet{
		Type: entry.TypeCheck,
		Key:  indexHash(spaceCheck, accountID[:], seqBytes),
	}
}

// SignerList returns the keylet for a signer list entry.
func SignerList(accountID [20]byte) Keylet {
	// Signer list uses owner page 0 as identifier
	ownerPageBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(ownerPageBytes, 0)
	return Keylet{
		Type: entry.TypeSignerList,
		Key:  indexHash(spaceSignerList, accountID[:], ownerPageBytes),
	}
}

// Ticket returns the keylet for a ticket entry.
func Ticket(accountID [20]byte, ticketSeq uint32) Keylet {
	seqBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(seqBytes, ticketSeq)
	return Keylet{
		Type: entry.TypeTicket,
		Key:  indexHash(spaceTicket, accountID[:], seqBytes),
	}
}

// DepositPreauth returns the keylet for a deposit preauthorization entry.
func DepositPreauth(owner, authorized [20]byte) Keylet {
	return Keylet{
		Type: entry.TypeDepositPreauth,
		Key:  indexHash(spaceDepPreauth, owner[:], authorized[:]),
	}
}

// CredentialPair represents an (issuer, credentialType) pair for credential-based
// deposit preauth keylet computation. Pairs must be sorted before use.
type CredentialPair struct {
	Issuer         [20]byte
	CredentialType []byte
}

// DepositPreauthCredentials returns the keylet for a credential-based deposit
// preauthorization entry. The credentials must already be sorted.
// Reference: rippled Indexes.cpp depositPreauth(owner, authCreds)
func DepositPreauthCredentials(owner [20]byte, sortedCreds []CredentialPair) Keylet {
	// For each credential pair, compute sha512Half(issuer, credType)
	data := make([][]byte, 0, 1+len(sortedCreds))
	data = append(data, owner[:])
	for _, c := range sortedCreds {
		h := crypto.Sha512Half(c.Issuer[:], c.CredentialType)
		hashBytes := make([]byte, 32)
		copy(hashBytes, h[:])
		data = append(data, hashBytes)
	}

	return Keylet{
		Type: entry.TypeDepositPreauth,
		Key:  indexHash(spaceDepPreauthCred, data...),
	}
}

// DepositPreauthByID returns a DepositPreauth keylet for a known entry key.
func DepositPreauthByID(key [32]byte) Keylet {
	return Keylet{
		Type: entry.TypeDepositPreauth,
		Key:  key,
	}
}

// Line returns the keylet for a trust line (RippleState) between two accounts.
// The currency is a 3-character code for standard currencies or a 40-character hex string.
func Line(account1, account2 [20]byte, currency string) Keylet {
	// Accounts must be sorted consistently - lower account first
	var low, high [20]byte
	if compareAccountIDs(account1, account2) < 0 {
		low, high = account1, account2
	} else {
		low, high = account2, account1
	}

	// Convert currency to 160-bit (20 byte) representation
	currencyBytes := currencyToBytes(currency)

	return Keylet{
		Type: entry.TypeRippleState,
		Key:  indexHash(spaceRippleDir, low[:], high[:], currencyBytes[:]),
	}
}

// compareAccountIDs compares two account IDs lexicographically.
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
func compareAccountIDs(a, b [20]byte) int {
	for i := 0; i < 20; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

// IsLowAccount returns true if account1 is the "low" account in a trust line.
// Trust lines store accounts in sorted order (low < high lexicographically).
func IsLowAccount(account1, account2 [20]byte) bool {
	return compareAccountIDs(account1, account2) < 0
}

// currencyToBytes converts a currency code to its 20-byte representation.
// Standard 3-character codes are zero-padded (e.g., "USD" -> 0x0000000000000000005553440000000000000000)
// Hex strings are decoded directly.
func currencyToBytes(currency string) [20]byte {
	var result [20]byte

	if len(currency) == 3 {
		// Standard currency code - ASCII in bytes 12-14
		result[12] = currency[0]
		result[13] = currency[1]
		result[14] = currency[2]
	} else if len(currency) == 40 {
		// Hex-encoded currency (non-standard)
		for i := 0; i < 20; i++ {
			result[i] = hexToByte(currency[i*2], currency[i*2+1])
		}
	}

	return result
}

// hexToByte converts two hex characters to a byte.
func hexToByte(high, low byte) byte {
	return hexNibble(high)<<4 | hexNibble(low)
}

// hexNibble converts a single hex character to its value.
func hexNibble(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	default:
		return 0
	}
}

// BookDir returns the keylet for an order book directory (base, without quality).
// The hash order follows rippled: paysCurrency, getsCurrency, paysIssuer, getsIssuer
func BookDir(takerPaysCurrency, takerPaysIssuer, takerGetsCurrency, takerGetsIssuer [20]byte) Keylet {
	return Keylet{
		Type: entry.TypeDirectoryNode,
		Key:  indexHash(spaceBookDir, takerPaysCurrency[:], takerGetsCurrency[:], takerPaysIssuer[:], takerGetsIssuer[:]),
	}
}

// Quality returns a keylet with the quality (exchange rate) encoded in the last 8 bytes.
// This is used for offer book directories where offers are sorted by quality.
// The quality is stored in big-endian format in the rightmost 8 bytes.
func Quality(k Keylet, quality uint64) Keylet {
	result := Keylet{
		Type: k.Type,
		Key:  k.Key, // Copy the key
	}
	// Encode quality in the last 8 bytes (big-endian)
	binary.BigEndian.PutUint64(result.Key[24:], quality)
	return result
}

// GetQuality extracts the quality value from the last 8 bytes of a keylet.
func GetQuality(k Keylet) uint64 {
	return binary.BigEndian.Uint64(k.Key[24:])
}

// nftPageMask is the low 96 bits (bytes 20-31) used for NFT page grouping.
var nftPageMask = [32]byte{
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
}

// NFTokenPageMin returns the minimum page key for an account.
// The key is [owner_20_bytes | 0x00_12_bytes] — NOT hashed.
// Reference: rippled Indexes.cpp nftpage_min
func NFTokenPageMin(accountID [20]byte) Keylet {
	var key [32]byte
	copy(key[:20], accountID[:])
	return Keylet{
		Type: entry.TypeNFTokenPage,
		Key:  key,
	}
}

// NFTokenPageMax returns the maximum page key for an account.
// The key is [owner_20_bytes | 0xFF_12_bytes] — NOT hashed.
// Reference: rippled Indexes.cpp nftpage_max
func NFTokenPageMax(accountID [20]byte) Keylet {
	var key [32]byte
	copy(key[:20], accountID[:])
	for i := 20; i < 32; i++ {
		key[i] = 0xFF
	}
	return Keylet{
		Type: entry.TypeNFTokenPage,
		Key:  key,
	}
}

// NFTokenPageForToken returns the page key for a specific token.
// The key is (base & ~pageMask) | (tokenID & pageMask) — NOT hashed.
// Reference: rippled Indexes.cpp nftpage(base, token)
func NFTokenPageForToken(base Keylet, tokenID [32]byte) Keylet {
	var key [32]byte
	for i := 0; i < 32; i++ {
		key[i] = (base.Key[i] & ^nftPageMask[i]) | (tokenID[i] & nftPageMask[i])
	}
	return Keylet{
		Type: entry.TypeNFTokenPage,
		Key:  key,
	}
}

// NFTokenOffer returns the keylet for an NFToken offer.
func NFTokenOffer(accountID [20]byte, sequence uint32) Keylet {
	seqBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(seqBytes, sequence)
	return Keylet{
		Type: entry.TypeNFTokenOffer,
		Key:  indexHash(spaceNFTokenOff, accountID[:], seqBytes),
	}
}

// NFTBuys returns the keylet for the buy offers directory of an NFToken.
func NFTBuys(nftokenID [32]byte) Keylet {
	return Keylet{
		Type: entry.TypeDirectoryNode,
		Key:  indexHash(spaceNFTBuyOffers, nftokenID[:]),
	}
}

// NFTSells returns the keylet for the sell offers directory of an NFToken.
func NFTSells(nftokenID [32]byte) Keylet {
	return Keylet{
		Type: entry.TypeDirectoryNode,
		Key:  indexHash(spaceNFTSellOffers, nftokenID[:]),
	}
}

// PayChannel returns the keylet for a payment channel.
func PayChannel(srcAccountID, dstAccountID [20]byte, sequence uint32) Keylet {
	seqBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(seqBytes, sequence)
	return Keylet{
		Type: entry.TypePayChannel,
		Key:  indexHash(spaceEscrow, srcAccountID[:], dstAccountID[:], seqBytes),
	}
}

// AMM returns the keylet for an AMM entry.
// The keylet is computed from the sorted asset pair (issuer + currency).
// Reference: rippled Indexes.cpp amm(Asset const& issue1, Asset const& issue2)
func AMM(issue1Issuer, issue1Currency, issue2Issuer, issue2Currency [20]byte) Keylet {
	// Sort the issues (compare issuer first, then currency)
	var minIssuer, minCurrency, maxIssuer, maxCurrency [20]byte

	cmp := compareAccountIDs(issue1Issuer, issue2Issuer)
	if cmp < 0 || (cmp == 0 && compareCurrencies(issue1Currency, issue2Currency) < 0) {
		minIssuer, minCurrency = issue1Issuer, issue1Currency
		maxIssuer, maxCurrency = issue2Issuer, issue2Currency
	} else {
		minIssuer, minCurrency = issue2Issuer, issue2Currency
		maxIssuer, maxCurrency = issue1Issuer, issue1Currency
	}

	return Keylet{
		Type: entry.TypeAMM,
		Key:  indexHash(spaceAMM, minIssuer[:], minCurrency[:], maxIssuer[:], maxCurrency[:]),
	}
}

// AMMByID returns an AMM keylet for a known AMM ID.
func AMMByID(ammID [32]byte) Keylet {
	return Keylet{
		Type: entry.TypeAMM,
		Key:  ammID,
	}
}

// compareCurrencies compares two currency codes lexicographically.
func compareCurrencies(a, b [20]byte) int {
	for i := 0; i < 20; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

// Oracle returns the keylet for an Oracle entry.
// Reference: rippled Indexes.cpp oracle(AccountID const& account, std::uint32_t const& documentID)
func Oracle(accountID [20]byte, documentID uint32) Keylet {
	docIDBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(docIDBytes, documentID)
	return Keylet{
		Type: entry.TypeOracle,
		Key:  indexHash(spaceOracle, accountID[:], docIDBytes),
	}
}

// Vault returns the keylet for a Vault entry.
// Reference: rippled Indexes.cpp vault(AccountID const& owner, std::uint32_t seq)
func Vault(ownerID [20]byte, sequence uint32) Keylet {
	seqBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(seqBytes, sequence)
	return Keylet{
		Type: entry.TypeVault,
		Key:  indexHash(spaceVault, ownerID[:], seqBytes),
	}
}

// VaultByID returns a Vault keylet for a known Vault ID.
func VaultByID(vaultID [32]byte) Keylet {
	return Keylet{
		Type: entry.TypeVault,
		Key:  vaultID,
	}
}

// DirPage returns the keylet for a specific page of a directory.
// Page 0 returns the root directory key unchanged.
// Other pages use a hash of the root key and page number.
// This works for any directory type (owner, book, etc.)
func DirPage(rootKey [32]byte, page uint64) Keylet {
	if page == 0 {
		return Keylet{
			Type: entry.TypeDirectoryNode,
			Key:  rootKey,
		}
	}
	pageBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(pageBytes, page)
	return Keylet{
		Type: entry.TypeDirectoryNode,
		Key:  indexHash(spaceDirNode, rootKey[:], pageBytes),
	}
}

// MakeMPTID creates an MPTokenIssuanceID from sequence and account.
// The ID is the sequence (big-endian 4 bytes) concatenated with the account ID (20 bytes).
// Reference: rippled Indexes.cpp makeMptID
func MakeMPTID(sequence uint32, account [20]byte) [24]byte {
	var mptID [24]byte
	// Sequence in big-endian
	seqBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(seqBytes, sequence)
	copy(mptID[:4], seqBytes)
	copy(mptID[4:], account[:])
	return mptID
}

// MPTIssuance returns the keylet for an MPToken issuance entry.
// Reference: rippled keylet::mptIssuance(MPTID const& issuanceID)
func MPTIssuance(mptID [24]byte) Keylet {
	return Keylet{
		Type: entry.TypeMPTokenIssuance,
		Key:  indexHash(spaceMPTIssu, mptID[:]),
	}
}

// MPTIssuanceBySeq returns the keylet for an MPToken issuance entry using sequence and issuer.
// Reference: rippled keylet::mptIssuance(std::uint32_t seq, AccountID const& issuer)
func MPTIssuanceBySeq(sequence uint32, issuer [20]byte) Keylet {
	return MPTIssuance(MakeMPTID(sequence, issuer))
}

// MPToken returns the keylet for an MPToken holder entry.
// The keylet is computed from the issuance key and holder account.
// Reference: rippled keylet::mptoken(uint256 const& issuanceKey, AccountID const& holder)
func MPToken(issuanceKey [32]byte, holder [20]byte) Keylet {
	return Keylet{
		Type: entry.TypeMPToken,
		Key:  indexHash(spaceMPToken, issuanceKey[:], holder[:]),
	}
}

// MPTokenByID returns the keylet for an MPToken holder entry using the MPT ID.
// Reference: rippled keylet::mptoken(MPTID const& issuanceID, AccountID const& holder)
func MPTokenByID(mptID [24]byte, holder [20]byte) Keylet {
	issuanceKey := MPTIssuance(mptID).Key
	return MPToken(issuanceKey, holder)
}

// DID returns the keylet for a DID (Decentralized Identifier) entry.
// Reference: rippled Indexes.cpp did(AccountID const& account)
func DID(accountID [20]byte) Keylet {
	return Keylet{
		Type: entry.TypeDID,
		Key:  indexHash(spaceDID, accountID[:]),
	}
}

// Credential returns the keylet for a Credential entry.
// Reference: rippled Indexes.cpp credential(AccountID const& subject, AccountID const& issuer, Slice const& credType)
// The credential keylet is computed from subject, issuer, and credential type.
func Credential(subject, issuer [20]byte, credentialType []byte) Keylet {
	return Keylet{
		Type: entry.TypeCredential,
		Key:  indexHash(spaceCredential, subject[:], issuer[:], credentialType),
	}
}

// CredentialByID returns a Credential keylet for a known Credential ID.
func CredentialByID(credentialID [32]byte) Keylet {
	return Keylet{
		Type: entry.TypeCredential,
		Key:  credentialID,
	}
}

// PermissionedDomain returns the keylet for a Permissioned Domain entry.
// Reference: rippled Indexes.cpp permissionedDomain(AccountID const& account, std::uint32_t seq)
func PermissionedDomain(accountID [20]byte, sequence uint32) Keylet {
	seqBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(seqBytes, sequence)
	return Keylet{
		Type: entry.TypePermissionedDomain,
		Key:  indexHash(spacePermDomain, accountID[:], seqBytes),
	}
}

// PermissionedDomainByID returns a PermissionedDomain keylet for a known domain ID.
func PermissionedDomainByID(domainID [32]byte) Keylet {
	return Keylet{
		Type: entry.TypePermissionedDomain,
		Key:  domainID,
	}
}
