package state

import (
	"encoding/hex"
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
		Flags:   decoded.Flags,
		decoded: decoded,
	}
	if decoded.HasIssuer() {
		var err error
		issuance.Issuer, err = decoded.GetIssuer()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.Issuer: %w", err)
		}
	}
	if decoded.HasSequence() {
		issuance.Sequence = decoded.Sequence
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
		value, err := decoded.GetMPTokenMetadata()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.MPTokenMetadata: %w", err)
		}
		issuance.MPTokenMetadata = strings.ToLower(hex.EncodeToString(value))
	}
	if decoded.HasDomainID() {
		value, err := decoded.GetDomainID()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.DomainID: %w", err)
		}
		encoded := strings.ToLower(hex.EncodeToString(value[:]))
		issuance.DomainID = &encoded
	}
	if decoded.HasReferenceHolding() {
		value, err := decoded.GetReferenceHolding()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.ReferenceHolding: %w", err)
		}
		encoded := strings.ToLower(hex.EncodeToString(value[:]))
		issuance.ReferenceHolding = &encoded
	}
	if decoded.HasImmutableFlags() {
		issuance.ImmutableFlags = decoded.ImmutableFlags
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
		value, err := decoded.GetSponsor()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.Sponsor: %w", err)
		}
		issuance.Sponsor, err = EncodeAccountID(value)
		if err != nil {
			return nil, fmt.Errorf("failed to encode MPTokenIssuance.Sponsor: %w", err)
		}
	}
	if decoded.HasPreviousTxnID() {
		var err error
		issuance.PreviousTxnID, err = decoded.GetPreviousTxnID()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.PreviousTxnID: %w", err)
		}
	}
	if decoded.HasPreviousTxnLgrSeq() {
		issuance.PreviousTxnLgrSeq = decoded.PreviousTxnLgrSeq
	}

	return issuance, nil
}

// SerializeMPTokenIssuance serializes an MPTokenIssuance to binary format.
func SerializeMPTokenIssuance(issuance *MPTokenIssuanceData) ([]byte, error) {
	entry := issuance.decoded
	var zeroIssuer [20]byte
	if issuance.Issuer != zeroIssuer || !entry.HasIssuer() {
		if err := entry.SetIssuerValue(issuance.Issuer); err != nil {
			return nil, fmt.Errorf("failed to encode MPTokenIssuance.Issuer: %w", err)
		}
	}
	entry.SetSequence(issuance.Sequence)
	entry.SetFlags(issuance.Flags)
	entry.SetOwnerNodeValue(issuance.OwnerNode)
	entry.SetOutstandingAmountValue(issuance.OutstandingAmount)
	if issuance.TransferFee != 0 || !entry.HasTransferFee() {
		entry.SetTransferFee(issuance.TransferFee)
	}
	if issuance.AssetScale != 0 || !entry.HasAssetScale() {
		entry.SetAssetScale(issuance.AssetScale)
	}
	if issuance.MaximumAmount != nil {
		entry.SetMaximumAmountValue(*issuance.MaximumAmount)
	} else if !entry.HasMaximumAmount() {
		entry.ClearMaximumAmount()
	}
	if issuance.LockedAmount != nil {
		entry.SetLockedAmountValue(*issuance.LockedAmount)
	} else if !entry.HasLockedAmount() {
		entry.ClearLockedAmount()
	}
	if issuance.MPTokenMetadata != "" {
		value, err := decodeMPTModelHex(issuance.MPTokenMetadata, "MPTokenIssuance.MPTokenMetadata", 0)
		if err != nil {
			return nil, err
		}
		entry.SetMPTokenMetadataValue(value)
	} else if !entry.HasMPTokenMetadata() {
		entry.ClearMPTokenMetadata()
	}
	if issuance.DomainID != nil {
		value, err := decodeMPTModelHex(*issuance.DomainID, "MPTokenIssuance.DomainID", 32)
		if err != nil {
			return nil, err
		}
		var id [32]byte
		copy(id[:], value)
		entry.SetDomainIDValue(id)
	} else if !entry.HasDomainID() {
		entry.ClearDomainID()
	}
	if issuance.ReferenceHolding != nil {
		value, err := decodeMPTModelHex(*issuance.ReferenceHolding, "MPTokenIssuance.ReferenceHolding", 32)
		if err != nil {
			return nil, err
		}
		var id [32]byte
		copy(id[:], value)
		entry.SetReferenceHoldingValue(id)
	} else if !entry.HasReferenceHolding() {
		entry.ClearReferenceHolding()
	}
	if issuance.ImmutableFlags != 0 || !entry.HasImmutableFlags() {
		entry.SetImmutableFlags(issuance.ImmutableFlags)
	}
	if len(issuance.IssuerEncryptionKey) != 0 {
		entry.SetIssuerEncryptionKeyValue(issuance.IssuerEncryptionKey)
	} else if !entry.HasIssuerEncryptionKey() {
		entry.ClearIssuerEncryptionKey()
	}
	if len(issuance.AuditorEncryptionKey) != 0 {
		entry.SetAuditorEncryptionKeyValue(issuance.AuditorEncryptionKey)
	} else if !entry.HasAuditorEncryptionKey() {
		entry.ClearAuditorEncryptionKey()
	}
	if issuance.ConfidentialOutstandingAmount != 0 || !entry.HasConfidentialOutstandingAmount() {
		entry.SetConfidentialOutstandingAmountValue(issuance.ConfidentialOutstandingAmount)
	}
	if issuance.Sponsor != "" {
		sponsor, err := DecodeAccountID(issuance.Sponsor)
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance.Sponsor: %w", err)
		}
		if err := entry.SetSponsorValue(sponsor); err != nil {
			return nil, fmt.Errorf("failed to encode MPTokenIssuance.Sponsor: %w", err)
		}
	} else if !entry.HasSponsor() {
		entry.ClearSponsor()
	}
	var zeroHash [32]byte
	if issuance.PreviousTxnID != zeroHash {
		entry.SetPreviousTxnIDValue(issuance.PreviousTxnID)
	}
	if issuance.PreviousTxnLgrSeq != 0 {
		entry.SetPreviousTxnLgrSeq(issuance.PreviousTxnLgrSeq)
	}

	return entry.Encode()
}

// ParseMPToken parses an MPToken ledger entry from binary data.
func ParseMPToken(data []byte) (*MPTokenData, error) {
	var decoded ledgerfields.MPToken
	if err := decoded.Decode(data); err != nil {
		return nil, err
	}
	token := &MPTokenData{
		Flags:   decoded.Flags,
		decoded: decoded,
	}
	if decoded.HasAccount() {
		var err error
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
		value, err := decoded.GetSponsor()
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPToken.Sponsor: %w", err)
		}
		token.Sponsor, err = EncodeAccountID(value)
		if err != nil {
			return nil, fmt.Errorf("failed to encode MPToken.Sponsor: %w", err)
		}
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
		token.ConfidentialBalanceVersion = decoded.ConfidentialBalanceVersion
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
		token.PreviousTxnLgrSeq = decoded.PreviousTxnLgrSeq
	}

	return token, nil
}

// SerializeMPToken serializes an MPToken to binary format.
func SerializeMPToken(token *MPTokenData) ([]byte, error) {
	entry := token.decoded
	var zeroAccount [20]byte
	if token.Account != zeroAccount || !entry.HasAccount() {
		if err := entry.SetAccountValue(token.Account); err != nil {
			return nil, fmt.Errorf("failed to encode MPToken.Account: %w", err)
		}
	}
	entry.SetFlags(token.Flags)
	entry.SetMPTokenIssuanceIDValue(token.MPTokenIssuanceID)
	entry.SetOwnerNodeValue(token.OwnerNode)
	entry.SetMPTAmountValue(token.MPTAmount)
	if token.LockedAmount != nil {
		entry.SetLockedAmountValue(*token.LockedAmount)
	} else if !entry.HasLockedAmount() {
		entry.ClearLockedAmount()
	}
	if token.Sponsor != "" {
		sponsor, err := DecodeAccountID(token.Sponsor)
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPToken.Sponsor: %w", err)
		}
		if err := entry.SetSponsorValue(sponsor); err != nil {
			return nil, fmt.Errorf("failed to encode MPToken.Sponsor: %w", err)
		}
	} else if !entry.HasSponsor() {
		entry.ClearSponsor()
	}
	if len(token.ConfidentialBalanceInbox) != 0 {
		entry.SetConfidentialBalanceInboxValue(token.ConfidentialBalanceInbox)
	} else if !entry.HasConfidentialBalanceInbox() {
		entry.ClearConfidentialBalanceInbox()
	}
	if len(token.ConfidentialBalanceSpending) != 0 {
		entry.SetConfidentialBalanceSpendingValue(token.ConfidentialBalanceSpending)
	} else if !entry.HasConfidentialBalanceSpending() {
		entry.ClearConfidentialBalanceSpending()
	}
	if token.ConfidentialBalanceVersion != 0 || !entry.HasConfidentialBalanceVersion() {
		entry.SetConfidentialBalanceVersion(token.ConfidentialBalanceVersion)
	}
	if len(token.IssuerEncryptedBalance) != 0 {
		entry.SetIssuerEncryptedBalanceValue(token.IssuerEncryptedBalance)
	} else if !entry.HasIssuerEncryptedBalance() {
		entry.ClearIssuerEncryptedBalance()
	}
	if len(token.AuditorEncryptedBalance) != 0 {
		entry.SetAuditorEncryptedBalanceValue(token.AuditorEncryptedBalance)
	} else if !entry.HasAuditorEncryptedBalance() {
		entry.ClearAuditorEncryptedBalance()
	}
	if len(token.HolderEncryptionKey) != 0 {
		entry.SetHolderEncryptionKeyValue(token.HolderEncryptionKey)
	} else if !entry.HasHolderEncryptionKey() {
		entry.ClearHolderEncryptionKey()
	}
	if token.PreviousTxnID != ([32]byte{}) {
		entry.SetPreviousTxnIDValue(token.PreviousTxnID)
	}
	if token.PreviousTxnLgrSeq != 0 {
		entry.SetPreviousTxnLgrSeq(token.PreviousTxnLgrSeq)
	}

	return entry.Encode()
}

func decodeMPTModelHex(value, field string, wantLen int) ([]byte, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("failed to encode %s: %w", field, err)
	}
	if wantLen != 0 && len(decoded) != wantLen {
		return nil, fmt.Errorf("failed to encode %s: got %d bytes, want %d", field, len(decoded), wantLen)
	}
	return decoded, nil
}
