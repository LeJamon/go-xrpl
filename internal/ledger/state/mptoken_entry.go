package state

import (
	"bytes"
	"fmt"
	"strings"

	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
)

// MPTokenIssuanceData holds parsed fields of an MPTokenIssuance ledger entry.
// Reference: rippled LedgerFormats.h ltMPTOKEN_ISSUANCE
type MPTokenIssuanceData struct {
	Issuer                        [20]byte
	Sequence                      uint32
	OwnerNode                     uint64
	OutstandingAmount             uint64
	TransferFee                   uint16
	AssetScale                    uint8
	MaximumAmount                 *uint64
	LockedAmount                  *uint64
	MPTokenMetadata               string  // hex-encoded
	DomainID                      *string // hex-encoded 32-byte hash, nil if not set
	ReferenceHolding              *string // hex-encoded 32-byte hash (vault share underlying), nil if not set
	Flags                         uint32
	ImmutableFlags                uint32 // soeDEFAULT: immutable capability and field bits, 0 when absent
	Sponsor                       string
	IssuerEncryptionKey           []byte
	AuditorEncryptionKey          []byte
	ConfidentialOutstandingAmount uint64

	// Threading fields. MPTokenIssuance is a threaded type, so these must
	// survive a parse→serialize round-trip.
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32

	decoded ledgerfields.MPTokenIssuance
}

// MPTokenData holds parsed fields of an MPToken ledger entry.
// Reference: rippled LedgerFormats.h ltMPTOKEN
type MPTokenData struct {
	Account                     [20]byte
	MPTokenIssuanceID           [24]byte // Hash192 (24 bytes)
	OwnerNode                   uint64
	MPTAmount                   uint64
	LockedAmount                *uint64
	Flags                       uint32
	Sponsor                     string
	ConfidentialBalanceInbox    []byte
	ConfidentialBalanceSpending []byte
	ConfidentialBalanceVersion  uint32
	IssuerEncryptedBalance      []byte
	AuditorEncryptedBalance     []byte
	HolderEncryptionKey         []byte

	// Threading fields — see MPTokenIssuanceData.
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32

	decoded ledgerfields.MPToken
}

// ParseMPTokenIssuance parses an MPTokenIssuance ledger entry from binary data.
func ParseMPTokenIssuance(data []byte) (*MPTokenIssuanceData, error) {
	var decoded ledgerfields.MPTokenIssuance
	if err := decoded.Decode(data); err != nil {
		return nil, err
	}
	issuance := &MPTokenIssuanceData{
		decoded: decoded,
	}
	var err error
	issuance.Flags, err = decoded.GetFlags()
	if err != nil {
		return nil, fmt.Errorf("failed to decode MPTokenIssuance.Flags: %w", err)
	}
	if decoded.HasIssuer() {
		issuance.Issuer, err = decoded.GetIssuer()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.Issuer: %w", err)
		}
	}
	if decoded.HasSequence() {
		issuance.Sequence, err = decoded.GetSequence()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.Sequence: %w", err)
		}
	}
	if decoded.HasTransferFee() {
		transferFee, err := decoded.GetTransferFee()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.TransferFee: %w", err)
		}
		issuance.TransferFee = transferFee
	}
	if decoded.HasOwnerNode() {
		var err error
		issuance.OwnerNode, err = decoded.GetOwnerNode()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.OwnerNode: %w", err)
		}
	}
	if decoded.HasAssetScale() {
		assetScale, err := decoded.GetAssetScale()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.AssetScale: %w", err)
		}
		issuance.AssetScale = assetScale
	}
	if decoded.HasOutstandingAmount() {
		var err error
		issuance.OutstandingAmount, err = decoded.GetOutstandingAmount()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.OutstandingAmount: %w", err)
		}
	}
	if decoded.HasMaximumAmount() {
		value, err := decoded.GetMaximumAmount()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.MaximumAmount: %w", err)
		}
		issuance.MaximumAmount = &value
	}
	if decoded.HasLockedAmount() {
		value, err := decoded.GetLockedAmount()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.LockedAmount: %w", err)
		}
		issuance.LockedAmount = &value
	}
	if decoded.HasMPTokenMetadata() {
		issuance.MPTokenMetadata = strings.ToLower(decoded.MPTokenMetadata)
	}
	if decoded.HasDomainID() {
		value := strings.ToLower(decoded.DomainID)
		issuance.DomainID = &value
	}
	if decoded.HasReferenceHolding() {
		value := strings.ToLower(decoded.ReferenceHolding)
		issuance.ReferenceHolding = &value
	}
	if decoded.HasImmutableFlags() {
		issuance.ImmutableFlags, err = decoded.GetImmutableFlags()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.ImmutableFlags: %w", err)
		}
	}
	if decoded.HasIssuerEncryptionKey() {
		value, err := decoded.GetIssuerEncryptionKey()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.IssuerEncryptionKey: %w", err)
		}
		issuance.IssuerEncryptionKey = append([]byte(nil), value...)
	}
	if decoded.HasAuditorEncryptionKey() {
		value, err := decoded.GetAuditorEncryptionKey()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.AuditorEncryptionKey: %w", err)
		}
		issuance.AuditorEncryptionKey = append([]byte(nil), value...)
	}
	if decoded.HasConfidentialOutstandingAmount() {
		var err error
		issuance.ConfidentialOutstandingAmount, err = decoded.GetConfidentialOutstandingAmount()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.ConfidentialOutstandingAmount: %w", err)
		}
	}
	if decoded.HasSponsor() {
		issuance.Sponsor = decoded.Sponsor
	}
	if decoded.HasPreviousTxnID() {
		var err error
		issuance.PreviousTxnID, err = decoded.GetPreviousTxnID()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.PreviousTxnID: %w", err)
		}
	}
	if decoded.HasPreviousTxnLgrSeq() {
		issuance.PreviousTxnLgrSeq, err = decoded.GetPreviousTxnLgrSeq()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.PreviousTxnLgrSeq: %w", err)
		}
	}

	return issuance, nil
}

// SerializeMPTokenIssuance serializes an MPTokenIssuance to binary format.
func SerializeMPTokenIssuance(issuance *MPTokenIssuanceData) ([]byte, error) {
	entry := issuance.decoded
	issuerUnchanged := false
	if entry.HasIssuer() {
		original, err := entry.GetIssuer()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.Issuer: %w", err)
		}
		issuerUnchanged = original == issuance.Issuer
	}
	if !entry.HasIssuer() || !issuerUnchanged {
		if err := entry.SetIssuerValue(issuance.Issuer); err != nil {
			return nil, fmt.Errorf("failed to encode MPTokenIssuance.Issuer: %w", err)
		}
	}
	entry.SetSequenceValue(issuance.Sequence)
	entry.SetFlagsValue(issuance.Flags)
	entry.SetOwnerNodeValue(issuance.OwnerNode)
	entry.SetOutstandingAmountValue(issuance.OutstandingAmount)
	entry.SetTransferFeeValue(issuance.TransferFee)
	entry.SetAssetScale(issuance.AssetScale)
	if issuance.MaximumAmount != nil {
		entry.SetMaximumAmountValue(*issuance.MaximumAmount)
	} else {
		entry.ClearMaximumAmount()
	}
	if issuance.LockedAmount != nil {
		entry.SetLockedAmountValue(*issuance.LockedAmount)
	} else {
		entry.ClearLockedAmount()
	}
	if issuance.MPTokenMetadata != "" || (entry.HasMPTokenMetadata() && strings.EqualFold(issuance.MPTokenMetadata, entry.MPTokenMetadata)) {
		entry.SetMPTokenMetadata(issuance.MPTokenMetadata)
	} else {
		entry.ClearMPTokenMetadata()
	}
	if issuance.DomainID != nil && (*issuance.DomainID != "" || strings.EqualFold(*issuance.DomainID, entry.DomainID)) {
		entry.SetDomainID(*issuance.DomainID)
	} else {
		entry.ClearDomainID()
	}
	if issuance.ReferenceHolding != nil && (*issuance.ReferenceHolding != "" || strings.EqualFold(*issuance.ReferenceHolding, entry.ReferenceHolding)) {
		entry.SetReferenceHolding(*issuance.ReferenceHolding)
	} else {
		entry.ClearReferenceHolding()
	}
	entry.SetImmutableFlagsValue(issuance.ImmutableFlags)
	if err := setOptionalMPTokenBlob(
		"MPTokenIssuance.IssuerEncryptionKey",
		issuance.IssuerEncryptionKey,
		entry.HasIssuerEncryptionKey,
		entry.GetIssuerEncryptionKey,
		entry.SetIssuerEncryptionKeyValue,
		entry.ClearIssuerEncryptionKey,
	); err != nil {
		return nil, err
	}
	if err := setOptionalMPTokenBlob(
		"MPTokenIssuance.AuditorEncryptionKey",
		issuance.AuditorEncryptionKey,
		entry.HasAuditorEncryptionKey,
		entry.GetAuditorEncryptionKey,
		entry.SetAuditorEncryptionKeyValue,
		entry.ClearAuditorEncryptionKey,
	); err != nil {
		return nil, err
	}
	entry.SetConfidentialOutstandingAmountValue(issuance.ConfidentialOutstandingAmount)
	if issuance.Sponsor != "" || (entry.HasSponsor() && issuance.Sponsor == entry.Sponsor) {
		entry.SetSponsor(issuance.Sponsor)
	} else {
		entry.ClearSponsor()
	}
	entry.SetPreviousTxnIDValue(issuance.PreviousTxnID)
	entry.SetPreviousTxnLgrSeqValue(issuance.PreviousTxnLgrSeq)

	return entry.Encode()
}

// ParseMPToken parses an MPToken ledger entry from binary data.
func ParseMPToken(data []byte) (*MPTokenData, error) {
	var decoded ledgerfields.MPToken
	if err := decoded.Decode(data); err != nil {
		return nil, err
	}
	token := &MPTokenData{
		decoded: decoded,
	}
	var err error
	token.Flags, err = decoded.GetFlags()
	if err != nil {
		return nil, fmt.Errorf("failed to decode MPToken.Flags: %w", err)
	}
	if decoded.HasAccount() {
		token.Account, err = decoded.GetAccount()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPToken.Account: %w", err)
		}
	}
	if decoded.HasMPTokenIssuanceID() {
		var err error
		token.MPTokenIssuanceID, err = decoded.GetMPTokenIssuanceID()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPToken.MPTokenIssuanceID: %w", err)
		}
	}
	if decoded.HasOwnerNode() {
		var err error
		token.OwnerNode, err = decoded.GetOwnerNode()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPToken.OwnerNode: %w", err)
		}
	}
	if decoded.HasMPTAmount() {
		var err error
		token.MPTAmount, err = decoded.GetMPTAmount()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPToken.MPTAmount: %w", err)
		}
	}
	if decoded.HasLockedAmount() {
		value, err := decoded.GetLockedAmount()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPToken.LockedAmount: %w", err)
		}
		token.LockedAmount = &value
	}
	if decoded.HasSponsor() {
		token.Sponsor = decoded.Sponsor
	}
	if decoded.HasConfidentialBalanceInbox() {
		var err error
		token.ConfidentialBalanceInbox, err = decoded.GetConfidentialBalanceInbox()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPToken.ConfidentialBalanceInbox: %w", err)
		}
		token.ConfidentialBalanceInbox = append([]byte(nil), token.ConfidentialBalanceInbox...)
	}
	if decoded.HasConfidentialBalanceSpending() {
		var err error
		token.ConfidentialBalanceSpending, err = decoded.GetConfidentialBalanceSpending()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPToken.ConfidentialBalanceSpending: %w", err)
		}
		token.ConfidentialBalanceSpending = append([]byte(nil), token.ConfidentialBalanceSpending...)
	}
	if decoded.HasConfidentialBalanceVersion() {
		token.ConfidentialBalanceVersion, err = decoded.GetConfidentialBalanceVersion()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPToken.ConfidentialBalanceVersion: %w", err)
		}
	}
	if decoded.HasIssuerEncryptedBalance() {
		var err error
		token.IssuerEncryptedBalance, err = decoded.GetIssuerEncryptedBalance()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPToken.IssuerEncryptedBalance: %w", err)
		}
		token.IssuerEncryptedBalance = append([]byte(nil), token.IssuerEncryptedBalance...)
	}
	if decoded.HasAuditorEncryptedBalance() {
		var err error
		token.AuditorEncryptedBalance, err = decoded.GetAuditorEncryptedBalance()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPToken.AuditorEncryptedBalance: %w", err)
		}
		token.AuditorEncryptedBalance = append([]byte(nil), token.AuditorEncryptedBalance...)
	}
	if decoded.HasHolderEncryptionKey() {
		var err error
		token.HolderEncryptionKey, err = decoded.GetHolderEncryptionKey()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPToken.HolderEncryptionKey: %w", err)
		}
		token.HolderEncryptionKey = append([]byte(nil), token.HolderEncryptionKey...)
	}
	if decoded.HasPreviousTxnID() {
		var err error
		token.PreviousTxnID, err = decoded.GetPreviousTxnID()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPToken.PreviousTxnID: %w", err)
		}
	}
	if decoded.HasPreviousTxnLgrSeq() {
		token.PreviousTxnLgrSeq, err = decoded.GetPreviousTxnLgrSeq()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPToken.PreviousTxnLgrSeq: %w", err)
		}
	}

	return token, nil
}

// SerializeMPToken serializes an MPToken to binary format.
func SerializeMPToken(token *MPTokenData) ([]byte, error) {
	entry := token.decoded
	accountUnchanged := false
	if entry.HasAccount() {
		original, err := entry.GetAccount()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPToken.Account: %w", err)
		}
		accountUnchanged = original == token.Account
	}
	if !entry.HasAccount() || !accountUnchanged {
		if err := entry.SetAccountValue(token.Account); err != nil {
			return nil, fmt.Errorf("failed to encode MPToken.Account: %w", err)
		}
	}
	entry.SetFlagsValue(token.Flags)
	entry.SetMPTokenIssuanceIDValue(token.MPTokenIssuanceID)
	entry.SetOwnerNodeValue(token.OwnerNode)
	entry.SetMPTAmountValue(token.MPTAmount)
	if token.LockedAmount != nil {
		entry.SetLockedAmountValue(*token.LockedAmount)
	} else {
		entry.ClearLockedAmount()
	}
	if token.Sponsor != "" || (entry.HasSponsor() && token.Sponsor == entry.Sponsor) {
		entry.SetSponsor(token.Sponsor)
	} else {
		entry.ClearSponsor()
	}
	if err := setOptionalMPTokenBlob(
		"MPToken.ConfidentialBalanceInbox",
		token.ConfidentialBalanceInbox,
		entry.HasConfidentialBalanceInbox,
		entry.GetConfidentialBalanceInbox,
		entry.SetConfidentialBalanceInboxValue,
		entry.ClearConfidentialBalanceInbox,
	); err != nil {
		return nil, err
	}
	if err := setOptionalMPTokenBlob(
		"MPToken.ConfidentialBalanceSpending",
		token.ConfidentialBalanceSpending,
		entry.HasConfidentialBalanceSpending,
		entry.GetConfidentialBalanceSpending,
		entry.SetConfidentialBalanceSpendingValue,
		entry.ClearConfidentialBalanceSpending,
	); err != nil {
		return nil, err
	}
	entry.SetConfidentialBalanceVersionValue(token.ConfidentialBalanceVersion)
	if err := setOptionalMPTokenBlob(
		"MPToken.IssuerEncryptedBalance",
		token.IssuerEncryptedBalance,
		entry.HasIssuerEncryptedBalance,
		entry.GetIssuerEncryptedBalance,
		entry.SetIssuerEncryptedBalanceValue,
		entry.ClearIssuerEncryptedBalance,
	); err != nil {
		return nil, err
	}
	if err := setOptionalMPTokenBlob(
		"MPToken.AuditorEncryptedBalance",
		token.AuditorEncryptedBalance,
		entry.HasAuditorEncryptedBalance,
		entry.GetAuditorEncryptedBalance,
		entry.SetAuditorEncryptedBalanceValue,
		entry.ClearAuditorEncryptedBalance,
	); err != nil {
		return nil, err
	}
	if err := setOptionalMPTokenBlob(
		"MPToken.HolderEncryptionKey",
		token.HolderEncryptionKey,
		entry.HasHolderEncryptionKey,
		entry.GetHolderEncryptionKey,
		entry.SetHolderEncryptionKeyValue,
		entry.ClearHolderEncryptionKey,
	); err != nil {
		return nil, err
	}
	entry.SetPreviousTxnIDValue(token.PreviousTxnID)
	entry.SetPreviousTxnLgrSeqValue(token.PreviousTxnLgrSeq)

	return entry.Encode()
}

func setOptionalMPTokenBlob(field string, value []byte, present func() bool, get func() ([]byte, error), set func([]byte), clear func()) error {
	if len(value) != 0 {
		set(value)
		return nil
	}
	if present() {
		original, err := get()
		if err != nil {
			return fmt.Errorf("failed to decode %s: %w", field, err)
		}
		if bytes.Equal(original, value) {
			set(value)
			return nil
		}
	}
	clear()
	return nil
}
