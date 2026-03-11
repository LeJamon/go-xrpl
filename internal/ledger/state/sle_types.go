package state

import (
	"encoding/hex"
	"strings"
)

// SLEAccountRoot represents an AccountRoot ledger entry with change tracking
type SLEAccountRoot struct {
	*SLEBase
}

// NewSLEAccountRoot creates a new AccountRoot SLE
func NewSLEAccountRoot(ledgerIndex [32]byte) *SLEAccountRoot {
	sle := &SLEAccountRoot{
		SLEBase: NewSLEBase(ledgerIndex, "AccountRoot"),
	}
	// Set field metadata according to rippled's behavior
	// Default: sMD_ChangeOrig | sMD_ChangeNew | sMD_DeleteFinal | sMD_Create
	sle.SetFieldMeta("Account", FieldMetaDefault)
	sle.SetFieldMeta("Balance", FieldMetaDefault)
	sle.SetFieldMeta("Flags", FieldMetaDefault)
	sle.SetFieldMeta("OwnerCount", FieldMetaDefault)
	sle.SetFieldMeta("Sequence", FieldMetaDefault)
	sle.SetFieldMeta("Domain", FieldMetaDefault)
	sle.SetFieldMeta("EmailHash", FieldMetaDefault)
	sle.SetFieldMeta("MessageKey", FieldMetaDefault)
	sle.SetFieldMeta("TransferRate", FieldMetaDefault)
	sle.SetFieldMeta("TickSize", FieldMetaDefault)
	sle.SetFieldMeta("RegularKey", FieldMetaDefault)
	sle.SetFieldMeta("AccountTxnID", FieldMetaDefault)
	sle.SetFieldMeta("AMMID", FieldMetaDefault)
	sle.SetFieldMeta("FirstNFTokenSequence", FieldMetaDefault)
	// PreviousTxnID and PreviousTxnLgrSeq only in DeleteFinal
	sle.SetFieldMeta("PreviousTxnID", FieldMetaDeleteFinal)
	sle.SetFieldMeta("PreviousTxnLgrSeq", FieldMetaDeleteFinal)
	return sle
}

// LoadFromAccountRoot loads original values from an AccountRoot struct
func (s *SLEAccountRoot) LoadFromAccountRoot(account *AccountRoot) {
	s.SetOriginal("Account", account.Account)
	s.SetOriginal("Balance", formatUint64(account.Balance))
	s.SetOriginal("Flags", account.Flags)
	s.SetOriginal("OwnerCount", account.OwnerCount)
	s.SetOriginal("Sequence", account.Sequence)
	if account.Domain != "" {
		s.SetOriginal("Domain", account.Domain)
	}
	if account.EmailHash != "" {
		s.SetOriginal("EmailHash", account.EmailHash)
	}
	if account.MessageKey != "" {
		s.SetOriginal("MessageKey", account.MessageKey)
	}
	if account.TransferRate != 0 {
		s.SetOriginal("TransferRate", account.TransferRate)
	}
	if account.TickSize != 0 {
		s.SetOriginal("TickSize", account.TickSize)
	}
	if account.RegularKey != "" {
		s.SetOriginal("RegularKey", account.RegularKey)
	}
	if account.HasFirstNFTSeq {
		s.SetOriginal("FirstNFTokenSequence", account.FirstNFTokenSequence)
	}
	var zeroHash [32]byte
	if account.AccountTxnID != zeroHash {
		s.SetOriginal("AccountTxnID", strings.ToUpper(hex.EncodeToString(account.AccountTxnID[:])))
	}
	if account.PreviousTxnID != zeroHash {
		s.SetOriginal("PreviousTxnID", strings.ToUpper(hex.EncodeToString(account.PreviousTxnID[:])))
	}
	if account.PreviousTxnLgrSeq != 0 {
		s.SetOriginal("PreviousTxnLgrSeq", account.PreviousTxnLgrSeq)
	}
}

// UpdateBalance updates the balance and tracks the change
func (s *SLEAccountRoot) UpdateBalance(newBalance uint64) {
	s.SetField("Balance", formatUint64(newBalance))
}

// UpdateOwnerCount updates the owner count and tracks the change
func (s *SLEAccountRoot) UpdateOwnerCount(newCount uint32) {
	s.SetField("OwnerCount", newCount)
}

// UpdateSequence updates the sequence and tracks the change
func (s *SLEAccountRoot) UpdateSequence(newSeq uint32) {
	s.SetField("Sequence", newSeq)
}

// UpdateFlags updates the flags and tracks the change
func (s *SLEAccountRoot) UpdateFlags(newFlags uint32) {
	s.SetField("Flags", newFlags)
}

// SLEOffer represents an Offer ledger entry with change tracking
type SLEOffer struct {
	*SLEBase
}

// NewSLEOffer creates a new Offer SLE
func NewSLEOffer(ledgerIndex [32]byte) *SLEOffer {
	sle := &SLEOffer{
		SLEBase: NewSLEBase(ledgerIndex, "Offer"),
	}
	// Set field metadata
	sle.SetFieldMeta("Account", FieldMetaDefault)
	sle.SetFieldMeta("Sequence", FieldMetaDefault)
	sle.SetFieldMeta("TakerPays", FieldMetaDefault)
	sle.SetFieldMeta("TakerGets", FieldMetaDefault)
	sle.SetFieldMeta("BookDirectory", FieldMetaDefault)
	sle.SetFieldMeta("BookNode", FieldMetaDefault)
	sle.SetFieldMeta("OwnerNode", FieldMetaDefault)
	sle.SetFieldMeta("Expiration", FieldMetaDefault)
	sle.SetFieldMeta("Flags", FieldMetaDefault)
	sle.SetFieldMeta("PreviousTxnID", FieldMetaDeleteFinal)
	sle.SetFieldMeta("PreviousTxnLgrSeq", FieldMetaDeleteFinal)
	return sle
}

// LoadFromLedgerOffer loads values from a LedgerOffer struct into both original and current.
// This is used when loading an existing offer from the ledger for modification or deletion.
// rippled tracks both origNode and curNode - origNode is the original state, curNode is the
// tracked state that may be modified. For FinalFields in metadata, curNode is used.
func (s *SLEOffer) LoadFromLedgerOffer(offer *LedgerOffer) {
	// Set both original and current to the same values initially
	// Current may be modified before the node is deleted
	s.SetOriginal("Account", offer.Account)
	s.SetField("Account", offer.Account)

	s.SetOriginal("Sequence", offer.Sequence)
	s.SetField("Sequence", offer.Sequence)

	takerPays := flattenAmount(offer.TakerPays)
	s.SetOriginal("TakerPays", takerPays)
	s.SetField("TakerPays", takerPays)

	takerGets := flattenAmount(offer.TakerGets)
	s.SetOriginal("TakerGets", takerGets)
	s.SetField("TakerGets", takerGets)

	bookDir := strings.ToUpper(hex.EncodeToString(offer.BookDirectory[:]))
	s.SetOriginal("BookDirectory", bookDir)
	s.SetField("BookDirectory", bookDir)

	// BookNode and OwnerNode use 16-char uppercase hex format with leading zeros
	bookNode := formatUint64HexPadded(offer.BookNode)
	s.SetOriginal("BookNode", bookNode)
	s.SetField("BookNode", bookNode)

	ownerNode := formatUint64HexPadded(offer.OwnerNode)
	s.SetOriginal("OwnerNode", ownerNode)
	s.SetField("OwnerNode", ownerNode)

	if offer.Expiration != 0 {
		s.SetOriginal("Expiration", offer.Expiration)
		s.SetField("Expiration", offer.Expiration)
	}

	s.SetOriginal("Flags", offer.Flags)
	s.SetField("Flags", offer.Flags)

	var zeroHash [32]byte
	if offer.PreviousTxnID != zeroHash {
		prevTxnID := strings.ToUpper(hex.EncodeToString(offer.PreviousTxnID[:]))
		s.SetOriginal("PreviousTxnID", prevTxnID)
		s.SetField("PreviousTxnID", prevTxnID)
	}
	if offer.PreviousTxnLgrSeq != 0 {
		s.SetOriginal("PreviousTxnLgrSeq", offer.PreviousTxnLgrSeq)
		s.SetField("PreviousTxnLgrSeq", offer.PreviousTxnLgrSeq)
	}
}

// SetNewOffer sets all fields for a newly created offer
func (s *SLEOffer) SetNewOffer(offer *LedgerOffer) {
	s.MarkAsCreated()
	s.SetField("Account", offer.Account)
	s.SetField("Sequence", offer.Sequence)
	s.SetField("TakerPays", flattenAmount(offer.TakerPays))
	s.SetField("TakerGets", flattenAmount(offer.TakerGets))
	s.SetField("BookDirectory", strings.ToUpper(hex.EncodeToString(offer.BookDirectory[:])))
	// Only include BookNode and OwnerNode if non-zero (rippled omits them when 0)
	// Use zero-padded 16-char hex format for these fields
	if offer.BookNode != 0 {
		s.SetField("BookNode", formatUint64HexPadded(offer.BookNode))
	}
	if offer.OwnerNode != 0 {
		s.SetField("OwnerNode", formatUint64HexPadded(offer.OwnerNode))
	}
	if offer.Expiration != 0 {
		s.SetField("Expiration", offer.Expiration)
	}
	if offer.Flags != 0 {
		s.SetField("Flags", offer.Flags)
	}
}

// UpdateTakerPays updates TakerPays and tracks the change
func (s *SLEOffer) UpdateTakerPays(amount Amount) {
	s.SetField("TakerPays", flattenAmount(amount))
}

// UpdateTakerGets updates TakerGets and tracks the change
func (s *SLEOffer) UpdateTakerGets(amount Amount) {
	s.SetField("TakerGets", flattenAmount(amount))
}

// SLEDirectoryNode represents a DirectoryNode ledger entry with change tracking
type SLEDirectoryNode struct {
	*SLEBase
	IsOwnerDirectory bool // true for owner directories, false for book directories
}

// NewSLEDirectoryNode creates a new DirectoryNode SLE
func NewSLEDirectoryNode(ledgerIndex [32]byte) *SLEDirectoryNode {
	sle := &SLEDirectoryNode{
		SLEBase: NewSLEBase(ledgerIndex, "DirectoryNode"),
	}
	// RootIndex always appears in metadata
	sle.SetFieldMeta("RootIndex", FieldMetaAlways)
	// Owner for owner directories
	sle.SetFieldMeta("Owner", FieldMetaAlways)
	sle.SetFieldMeta("Flags", FieldMetaAlways)
	// Book directory specific fields
	sle.SetFieldMeta("ExchangeRate", FieldMetaDefault)
	sle.SetFieldMeta("TakerPaysCurrency", FieldMetaDefault)
	sle.SetFieldMeta("TakerPaysIssuer", FieldMetaDefault)
	sle.SetFieldMeta("TakerGetsCurrency", FieldMetaDefault)
	sle.SetFieldMeta("TakerGetsIssuer", FieldMetaDefault)
	// Directory page fields - Indexes is NOT included in metadata
	// (rippled doesn't include Indexes changes in metadata to save space)
	sle.SetFieldMeta("Indexes", FieldMetaNever)
	sle.SetFieldMeta("IndexNext", FieldMetaDefault)
	sle.SetFieldMeta("IndexPrevious", FieldMetaDefault)
	sle.SetFieldMeta("NFTokenID", FieldMetaDefault)
	sle.SetFieldMeta("DomainID", FieldMetaDefault)
	sle.SetFieldMeta("PreviousTxnID", FieldMetaDeleteFinal)
	sle.SetFieldMeta("PreviousTxnLgrSeq", FieldMetaDeleteFinal)
	return sle
}

// SetAsOwnerDirectory configures this as an owner directory
func (s *SLEDirectoryNode) SetAsOwnerDirectory(owner string, rootIndex [32]byte) {
	s.IsOwnerDirectory = true
	s.SetField("Owner", owner)
	s.SetField("RootIndex", strings.ToUpper(hex.EncodeToString(rootIndex[:])))
	s.SetField("Flags", uint32(0))
}

// SetAsBookDirectory configures this as a book directory
func (s *SLEDirectoryNode) SetAsBookDirectory(
	rootIndex [32]byte,
	exchangeRate uint64,
	takerPaysCurrency [20]byte,
	takerPaysIssuer [20]byte,
	takerGetsCurrency [20]byte,
	takerGetsIssuer [20]byte,
) {
	s.IsOwnerDirectory = false
	s.SetField("RootIndex", strings.ToUpper(hex.EncodeToString(rootIndex[:])))
	s.SetField("ExchangeRate", strings.ToUpper(formatUint64Hex(exchangeRate)))
	// For CreatedNode NewFields, rippled omits zero (default) currency/issuer fields
	// Only set non-zero values. All-zeros means XRP which is the default.
	var zeroCurrency [20]byte
	if takerPaysCurrency != zeroCurrency {
		s.SetField("TakerPaysCurrency", strings.ToUpper(hex.EncodeToString(takerPaysCurrency[:])))
	}
	if takerPaysIssuer != zeroCurrency {
		s.SetField("TakerPaysIssuer", strings.ToUpper(hex.EncodeToString(takerPaysIssuer[:])))
	}
	if takerGetsCurrency != zeroCurrency {
		s.SetField("TakerGetsCurrency", strings.ToUpper(hex.EncodeToString(takerGetsCurrency[:])))
	}
	if takerGetsIssuer != zeroCurrency {
		s.SetField("TakerGetsIssuer", strings.ToUpper(hex.EncodeToString(takerGetsIssuer[:])))
	}
}

// LoadFromDirectoryNode loads values from a DirectoryNode struct into both original and current.
// This is used when loading an existing directory from the ledger for modification or deletion.
func (s *SLEDirectoryNode) LoadFromDirectoryNode(dir *DirectoryNode) {
	rootIndex := strings.ToUpper(hex.EncodeToString(dir.RootIndex[:]))
	s.SetOriginal("RootIndex", rootIndex)
	s.SetField("RootIndex", rootIndex)

	s.SetOriginal("Flags", dir.Flags)
	s.SetField("Flags", dir.Flags)

	if dir.Owner != [20]byte{} {
		s.IsOwnerDirectory = true
		ownerAddr, _ := EncodeAccountID(dir.Owner)
		s.SetOriginal("Owner", ownerAddr)
		s.SetField("Owner", ownerAddr)
	}

	// Check if this is a book directory (has any book fields set)
	var zeroCurrency [20]byte
	hasBookFields := dir.ExchangeRate != 0 ||
		dir.TakerPaysCurrency != zeroCurrency || dir.TakerPaysIssuer != zeroCurrency ||
		dir.TakerGetsCurrency != zeroCurrency || dir.TakerGetsIssuer != zeroCurrency

	if hasBookFields {
		s.IsOwnerDirectory = false
		// For book directories, include ALL four currency/issuer fields (even zeros)
		// and ExchangeRate if set. Use uppercase hex format.
		if dir.ExchangeRate != 0 {
			exchRate := strings.ToUpper(formatUint64Hex(dir.ExchangeRate))
			s.SetOriginal("ExchangeRate", exchRate)
			s.SetField("ExchangeRate", exchRate)
		}
		takerPaysCurr := strings.ToUpper(hex.EncodeToString(dir.TakerPaysCurrency[:]))
		s.SetOriginal("TakerPaysCurrency", takerPaysCurr)
		s.SetField("TakerPaysCurrency", takerPaysCurr)

		takerPaysIss := strings.ToUpper(hex.EncodeToString(dir.TakerPaysIssuer[:]))
		s.SetOriginal("TakerPaysIssuer", takerPaysIss)
		s.SetField("TakerPaysIssuer", takerPaysIss)

		takerGetsCurr := strings.ToUpper(hex.EncodeToString(dir.TakerGetsCurrency[:]))
		s.SetOriginal("TakerGetsCurrency", takerGetsCurr)
		s.SetField("TakerGetsCurrency", takerGetsCurr)

		takerGetsIss := strings.ToUpper(hex.EncodeToString(dir.TakerGetsIssuer[:]))
		s.SetOriginal("TakerGetsIssuer", takerGetsIss)
		s.SetField("TakerGetsIssuer", takerGetsIss)
	}

	if len(dir.Indexes) > 0 {
		indexes := make([]string, len(dir.Indexes))
		for i, idx := range dir.Indexes {
			indexes[i] = strings.ToUpper(hex.EncodeToString(idx[:]))
		}
		s.SetOriginal("Indexes", indexes)
		s.SetField("Indexes", indexes)
	}

	if dir.IndexNext != 0 {
		idxNext := formatUint64Hex(dir.IndexNext)
		s.SetOriginal("IndexNext", idxNext)
		s.SetField("IndexNext", idxNext)
	}
	if dir.IndexPrevious != 0 {
		idxPrev := formatUint64Hex(dir.IndexPrevious)
		s.SetOriginal("IndexPrevious", idxPrev)
		s.SetField("IndexPrevious", idxPrev)
	}

	var zeroHash [32]byte
	if dir.NFTokenID != zeroHash {
		nftID := strings.ToUpper(hex.EncodeToString(dir.NFTokenID[:]))
		s.SetOriginal("NFTokenID", nftID)
		s.SetField("NFTokenID", nftID)
	}
	if dir.DomainID != zeroHash {
		domID := strings.ToUpper(hex.EncodeToString(dir.DomainID[:]))
		s.SetOriginal("DomainID", domID)
		s.SetField("DomainID", domID)
	}
}

// UpdateIndexes updates the directory indexes and tracks changes
func (s *SLEDirectoryNode) UpdateIndexes(indexes [][32]byte) {
	strIndexes := make([]string, len(indexes))
	for i, idx := range indexes {
		strIndexes[i] = strings.ToUpper(hex.EncodeToString(idx[:]))
	}
	s.SetField("Indexes", strIndexes)
}

// formatUint64 formats a uint64 as a decimal string (for XRP drops)
func formatUint64(v uint64) string {
	if v == 0 {
		return "0"
	}
	result := make([]byte, 20)
	i := len(result)
	for v > 0 {
		i--
		result[i] = byte(v%10) + '0'
		v /= 10
	}
	return string(result[i:])
}

// Note: formatUint64Hex is defined in directory.go
