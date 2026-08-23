package state

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// accountRootReader is the minimal read surface ReadAccountRoot needs.
type accountRootReader interface {
	Read(k keylet.Keylet) ([]byte, error)
}

// ReadAccountRoot reads and parses the AccountRoot for accountID from view.
// An absent account returns (nil, nil); storage and parse failures are
// preserved so protocol callers can distinguish corruption from absence.
func ReadAccountRoot(view accountRootReader, accountID [20]byte) (*AccountRoot, error) {
	data, err := view.Read(keylet.Account(accountID))
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	return ParseAccountRoot(data)
}

// AccountRoot represents an account in the ledger
type AccountRoot struct {
	Account                string
	Balance                uint64
	Sequence               uint32
	OwnerCount             uint32
	SponsoredOwnerCount    uint32 // Owner-count units paid by this account's sponsors
	SponsoringOwnerCount   uint32 // Owner-count units this account sponsors
	SponsoringAccountCount uint32 // Distinct accounts sponsored by this account
	Sponsor                string
	HasSponsor             bool
	Flags                  uint32
	RegularKey             string
	Domain                 string
	EmailHash              string
	MessageKey             string
	TransferRate           uint32
	TickSize               uint8
	NFTokenMinter          string   // Account allowed to mint NFTokens on behalf of this account
	MintedNFTokens         uint32   // Number of NFTokens minted by this account (issuer tracking)
	BurnedNFTokens         uint32   // Number of NFTokens burned for this issuer
	FirstNFTokenSequence   uint32   // First NFToken sequence (set by fixNFTokenRemint)
	HasFirstNFTSeq         bool     // Whether FirstNFTokenSequence is set (zero is a valid value)
	AccountTxnID           [32]byte // Hash of the last transaction this account submitted (when enabled)
	HasAccountTxnID        bool     // Whether sfAccountTxnID is present (zero is a valid value after asfAccountTxnID is enabled)
	WalletLocator          string   // Arbitrary hex data (deprecated)
	WalletSize             uint32   // Arbitrary data (deprecated)
	HasWalletSize          bool     // Whether sfWalletSize is present (zero is a valid value)
	TicketCount            uint32   // Number of outstanding tickets owned by this account
	AMMID                  [32]byte // Links AMM pseudo-account to its AMM ledger entry (sfAMMID, fieldCode 14)
	VaultID                [32]byte // Links Vault pseudo-account to its Vault ledger entry (sfVaultID, fieldCode 35)
	LoanBrokerID           [32]byte // Links LoanBroker pseudo-account to its LoanBroker ledger entry (sfLoanBrokerID, fieldCode 37)
	PreviousTxnID          [32]byte
	PreviousTxnLgrSeq      uint32
	decodedOptionals       map[string]any
}

// HasAMMID reports whether the sfAMMID field is present, the faithful equivalent
// of rippled's sleAcct->isFieldPresent(sfAMMID). AMMID is a SHA-512Half hash that
// is never zero when set, and the serializer emits it only when non-zero, so a zero
// value is the canonical representation of an absent field.
func (a *AccountRoot) HasAMMID() bool {
	return a != nil && a.AMMID != [32]byte{}
}

// HasVaultID reports whether the sfVaultID pseudo-account designator is present.
func (a *AccountRoot) HasVaultID() bool {
	return a != nil && a.VaultID != [32]byte{}
}

// HasLoanBrokerID reports whether the sfLoanBrokerID pseudo-account designator is present.
func (a *AccountRoot) HasLoanBrokerID() bool {
	return a != nil && a.LoanBrokerID != [32]byte{}
}

// PseudoAccountFieldCount returns how many pseudo-account designator fields
// (sfAMMID, sfVaultID, sfLoanBrokerID) are present. rippled's ValidPseudoAccounts
// invariant requires exactly one to be set. Reference: rippled sfields.macro —
// fields flagged SField::sMD_PseudoAccount.
func (a *AccountRoot) PseudoAccountFieldCount() int {
	if a == nil {
		return 0
	}
	n := 0
	if a.HasAMMID() {
		n++
	}
	if a.HasVaultID() {
		n++
	}
	if a.HasLoanBrokerID() {
		n++
	}
	return n
}

// IsPseudoAccount reports whether this AccountRoot is a pseudo-account, mirroring
// rippled's isPseudoAccount (View.cpp) which tests whether any of the
// pseudo-account owner fields (sfAMMID, sfVaultID, sfLoanBrokerID) is present.
func (a *AccountRoot) IsPseudoAccount() bool {
	return a.PseudoAccountFieldCount() > 0
}

const FieldTypeHash256 = 5

// STArray/STObject delimiters in the canonical binary format.
const (
	objectEndMarker = 0xE1
	arrayEndMarker  = 0xF1
)

// AccountRoot ledger entry flags.
const (
	LsfPasswordSpent                = entry.LsfPasswordSpent
	LsfRequireDestTag               = entry.LsfRequireDestTag
	LsfRequireAuth                  = entry.LsfRequireAuth
	LsfDisallowXRP                  = entry.LsfDisallowXRP
	LsfDisableMaster                = entry.LsfDisableMaster
	LsfNoFreeze                     = entry.LsfNoFreeze
	LsfGlobalFreeze                 = entry.LsfGlobalFreeze
	LsfDefaultRipple                = entry.LsfDefaultRipple
	LsfDepositAuth                  = entry.LsfDepositAuth
	LsfDisallowIncomingNFTokenOffer = entry.LsfDisallowIncomingNFTokenOffer
	LsfDisallowIncomingCheck        = entry.LsfDisallowIncomingCheck
	LsfDisallowIncomingPayChan      = entry.LsfDisallowIncomingPayChan
	LsfDisallowIncomingTrustline    = entry.LsfDisallowIncomingTrustline
	LsfAllowTrustLineLocking        = entry.LsfAllowTrustLineLocking
	LsfAllowTrustLineClawback       = entry.LsfAllowTrustLineClawback
)

// encodeAccountID encodes a 20-byte account ID to an XRPL address
func encodeAccountID(accountID [20]byte) (string, error) {
	return addresscodec.EncodeAccountIDToClassicAddress(accountID[:])
}

// ParseAccountRoot parses account data from binary format
func ParseAccountRoot(data []byte) (*AccountRoot, error) {
	var decoded entry.AccountRoot
	if err := decoded.Decode(data); err != nil {
		return nil, fmt.Errorf("failed to decode AccountRoot: %w", err)
	}
	fields := decoded.ToMap()
	balance, err := decodeNativeLedgerBalance("AccountRoot.Balance", decoded.Balance)
	if err != nil {
		return nil, err
	}
	domain, err := hex.DecodeString(decoded.Domain)
	if err != nil {
		return nil, fmt.Errorf("AccountRoot.Domain: invalid hex: %w", err)
	}
	if decoded.TickSize < 0 || decoded.TickSize > 255 {
		return nil, fmt.Errorf("AccountRoot.TickSize: decoded value %d is out of range", decoded.TickSize)
	}

	account := &AccountRoot{
		Account:                decoded.Account,
		Balance:                balance,
		Sequence:               decoded.Sequence,
		OwnerCount:             decoded.OwnerCount,
		SponsoredOwnerCount:    decoded.SponsoredOwnerCount,
		SponsoringOwnerCount:   decoded.SponsoringOwnerCount,
		SponsoringAccountCount: decoded.SponsoringAccountCount,
		Sponsor:                decoded.Sponsor,
		HasSponsor:             fields["Sponsor"] != nil,
		Flags:                  decoded.Flags,
		RegularKey:             decoded.RegularKey,
		Domain:                 string(domain),
		EmailHash:              strings.ToLower(decoded.EmailHash),
		MessageKey:             strings.ToLower(decoded.MessageKey),
		TransferRate:           decoded.TransferRate,
		TickSize:               uint8(decoded.TickSize),
		NFTokenMinter:          decoded.NFTokenMinter,
		MintedNFTokens:         decoded.MintedNFTokens,
		BurnedNFTokens:         decoded.BurnedNFTokens,
		FirstNFTokenSequence:   decoded.FirstNFTokenSequence,
		HasFirstNFTSeq:         fields["FirstNFTokenSequence"] != nil,
		HasAccountTxnID:        fields["AccountTxnID"] != nil,
		WalletLocator:          strings.ToLower(decoded.WalletLocator),
		WalletSize:             decoded.WalletSize,
		HasWalletSize:          fields["WalletSize"] != nil,
		TicketCount:            decoded.TicketCount,
		PreviousTxnLgrSeq:      decoded.PreviousTxnLgrSeq,
		decodedOptionals: map[string]any{
			"Domain":       string(domain),
			"MessageKey":   strings.ToLower(decoded.MessageKey),
			"TransferRate": decoded.TransferRate,
			"TickSize":     uint8(decoded.TickSize),
			"TicketCount":  decoded.TicketCount,
			"AMMID":        [32]byte{},
			"VaultID":      [32]byte{},
			"LoanBrokerID": [32]byte{},
		},
	}
	for _, field := range []string{"Domain", "MessageKey", "TransferRate", "TickSize", "TicketCount", "AMMID", "VaultID", "LoanBrokerID"} {
		if _, ok := fields[field]; !ok {
			delete(account.decodedOptionals, field)
		}
	}
	for _, hash := range []struct {
		field string
		value string
		dst   []byte
	}{
		{"AccountTxnID", decoded.AccountTxnID, account.AccountTxnID[:]},
		{"AMMID", decoded.AMMID, account.AMMID[:]},
		{"VaultID", decoded.VaultID, account.VaultID[:]},
		{"LoanBrokerID", decoded.LoanBrokerID, account.LoanBrokerID[:]},
		{"PreviousTxnID", decoded.PreviousTxnID, account.PreviousTxnID[:]},
	} {
		if _, ok := fields[hash.field]; !ok {
			continue
		}
		if err := decodeLedgerHex("AccountRoot."+hash.field, hash.value, hash.dst); err != nil {
			return nil, err
		}
	}
	if _, ok := fields["AMMID"]; ok {
		account.decodedOptionals["AMMID"] = account.AMMID
	}
	if _, ok := fields["VaultID"]; ok {
		account.decodedOptionals["VaultID"] = account.VaultID
	}
	if _, ok := fields["LoanBrokerID"]; ok {
		account.decodedOptionals["LoanBrokerID"] = account.LoanBrokerID
	}
	return account, nil
}

// SerializeAccountRoot serializes an AccountRoot to binary format
func SerializeAccountRoot(account *AccountRoot) ([]byte, error) {
	if account == nil {
		return nil, errors.New("failed to encode AccountRoot: nil entry")
	}

	var sle entry.AccountRoot
	sle.SetBalance(fmt.Sprintf("%d", account.Balance))
	sle.SetSequence(account.Sequence)
	sle.SetOwnerCount(account.OwnerCount)
	if account.SponsoredOwnerCount > 0 {
		sle.SetSponsoredOwnerCount(account.SponsoredOwnerCount)
	}
	if account.SponsoringOwnerCount > 0 {
		sle.SetSponsoringOwnerCount(account.SponsoringOwnerCount)
	}
	if account.SponsoringAccountCount > 0 {
		sle.SetSponsoringAccountCount(account.SponsoringAccountCount)
	}
	if account.HasSponsor || account.Sponsor != "" {
		sle.SetSponsor(account.Sponsor)
	}
	sle.SetFlags(account.Flags)

	if account.Account != "" {
		sle.SetAccount(account.Account)
	}

	if account.TransferRate > 0 || decodedFieldUnchanged(account.decodedOptionals, "TransferRate", account.TransferRate) {
		sle.SetTransferRate(account.TransferRate)
	}

	if account.RegularKey != "" {
		sle.SetRegularKey(account.RegularKey)
	}

	if account.Domain != "" || decodedFieldUnchanged(account.decodedOptionals, "Domain", account.Domain) {
		sle.SetDomain(strings.ToUpper(hex.EncodeToString([]byte(account.Domain))))
	}

	if account.EmailHash != "" {
		sle.SetEmailHash(strings.ToUpper(account.EmailHash))
	}

	if account.MessageKey != "" || decodedFieldUnchanged(account.decodedOptionals, "MessageKey", account.MessageKey) {
		sle.SetMessageKey(strings.ToUpper(account.MessageKey))
	}

	if account.NFTokenMinter != "" {
		sle.SetNFTokenMinter(account.NFTokenMinter)
	}

	if account.MintedNFTokens > 0 {
		sle.SetMintedNFTokens(account.MintedNFTokens)
	}

	if account.BurnedNFTokens > 0 {
		sle.SetBurnedNFTokens(account.BurnedNFTokens)
	}

	if account.HasFirstNFTSeq {
		sle.SetFirstNFTokenSequence(account.FirstNFTokenSequence)
	}

	if account.TicketCount > 0 || decodedFieldUnchanged(account.decodedOptionals, "TicketCount", account.TicketCount) {
		sle.SetTicketCount(account.TicketCount)
	}

	var zeroHash [32]byte
	if account.HasAccountTxnID {
		sle.SetAccountTxnID(strings.ToUpper(hex.EncodeToString(account.AccountTxnID[:])))
	}

	if account.WalletLocator != "" {
		sle.SetWalletLocator(strings.ToUpper(account.WalletLocator))
	}
	if account.HasWalletSize {
		sle.SetWalletSize(account.WalletSize)
	}

	if account.AMMID != zeroHash || decodedFieldUnchanged(account.decodedOptionals, "AMMID", account.AMMID) {
		sle.SetAMMID(strings.ToUpper(hex.EncodeToString(account.AMMID[:])))
	}

	if account.VaultID != zeroHash || decodedFieldUnchanged(account.decodedOptionals, "VaultID", account.VaultID) {
		sle.SetVaultID(strings.ToUpper(hex.EncodeToString(account.VaultID[:])))
	}
	if account.LoanBrokerID != zeroHash || decodedFieldUnchanged(account.decodedOptionals, "LoanBrokerID", account.LoanBrokerID) {
		sle.SetLoanBrokerID(strings.ToUpper(hex.EncodeToString(account.LoanBrokerID[:])))
	}

	sle.SetPreviousTxnID(strings.ToUpper(hex.EncodeToString(account.PreviousTxnID[:])))
	sle.SetPreviousTxnLgrSeq(account.PreviousTxnLgrSeq)

	if account.TickSize > 0 || decodedFieldUnchanged(account.decodedOptionals, "TickSize", account.TickSize) {
		sle.SetTickSize(account.TickSize)
	}

	data, err := sle.Encode()
	if err != nil {
		return nil, fmt.Errorf("failed to encode AccountRoot: %w", err)
	}
	return data, nil
}
