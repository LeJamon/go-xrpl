package keylet

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sort"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// Space identifiers for keylet generation
// These correspond to the LedgerNameSpace enum in rippled
const (
	spaceAccount        uint16 = 'a' // Account root
	spaceDirNode        uint16 = 'd' // Directory node
	spaceRippleDir      uint16 = 'r' // Trust line directory
	spaceOffer          uint16 = 'o' // Offer
	spaceOwnerDir       uint16 = 'O' // Owner directory
	spaceBookDir        uint16 = 'B' // Order book directory
	spaceSkip           uint16 = 's' // Skip list
	spaceEscrow         uint16 = 'u' // Escrow
	spacePayChan        uint16 = 'x' // Payment channel
	spaceAmendments     uint16 = 'f' // Amendments (singleton)
	spaceFees           uint16 = 'e' // Fee settings (singleton)
	spaceTicket         uint16 = 'T' // Ticket
	spaceSignerList     uint16 = 'S' // Signer list
	spaceCheck          uint16 = 'C' // Check
	spaceDepPreauth     uint16 = 'p' // Deposit preauthorization
	spaceDepPreauthCred uint16 = 'P' // Deposit preauthorization (credential-based)
	spaceNFTokenOff     uint16 = 'q' // NFToken offer
	spaceNFTBuyOffers   uint16 = 'h' // NFToken buy offers directory
	spaceNFTSellOffers  uint16 = 'i' // NFToken sell offers directory
	spaceAMM            uint16 = 'A' // AMM
	spaceBridge         uint16 = 'H' // XChain bridge
	spaceXCClaimID      uint16 = 'Q' // XChain claim ID
	spaceXCCreateAc     uint16 = 'K' // XChain create account claim
	spaceDID            uint16 = 'I' // DID
	spaceOracle         uint16 = 'R' // Oracle
	spaceMPTIssu        uint16 = '~' // MPToken issuance
	spaceMPToken        uint16 = 't' // MPToken
	spaceCredential     uint16 = 'D' // Credential
	spacePermDomain     uint16 = 'm' // Permissioned domain
	spaceNegativeUNL    uint16 = 'N' // Negative UNL (singleton)
	spaceVault          uint16 = 'V' // Vault
	spaceDelegate       uint16 = 'E' // Delegate
	spaceLoanBroker     uint16 = 'l' // Loan broker (lower-case L)
	spaceLoan           uint16 = 'L' // Loan
)

// Keylet represents an addressable location in the ledger state.
// It combines a type identifier with a 256-bit key.
type Keylet struct {
	Type entry.Type
	Key  [32]byte
}

// indexHash computes a keylet key by hashing the space and provided data.
func indexHash(space uint16, data ...[]byte) [32]byte {
	var spaceBytes [2]byte
	binary.BigEndian.PutUint16(spaceBytes[:], space)

	inputs := make([][]byte, 0, len(data)+1)
	inputs = append(inputs, spaceBytes[:])
	inputs = append(inputs, data...)

	return sha512half.Sum(inputs...)
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

// NegativeUNL returns the keylet for the singleton negative UNL entry.
// Reference: rippled Indexes.cpp negativeUNL()
func NegativeUNL() Keylet {
	return Keylet{
		Type: entry.TypeNegativeUNL,
		Key:  indexHash(spaceNegativeUNL),
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
	var seqBytes [4]byte
	binary.BigEndian.PutUint32(seqBytes[:], ledgerSeq>>16)
	return Keylet{
		Type: entry.TypeLedgerHashes,
		Key:  indexHash(spaceSkip, seqBytes[:]),
	}
}

// Bridge returns the keylet for a bridge entry.
func Bridge(door [20]byte, currency [20]byte) Keylet {
	return Keylet{
		Type: entry.TypeBridge,
		Key:  indexHash(spaceBridge, door[:], currency[:]),
	}
}

// XChainBridge identifies the assets and door accounts on both sides of a bridge.
type XChainBridge struct {
	LockingDoor     [20]byte
	LockingCurrency [20]byte
	LockingIssuer   [20]byte
	IssuingDoor     [20]byte
	IssuingCurrency [20]byte
	IssuingIssuer   [20]byte
}

// XChainClaimID returns the keylet for an owned cross-chain claim entry.
func XChainClaimID(bridge XChainBridge, sequence uint64) Keylet {
	var seqBytes [8]byte
	binary.BigEndian.PutUint64(seqBytes[:], sequence)
	return Keylet{
		Type: entry.TypeXChainOwnedClaimID,
		Key: indexHash(
			spaceXCClaimID,
			bridge.LockingDoor[:],
			bridge.LockingCurrency[:],
			bridge.LockingIssuer[:],
			bridge.IssuingDoor[:],
			bridge.IssuingCurrency[:],
			bridge.IssuingIssuer[:],
			seqBytes[:],
		),
	}
}

// XChainCreateAccountClaimID returns the keylet for an owned create-account claim entry.
func XChainCreateAccountClaimID(bridge XChainBridge, sequence uint64) Keylet {
	var seqBytes [8]byte
	binary.BigEndian.PutUint64(seqBytes[:], sequence)
	return Keylet{
		Type: entry.TypeXChainOwnedCreateAccountClaimID,
		Key: indexHash(
			spaceXCCreateAc,
			bridge.LockingDoor[:],
			bridge.LockingCurrency[:],
			bridge.LockingIssuer[:],
			bridge.IssuingDoor[:],
			bridge.IssuingCurrency[:],
			bridge.IssuingIssuer[:],
			seqBytes[:],
		),
	}
}

// Offer returns the keylet for an offer entry.
func Offer(accountID [20]byte, sequence uint32) Keylet {
	var seqBytes [4]byte
	binary.BigEndian.PutUint32(seqBytes[:], sequence)
	return Keylet{
		Type: entry.TypeOffer,
		Key:  indexHash(spaceOffer, accountID[:], seqBytes[:]),
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
	return DirPage(OwnerDir(accountID).Key, page)
}

// Escrow returns the keylet for an escrow entry.
func Escrow(accountID [20]byte, sequence uint32) Keylet {
	var seqBytes [4]byte
	binary.BigEndian.PutUint32(seqBytes[:], sequence)
	return Keylet{
		Type: entry.TypeEscrow,
		Key:  indexHash(spaceEscrow, accountID[:], seqBytes[:]),
	}
}

// Check returns the keylet for a check entry.
func Check(accountID [20]byte, sequence uint32) Keylet {
	var seqBytes [4]byte
	binary.BigEndian.PutUint32(seqBytes[:], sequence)
	return Keylet{
		Type: entry.TypeCheck,
		Key:  indexHash(spaceCheck, accountID[:], seqBytes[:]),
	}
}

// SignerList returns the keylet for a signer list entry.
func SignerList(accountID [20]byte) Keylet {
	// Signer list uses owner page 0 as identifier
	var ownerPageBytes [4]byte
	return Keylet{
		Type: entry.TypeSignerList,
		Key:  indexHash(spaceSignerList, accountID[:], ownerPageBytes[:]),
	}
}

// Ticket returns the keylet for a ticket entry.
func Ticket(accountID [20]byte, ticketSeq uint32) Keylet {
	var seqBytes [4]byte
	binary.BigEndian.PutUint32(seqBytes[:], ticketSeq)
	return Keylet{
		Type: entry.TypeTicket,
		Key:  indexHash(spaceTicket, accountID[:], seqBytes[:]),
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
// deposit preauth keylet computation.
type CredentialPair struct {
	Issuer         [20]byte
	CredentialType []byte
}

// DepositPreauthCredentials returns the keylet for a credential-based deposit
// preauthorization entry. Credentials are sorted and deduplicated before hashing.
// Reference: rippled Indexes.cpp depositPreauth(owner, authCreds)
func DepositPreauthCredentials(owner [20]byte, credentials []CredentialPair) Keylet {
	sorted := append([]CredentialPair(nil), credentials...)
	sort.Slice(sorted, func(i, j int) bool {
		if cmp := bytes.Compare(sorted[i].Issuer[:], sorted[j].Issuer[:]); cmp != 0 {
			return cmp < 0
		}
		return bytes.Compare(sorted[i].CredentialType, sorted[j].CredentialType) < 0
	})

	hashes := make([][32]byte, 0, len(sorted))
	for i, c := range sorted {
		if i > 0 && c.Issuer == sorted[i-1].Issuer && bytes.Equal(c.CredentialType, sorted[i-1].CredentialType) {
			continue
		}
		hashes = append(hashes, sha512half.Sum(c.Issuer[:], c.CredentialType))
	}
	data := make([][]byte, 0, 2+len(hashes))
	data = append(data, owner[:])
	for i := range hashes {
		data = append(data, hashes[i][:])
	}
	var sizeBytes [8]byte
	binary.BigEndian.PutUint64(sizeBytes[:], uint64(len(hashes)))
	data = append(data, sizeBytes[:])

	return Keylet{
		Type: entry.TypeDepositPreauth,
		Key:  indexHash(spaceDepPreauthCred, data...),
	}
}

// Line returns the keylet for a trust line (RippleState) between two accounts.
// The currency is a 3-character code for standard currencies or a 40-character hex string.
func Line(account1, account2 [20]byte, currency string) Keylet {
	// Accounts must be sorted consistently - lower account first
	var low, high [20]byte
	if bytes.Compare(account1[:], account2[:]) < 0 {
		low, high = account1, account2
	} else {
		low, high = account2, account1
	}

	// Convert currency to 160-bit (20 byte) representation
	currencyBytes := CurrencyBytes(currency)

	return Keylet{
		Type: entry.TypeRippleState,
		Key:  indexHash(spaceRippleDir, low[:], high[:], currencyBytes[:]),
	}
}

// IsLowAccount returns true if account1 is the "low" account in a trust line.
// Trust lines store accounts in sorted order (low < high lexicographically).
func IsLowAccount(account1, account2 [20]byte) bool {
	return bytes.Compare(account1[:], account2[:]) < 0
}

// CurrencyBytes converts a currency code to its 20-byte representation,
// matching rippled's to_currency (UintTypes.cpp:84-107):
//   - "" or "XRP" → xrpCurrency() (all-zeros).
//   - 3-char ISO code whose chars all lie in isoCharSet → zero-padded ASCII
//     at offset 12-14.
//   - 3-char code with any char outside isoCharSet → noCurrency().
//   - 40-char hex → decoded bytes; malformed hex → noCurrency().
//   - Any other length → noCurrency().
//
// noCurrency is Currency(1) in rippled's base_uint<160> big-endian, i.e. a
// 20-byte value with only the trailing byte 0x01 (UintTypes.cpp:126-130).
// Distinct from xrpCurrency() so malformed input never collides with XRP.
// Callers are still expected to validate upstream.
func CurrencyBytes(currency string) [20]byte {
	var result [20]byte

	if currency == "" || currency == "XRP" {
		return result
	}

	switch len(currency) {
	case 3:
		for i := range 3 {
			if !isISOCurrencyChar(currency[i]) {
				return noCurrency
			}
		}
		result[12] = currency[0]
		result[13] = currency[1]
		result[14] = currency[2]
	case 40:
		if _, err := hex.Decode(result[:], []byte(currency)); err != nil {
			return noCurrency
		}
	default:
		return noCurrency
	}

	return result
}

// IsValidCurrencyCode reports whether code is a well-formed currency code per
// rippled's to_currency (UintTypes.cpp:83-107): empty or "XRP" (native), a
// 3-character code drawn entirely from isoCharSet, or 40 hex digits. It checks
// form only — the reserved-value codes NoCurrency and BadCurrency are
// well-formed and report true here; use ParseCurrency to reject those.
func IsValidCurrencyCode(code string) bool {
	if code == "" || code == "XRP" {
		return true
	}
	switch len(code) {
	case 3:
		for i := range len(code) {
			if !isISOCurrencyChar(code[i]) {
				return false
			}
		}
		return true
	case 40:
		_, err := hex.DecodeString(code)
		return err == nil
	default:
		return false
	}
}

// Currency-parsing errors. Callers distinguishing a malformed code from a
// reserved one can match these with errors.Is.
var (
	// ErrInvalidCurrency reports a code that is not well-formed per to_currency.
	ErrInvalidCurrency = errors.New("invalid currency code")
	// ErrReservedCurrency reports a well-formed code that resolves to one of the
	// reserved sentinels (NoCurrency or BadCurrency).
	ErrReservedCurrency = errors.New("reserved currency code")
)

// ParseCurrency validates code against rippled's to_currency rules and returns
// the 20-byte currency. It errors on malformed codes and on the reserved
// sentinels NoCurrency and BadCurrency, giving callers a single
// validate-and-encode entry point that stays symmetric with CurrencyBytes.
func ParseCurrency(code string) ([20]byte, error) {
	if !IsValidCurrencyCode(code) {
		return [20]byte{}, ErrInvalidCurrency
	}
	currency := CurrencyBytes(code)
	if currency == noCurrency || currency == badCurrency {
		return [20]byte{}, ErrReservedCurrency
	}
	return currency, nil
}

// isISOCurrencyChar reports whether c is in rippled's isoCharSet
// (UintTypes.cpp:39-43): ASCII letters, digits, and "<>(){}[]|?!@#$%^&*".
func isISOCurrencyChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	case c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '<', '>', '(', ')', '{', '}', '[', ']', '|', '?', '!', '@', '#', '$', '%', '^', '&', '*':
		return true
	}
	return false
}

// noCurrency mirrors rippled's noCurrency() sentinel (UintTypes.cpp:126-130) —
// base_uint<160>{1} stored big-endian, distinct from xrpCurrency() = all-zeros.
// to_currency yields it for any malformed code.
var noCurrency = [20]byte{19: 0x01}

// badCurrency mirrors rippled's badCurrency() sentinel (UintTypes.cpp:133-137) —
// Currency(0x5852500000000000), the ISO-style spelling of the reserved system
// code "XRP" packed at bytes 12-14.
var badCurrency = [20]byte{12: 'X', 13: 'R', 14: 'P'}

// NoCurrency returns the reserved sentinel that to_currency yields for any
// malformed code. It is returned by value so callers cannot mutate the shared
// process-wide sentinel.
func NoCurrency() [20]byte { return noCurrency }

// BadCurrency returns the reserved sentinel for the ISO-style spelling of the
// system code "XRP". It is returned by value so callers cannot mutate the
// shared process-wide sentinel.
func BadCurrency() [20]byte { return badCurrency }

// BookDir returns the keylet for an order book directory (base, without quality).
// The hash order follows rippled: paysCurrency, getsCurrency, paysIssuer, getsIssuer
func BookDir(takerPaysCurrency, takerPaysIssuer, takerGetsCurrency, takerGetsIssuer [20]byte) Keylet {
	return Keylet{
		Type: entry.TypeDirectoryNode,
		Key:  indexHash(spaceBookDir, takerPaysCurrency[:], takerGetsCurrency[:], takerPaysIssuer[:], takerGetsIssuer[:]),
	}
}

// BookDirWithDomain returns the keylet for a permissioned domain order book directory.
// Domain offers are stored in a separate directory that includes the domain ID in the hash.
// Reference: rippled Indexes.cpp getBookBase() with book.domain set
func BookDirWithDomain(takerPaysCurrency, takerPaysIssuer, takerGetsCurrency, takerGetsIssuer [20]byte, domainID [32]byte) Keylet {
	return Keylet{
		Type: entry.TypeDirectoryNode,
		Key:  indexHash(spaceBookDir, takerPaysCurrency[:], takerGetsCurrency[:], takerPaysIssuer[:], takerGetsIssuer[:], domainID[:]),
	}
}

// BookSide identifies one side of an order book: either an Issue (a currency
// with an issuer — XRP being the zero currency and zero issuer) or an MPT (its
// 192-bit MPTokenIssuanceID). Mirrors rippled's Asset = std::variant<Issue,
// MPTIssue>. Construct with IssueSide / MPTSide.
type BookSide struct {
	Currency [20]byte
	Issuer   [20]byte
	MPTID    [24]byte
	IsMPT    bool
}

// IssueSide builds an Issue book side (currency + issuer).
func IssueSide(currency, issuer [20]byte) BookSide {
	return BookSide{Currency: currency, Issuer: issuer}
}

// MPTSide builds an MPT book side from a 192-bit MPTokenIssuanceID.
func MPTSide(mptID [24]byte) BookSide {
	return BookSide{MPTID: mptID, IsMPT: true}
}

// BookBase returns the base keylet (quality 0) for an order book whose pays and
// gets sides may each be an Issue or an MPT, mirroring rippled getBookBase
// (Indexes.cpp). The hashed field layout depends on each side's kind: the two
// "asset" fields come first (a side's currency if it is an Issue, else its MPT
// id, which already embeds the issuer), followed by the issuer of any Issue
// side (MPT sides contribute no separate issuer). An optional domain id is
// appended for permissioned-domain books. For two Issue sides the layout is
// (paysCurrency, getsCurrency, paysIssuer, getsIssuer) — byte-identical to
// BookDir, so existing books keep their keys.
func BookBase(pays, gets BookSide, domainID *[32]byte) Keylet {
	data := make([][]byte, 0, 5)
	if pays.IsMPT {
		data = append(data, pays.MPTID[:])
	} else {
		data = append(data, pays.Currency[:])
	}
	if gets.IsMPT {
		data = append(data, gets.MPTID[:])
	} else {
		data = append(data, gets.Currency[:])
	}
	if !pays.IsMPT {
		data = append(data, pays.Issuer[:])
	}
	if !gets.IsMPT {
		data = append(data, gets.Issuer[:])
	}
	if domainID != nil {
		data = append(data, domainID[:])
	}
	return Quality(Keylet{
		Type: entry.TypeDirectoryNode,
		Key:  indexHash(spaceBookDir, data...),
	}, 0)
}

// Quality returns a keylet with the quality (exchange rate) encoded in the last 8 bytes.
// This is used for offer book directories where offers are sorted by quality.
// The quality is stored in big-endian format in the rightmost 8 bytes.
func Quality(k Keylet, quality uint64) Keylet {
	result := Keylet{
		Type: k.Type,
		Key:  k.Key,
	}
	// Encode quality in the last 8 bytes (big-endian)
	binary.BigEndian.PutUint64(result.Key[24:], quality)
	return result
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
	for i := range 32 {
		key[i] = (base.Key[i] & ^nftPageMask[i]) | (tokenID[i] & nftPageMask[i])
	}
	return Keylet{
		Type: entry.TypeNFTokenPage,
		Key:  key,
	}
}

// NFTokenOffer returns the keylet for an NFToken offer.
func NFTokenOffer(accountID [20]byte, sequence uint32) Keylet {
	var seqBytes [4]byte
	binary.BigEndian.PutUint32(seqBytes[:], sequence)
	return Keylet{
		Type: entry.TypeNFTokenOffer,
		Key:  indexHash(spaceNFTokenOff, accountID[:], seqBytes[:]),
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
	var seqBytes [4]byte
	binary.BigEndian.PutUint32(seqBytes[:], sequence)
	return Keylet{
		Type: entry.TypePayChannel,
		Key:  indexHash(spacePayChan, srcAccountID[:], dstAccountID[:], seqBytes[:]),
	}
}

// AMM returns the keylet for an AMM entry.
// Sort key mirrors rippled's Issue::operator<=>
// (rippled/include/xrpl/protocol/Issue.h:99-108): compare currency; on a
// currency tie, return equivalent if the currency is XRP, otherwise compare
// account. Hash input order is (account, currency, account, currency) per
// std::minmax feeding indexHash in
// rippled/src/libxrpl/protocol/Indexes.cpp:446-456 amm().
func AMM(issue1Issuer, issue1Currency, issue2Issuer, issue2Currency [20]byte) Keylet {
	return AMMAsset(
		IssueSide(issue1Currency, issue1Issuer),
		IssueSide(issue2Currency, issue2Issuer),
	)
}

// AMMAsset returns the AMM keylet for two Issue or MPT assets. Asset ordering
// matches rippled's Asset comparison: MPT assets sort before Issue assets;
// assets of the same kind retain their native Issue/MPT ordering.
func AMMAsset(asset1, asset2 BookSide) Keylet {
	minAsset, maxAsset := asset1, asset2
	if !bookSideLessEqual(asset1, asset2) {
		minAsset, maxAsset = asset2, asset1
	}

	var key [32]byte
	switch {
	case minAsset.IsMPT && maxAsset.IsMPT:
		key = indexHash(spaceAMM, minAsset.MPTID[:], maxAsset.MPTID[:])
	case minAsset.IsMPT:
		key = indexHash(spaceAMM, minAsset.MPTID[:], maxAsset.Issuer[:], maxAsset.Currency[:])
	default:
		key = indexHash(
			spaceAMM,
			minAsset.Issuer[:], minAsset.Currency[:],
			maxAsset.Issuer[:], maxAsset.Currency[:],
		)
	}

	return Keylet{Type: entry.TypeAMM, Key: key}
}

func bookSideLessEqual(lhs, rhs BookSide) bool {
	if lhs.IsMPT != rhs.IsMPT {
		return lhs.IsMPT
	}
	if lhs.IsMPT {
		return bytes.Compare(lhs.MPTID[:], rhs.MPTID[:]) <= 0
	}
	return issue1LessEqualIssue2(lhs.Currency, lhs.Issuer, rhs.Currency, rhs.Issuer)
}

// IssueLessEqual reports whether issue1 sorts at-or-before issue2 under
// rippled's Issue::operator<=> (Issue.h): compare the 20-byte currency, then —
// for non-XRP currencies — the 20-byte issuer AccountID. An XRP currency tie is
// equivalent (accounts are not compared), so the result is true.
func IssueLessEqual(currency1, issuer1, currency2, issuer2 [20]byte) bool {
	return issue1LessEqualIssue2(currency1, issuer1, currency2, issuer2)
}

// issue1LessEqualIssue2 reports whether issue1 sorts at-or-before issue2 under
// rippled's Issue::operator<=>. A true result preserves the original argument
// order through std::minmax — including the equivalent case (currency tie on
// XRP, or full Issue equality) where rippled returns
// std::weak_ordering::equivalent and std::minmax keeps (a, b).
func issue1LessEqualIssue2(currency1, issuer1, currency2, issuer2 [20]byte) bool {
	if c := bytes.Compare(currency1[:], currency2[:]); c != 0 {
		return c < 0
	}
	if currency1 == ([20]byte{}) {
		// Both currencies are XRP — Issue.h:104 returns equivalent without
		// comparing accounts. std::minmax then keeps original order.
		return true
	}
	return bytes.Compare(issuer1[:], issuer2[:]) <= 0
}

// AMMByID returns an AMM keylet for a known AMM ID.
func AMMByID(ammID [32]byte) Keylet {
	return Keylet{
		Type: entry.TypeAMM,
		Key:  ammID,
	}
}

// VaultByID returns a Vault keylet for a known Vault ID.
func VaultByID(vaultID [32]byte) Keylet {
	return Keylet{
		Type: entry.TypeVault,
		Key:  vaultID,
	}
}

// Oracle returns the keylet for an Oracle entry.
// Reference: rippled Indexes.cpp oracle(AccountID const& account, std::uint32_t const& documentID)
func Oracle(accountID [20]byte, documentID uint32) Keylet {
	var docIDBytes [4]byte
	binary.BigEndian.PutUint32(docIDBytes[:], documentID)
	return Keylet{
		Type: entry.TypeOracle,
		Key:  indexHash(spaceOracle, accountID[:], docIDBytes[:]),
	}
}

// Vault returns the keylet for a Vault entry.
// Reference: rippled Indexes.cpp vault(AccountID const& owner, std::uint32_t seq)
func Vault(ownerID [20]byte, sequence uint32) Keylet {
	var seqBytes [4]byte
	binary.BigEndian.PutUint32(seqBytes[:], sequence)
	return Keylet{
		Type: entry.TypeVault,
		Key:  indexHash(spaceVault, ownerID[:], seqBytes[:]),
	}
}

// LoanBroker returns the keylet for a LoanBroker entry.
// Reference: rippled Indexes.cpp loanbroker(AccountID const& owner, std::uint32_t seq)
func LoanBroker(ownerID [20]byte, sequence uint32) Keylet {
	var seqBytes [4]byte
	binary.BigEndian.PutUint32(seqBytes[:], sequence)
	return Keylet{
		Type: entry.TypeLoanBroker,
		Key:  indexHash(spaceLoanBroker, ownerID[:], seqBytes[:]),
	}
}

// LoanBrokerByID returns a LoanBroker keylet for a known LoanBroker ID.
func LoanBrokerByID(brokerID [32]byte) Keylet {
	return Keylet{
		Type: entry.TypeLoanBroker,
		Key:  brokerID,
	}
}

// LoanByID returns a Loan keylet for a known Loan ID.
func LoanByID(loanID [32]byte) Keylet {
	return Keylet{
		Type: entry.TypeLoan,
		Key:  loanID,
	}
}

// Loan returns the keylet for a Loan entry.
// Reference: rippled Indexes.cpp loan(uint256 const& loanBrokerID, std::uint32_t loanSeq)
func Loan(loanBrokerID [32]byte, loanSeq uint32) Keylet {
	var seqBytes [4]byte
	binary.BigEndian.PutUint32(seqBytes[:], loanSeq)
	return Keylet{
		Type: entry.TypeLoan,
		Key:  indexHash(spaceLoan, loanBrokerID[:], seqBytes[:]),
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
	var pageBytes [8]byte
	binary.BigEndian.PutUint64(pageBytes[:], page)
	return Keylet{
		Type: entry.TypeDirectoryNode,
		Key:  indexHash(spaceDirNode, rootKey[:], pageBytes[:]),
	}
}

// MakeMPTID creates an MPTokenIssuanceID from sequence and account.
// The ID is the sequence (big-endian 4 bytes) concatenated with the account ID (20 bytes).
// Reference: rippled Indexes.cpp makeMptID
func MakeMPTID(sequence uint32, account [20]byte) [24]byte {
	var mptID [24]byte
	binary.BigEndian.PutUint32(mptID[:4], sequence)
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
	var seqBytes [4]byte
	binary.BigEndian.PutUint32(seqBytes[:], sequence)
	return Keylet{
		Type: entry.TypePermissionedDomain,
		Key:  indexHash(spacePermDomain, accountID[:], seqBytes[:]),
	}
}

// PermissionedDomainByID returns a PermissionedDomain keylet for a known domain ID.
func PermissionedDomainByID(domainID [32]byte) Keylet {
	return Keylet{
		Type: entry.TypePermissionedDomain,
		Key:  domainID,
	}
}

// Delegate returns the keylet for a delegation entry.
// The key is computed from the account that grants and the account that receives the delegation.
// Reference: rippled Indexes.cpp delegate(account, authorizedAccount) — LedgerNameSpace::DELEGATE = 'E'
func Delegate(account, authorizedAccount [20]byte) Keylet {
	return Keylet{
		Type: entry.TypeDelegate,
		Key:  indexHash(spaceDelegate, account[:], authorizedAccount[:]),
	}
}
