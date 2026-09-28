package state

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
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
	decoded                entry.AccountRoot
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
	balanceValue, err := decoded.GetBalance()
	if err != nil {
		return nil, err
	}
	balance, err := decodeNativeLedgerBalance("AccountRoot.Balance", balanceValue)
	if err != nil {
		return nil, err
	}
	domain, err := decoded.GetDomain()
	if err != nil {
		return nil, fmt.Errorf("AccountRoot.Domain: invalid hex: %w", err)
	}
	tickSize, err := decoded.GetTickSize()
	if err != nil {
		return nil, err
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
		HasSponsor:             decoded.HasSponsor(),
		Flags:                  decoded.Flags,
		RegularKey:             decoded.RegularKey,
		Domain:                 string(domain),
		EmailHash:              strings.ToLower(decoded.EmailHash),
		MessageKey:             strings.ToLower(decoded.MessageKey),
		TransferRate:           decoded.TransferRate,
		TickSize:               tickSize,
		NFTokenMinter:          decoded.NFTokenMinter,
		MintedNFTokens:         decoded.MintedNFTokens,
		BurnedNFTokens:         decoded.BurnedNFTokens,
		FirstNFTokenSequence:   decoded.FirstNFTokenSequence,
		HasFirstNFTSeq:         decoded.HasFirstNFTokenSequence(),
		HasAccountTxnID:        decoded.HasAccountTxnID(),
		WalletLocator:          strings.ToLower(decoded.WalletLocator),
		WalletSize:             decoded.WalletSize,
		HasWalletSize:          decoded.HasWalletSize(),
		TicketCount:            decoded.TicketCount,
		PreviousTxnLgrSeq:      decoded.PreviousTxnLgrSeq,
		decoded:                decoded,
	}
	if decoded.HasAccountTxnID() {
		account.AccountTxnID, err = decoded.GetAccountTxnID()
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasAMMID() {
		account.AMMID, err = decoded.GetAMMID()
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasVaultID() {
		account.VaultID, err = decoded.GetVaultID()
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasLoanBrokerID() {
		account.LoanBrokerID, err = decoded.GetLoanBrokerID()
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasPreviousTxnID() {
		account.PreviousTxnID, err = decoded.GetPreviousTxnID()
		if err != nil {
			return nil, err
		}
	}
	return account, nil
}

// SerializeAccountRoot serializes an AccountRoot to binary format
func SerializeAccountRoot(account *AccountRoot) ([]byte, error) {
	if account == nil {
		return nil, errors.New("failed to encode AccountRoot: nil entry")
	}

	if account.Account == "" {
		return nil, errors.New("failed to encode AccountRoot: required field Account is not set")
	}
	decodedDomain, err := account.decoded.GetDomain()
	if err != nil {
		return nil, err
	}
	decodedAMMID, err := account.decoded.GetAMMID()
	if err != nil {
		return nil, err
	}
	decodedVaultID, err := account.decoded.GetVaultID()
	if err != nil {
		return nil, err
	}
	decodedLoanBrokerID, err := account.decoded.GetLoanBrokerID()
	if err != nil {
		return nil, err
	}
	sle := account.decoded
	if err := sle.SetBalanceValue(entry.AmountValue{Value: strconv.FormatUint(account.Balance, 10)}); err != nil {
		return nil, err
	}
	sle.SetSequence(account.Sequence)
	sle.SetOwnerCount(account.OwnerCount)
	sle.SetSponsoredOwnerCount(account.SponsoredOwnerCount)
	sle.SetSponsoringOwnerCount(account.SponsoringOwnerCount)
	sle.SetSponsoringAccountCount(account.SponsoringAccountCount)
	sle.SetFlags(account.Flags)
	sle.SetMintedNFTokens(account.MintedNFTokens)
	sle.SetBurnedNFTokens(account.BurnedNFTokens)
	sle.SetAccount(account.Account)
	if account.HasSponsor || account.Sponsor != "" {
		sle.SetSponsor(account.Sponsor)
	} else {
		sle.ClearSponsor()
	}
	if account.TransferRate != 0 || (account.decoded.HasTransferRate() && account.decoded.TransferRate == account.TransferRate) {
		sle.SetTransferRate(account.TransferRate)
	} else {
		sle.ClearTransferRate()
	}
	if account.RegularKey != "" || (account.decoded.HasRegularKey() && account.decoded.RegularKey == account.RegularKey) {
		sle.SetRegularKey(account.RegularKey)
	} else {
		sle.ClearRegularKey()
	}
	if account.Domain != "" || (account.decoded.HasDomain() && bytes.Equal(decodedDomain, []byte(account.Domain))) {
		sle.SetDomainValue([]byte(account.Domain))
	} else {
		sle.ClearDomain()
	}
	if account.EmailHash != "" || (account.decoded.HasEmailHash() && account.decoded.EmailHash == strings.ToUpper(account.EmailHash)) {
		sle.SetEmailHash(strings.ToUpper(account.EmailHash))
	} else {
		sle.ClearEmailHash()
	}
	if account.MessageKey != "" || (account.decoded.HasMessageKey() && account.decoded.MessageKey == strings.ToUpper(account.MessageKey)) {
		sle.SetMessageKey(strings.ToUpper(account.MessageKey))
	} else {
		sle.ClearMessageKey()
	}
	if account.NFTokenMinter != "" || (account.decoded.HasNFTokenMinter() && account.decoded.NFTokenMinter == account.NFTokenMinter) {
		sle.SetNFTokenMinter(account.NFTokenMinter)
	} else {
		sle.ClearNFTokenMinter()
	}
	if account.HasFirstNFTSeq {
		sle.SetFirstNFTokenSequence(account.FirstNFTokenSequence)
	} else {
		sle.ClearFirstNFTokenSequence()
	}
	if account.TicketCount != 0 || (account.decoded.HasTicketCount() && account.decoded.TicketCount == account.TicketCount) {
		sle.SetTicketCount(account.TicketCount)
	} else {
		sle.ClearTicketCount()
	}
	if account.HasAccountTxnID {
		sle.SetAccountTxnIDValue(account.AccountTxnID)
	} else {
		sle.ClearAccountTxnID()
	}
	if account.WalletLocator != "" || (account.decoded.HasWalletLocator() && account.decoded.WalletLocator == strings.ToUpper(account.WalletLocator)) {
		sle.SetWalletLocator(strings.ToUpper(account.WalletLocator))
	} else {
		sle.ClearWalletLocator()
	}
	if account.HasWalletSize {
		sle.SetWalletSize(account.WalletSize)
	} else {
		sle.ClearWalletSize()
	}
	if account.AMMID != [32]byte{} || (account.decoded.HasAMMID() && decodedAMMID == account.AMMID) {
		sle.SetAMMIDValue(account.AMMID)
	} else {
		sle.ClearAMMID()
	}
	if account.VaultID != [32]byte{} || (account.decoded.HasVaultID() && decodedVaultID == account.VaultID) {
		sle.SetVaultIDValue(account.VaultID)
	} else {
		sle.ClearVaultID()
	}
	if account.LoanBrokerID != [32]byte{} || (account.decoded.HasLoanBrokerID() && decodedLoanBrokerID == account.LoanBrokerID) {
		sle.SetLoanBrokerIDValue(account.LoanBrokerID)
	} else {
		sle.ClearLoanBrokerID()
	}
	if account.TickSize != 0 || (account.decoded.HasTickSize() && account.decoded.TickSize == int(account.TickSize)) {
		sle.SetTickSize(account.TickSize)
	} else {
		sle.ClearTickSize()
	}
	sle.SetPreviousTxnIDValue(account.PreviousTxnID)
	sle.SetPreviousTxnLgrSeq(account.PreviousTxnLgrSeq)

	data, err := sle.Encode()
	if err != nil {
		return nil, fmt.Errorf("failed to encode AccountRoot: %w", err)
	}
	return data, nil
}
