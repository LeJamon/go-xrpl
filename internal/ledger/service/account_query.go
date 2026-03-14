package service

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math"
	"sort"
	"strconv"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/credential"
	"github.com/LeJamon/goXRPLd/keylet"
)

// AccountInfoResult contains account information from the ledger
type AccountInfoResult struct {
	Account           string
	Balance           uint64
	Flags             uint32
	OwnerCount        uint32
	Sequence          uint32
	RegularKey        string
	Domain            string
	EmailHash         string
	TransferRate      uint32
	TickSize          uint8
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
	LedgerIndex       uint32
	LedgerHash        [32]byte
	Validated         bool
	RawData           []byte   // Raw SLE binary for full deserialization
	Index             [32]byte // SLE key (keylet hash)
}

// GetAccountInfo retrieves account information from the ledger
func (s *Service) GetAccountInfo(account string, ledgerIndex string) (*AccountInfoResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Determine which ledger to use
	var targetLedger *ledger.Ledger
	var validated bool

	switch ledgerIndex {
	case "current", "":
		targetLedger = s.openLedger
		validated = false
	case "closed":
		targetLedger = s.closedLedger
		validated = s.closedLedger == s.validatedLedger
	case "validated":
		targetLedger = s.validatedLedger
		validated = true
	default:
		// Try to parse as a number
		seq, err := strconv.ParseUint(ledgerIndex, 10, 32)
		if err != nil {
			return nil, errors.New("invalid ledger_index")
		}
		var ok bool
		targetLedger, ok = s.ledgerHistory[uint32(seq)]
		if !ok {
			return nil, ErrLedgerNotFound
		}
		validated = targetLedger.IsValidated()
	}

	if targetLedger == nil {
		return nil, ErrNoOpenLedger
	}

	// Decode the account address to get the account ID
	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil {
		return nil, errors.New("invalid account address: " + err.Error())
	}

	var accountID [20]byte
	copy(accountID[:], accountIDBytes)

	// Get the account keylet
	accountKey := keylet.Account(accountID)

	// Check if account exists
	exists, err := targetLedger.Exists(accountKey)
	if err != nil {
		return nil, errors.New("failed to check account existence: " + err.Error())
	}
	if !exists {
		return nil, errors.New("account not found")
	}

	// Read the account data
	data, err := targetLedger.Read(accountKey)
	if err != nil {
		return nil, errors.New("failed to read account: " + err.Error())
	}

	// Parse the account root
	accountRoot, err := state.ParseAccountRootFromBytes(data)
	if err != nil {
		return nil, errors.New("failed to parse account data: " + err.Error())
	}

	return &AccountInfoResult{
		Account:           account,
		Balance:           accountRoot.Balance,
		Flags:             accountRoot.Flags,
		OwnerCount:        accountRoot.OwnerCount,
		Sequence:          accountRoot.Sequence,
		RegularKey:        accountRoot.RegularKey,
		Domain:            accountRoot.Domain,
		EmailHash:         accountRoot.EmailHash,
		TransferRate:      accountRoot.TransferRate,
		TickSize:          accountRoot.TickSize,
		PreviousTxnID:     accountRoot.PreviousTxnID,
		PreviousTxnLgrSeq: accountRoot.PreviousTxnLgrSeq,
		LedgerIndex:       targetLedger.Sequence(),
		LedgerHash:        targetLedger.Hash(),
		Validated:         validated,
		RawData:           data,
		Index:             accountKey.Key,
	}, nil
}

// TrustLine represents a trust line from account_lines RPC
type TrustLine struct {
	Account        string `json:"account"`
	Balance        string `json:"balance"`
	Currency       string `json:"currency"`
	Limit          string `json:"limit"`
	LimitPeer      string `json:"limit_peer"`
	QualityIn      uint32 `json:"quality_in,omitempty"`
	QualityOut     uint32 `json:"quality_out,omitempty"`
	NoRipple       bool   `json:"no_ripple,omitempty"`
	NoRipplePeer   bool   `json:"no_ripple_peer,omitempty"`
	Authorized     bool   `json:"authorized,omitempty"`
	PeerAuthorized bool   `json:"peer_authorized,omitempty"`
	Freeze         bool   `json:"freeze,omitempty"`
	FreezePeer     bool   `json:"freeze_peer,omitempty"`
}

// AccountLinesResult contains the result of account_lines RPC
type AccountLinesResult struct {
	Account     string      `json:"account"`
	Lines       []TrustLine `json:"lines"`
	LedgerIndex uint32      `json:"ledger_index"`
	LedgerHash  [32]byte    `json:"ledger_hash"`
	Validated   bool        `json:"validated"`
	Marker      string      `json:"marker,omitempty"`
}

// GetAccountLines retrieves trust lines for an account
func (s *Service) GetAccountLines(account string, ledgerIndex string, peer string, limit uint32) (*AccountLinesResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Determine which ledger to use
	targetLedger, validated, err := s.getLedgerForQuery(ledgerIndex)
	if err != nil {
		return nil, err
	}

	// Decode the account address
	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil {
		return nil, errors.New("invalid account address: " + err.Error())
	}
	var accountID [20]byte
	copy(accountID[:], accountIDBytes)

	// Parse peer if provided
	var peerID [20]byte
	hasPeer := false
	if peer != "" {
		_, peerIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(peer)
		if err != nil {
			return nil, errors.New("invalid peer address: " + err.Error())
		}
		copy(peerID[:], peerIDBytes)
		hasPeer = true
	}

	// Set default limit
	if limit == 0 || limit > 400 {
		limit = 200
	}

	// Collect trust lines by iterating through ledger entries
	var lines []TrustLine

	targetLedger.ForEach(func(key [32]byte, data []byte) bool {
		// Check if we've reached the limit
		if uint32(len(lines)) >= limit {
			return false
		}

		// Check if this is a RippleState entry (trust line)
		if len(data) < 3 {
			return true
		}

		// Check LedgerEntryType field
		if data[0] != 0x11 { // UInt16 type code 1, field code 1
			return true
		}
		entryType := uint16(data[1])<<8 | uint16(data[2])
		if entryType != 0x0072 { // RippleState type
			return true
		}

		// Parse the RippleState
		rs, err := state.ParseRippleStateFromBytes(data)
		if err != nil {
			return true
		}

		// Check if this trust line involves our account
		lowID, _ := decodeAccountIDLocal(rs.LowLimit.Issuer)
		highID, _ := decodeAccountIDLocal(rs.HighLimit.Issuer)

		var isLowAccount bool
		var peerAccount string

		if lowID == accountID {
			isLowAccount = true
			peerAccount = rs.HighLimit.Issuer
		} else if highID == accountID {
			isLowAccount = false
			peerAccount = rs.LowLimit.Issuer
		} else {
			return true // Not our account
		}

		// Filter by peer if specified
		if hasPeer {
			peerAccountID, _ := decodeAccountIDLocal(peerAccount)
			if peerAccountID != peerID {
				return true
			}
		}

		// Build trust line response
		line := TrustLine{
			Account:  peerAccount,
			Currency: rs.Balance.Currency,
		}

		// Calculate balance from perspective of our account
		// Positive balance means peer owes us, negative means we owe peer
		if isLowAccount {
			// We are low account
			// Balance is positive if low owes high (we owe them) -> negative for us
			// Balance is negative if high owes low (they owe us) -> positive for us
			line.Balance = rs.Balance.Negate().Value()
			line.Limit = rs.LowLimit.Value()
			line.LimitPeer = rs.HighLimit.Value()
			line.NoRipple = (rs.Flags & 0x00020000) != 0       // lsfLowNoRipple
			line.NoRipplePeer = (rs.Flags & 0x00040000) != 0   // lsfHighNoRipple
			line.Authorized = (rs.Flags & 0x00010000) != 0     // lsfLowAuth
			line.PeerAuthorized = (rs.Flags & 0x00080000) != 0 // lsfHighAuth
			line.Freeze = (rs.Flags & 0x00400000) != 0         // lsfLowFreeze
			line.FreezePeer = (rs.Flags & 0x00800000) != 0     // lsfHighFreeze
		} else {
			// We are high account
			line.Balance = rs.Balance.Value()
			line.Limit = rs.HighLimit.Value()
			line.LimitPeer = rs.LowLimit.Value()
			line.NoRipple = (rs.Flags & 0x00040000) != 0       // lsfHighNoRipple
			line.NoRipplePeer = (rs.Flags & 0x00020000) != 0   // lsfLowNoRipple
			line.Authorized = (rs.Flags & 0x00080000) != 0     // lsfHighAuth
			line.PeerAuthorized = (rs.Flags & 0x00010000) != 0 // lsfLowAuth
			line.Freeze = (rs.Flags & 0x00800000) != 0         // lsfHighFreeze
			line.FreezePeer = (rs.Flags & 0x00400000) != 0     // lsfLowFreeze
		}

		line.QualityIn = rs.LowQualityIn
		line.QualityOut = rs.LowQualityOut

		lines = append(lines, line)
		return true
	})

	return &AccountLinesResult{
		Account:     account,
		Lines:       lines,
		LedgerIndex: targetLedger.Sequence(),
		LedgerHash:  targetLedger.Hash(),
		Validated:   validated,
	}, nil
}

// AccountOffer represents an offer from account_offers RPC
type AccountOffer struct {
	Flags      uint32      `json:"flags"`
	Seq        uint32      `json:"seq"`
	TakerGets  interface{} `json:"taker_gets"`
	TakerPays  interface{} `json:"taker_pays"`
	Quality    string      `json:"quality"`
	Expiration uint32      `json:"expiration,omitempty"`
}

// AccountOffersResult contains the result of account_offers RPC
type AccountOffersResult struct {
	Account     string         `json:"account"`
	Offers      []AccountOffer `json:"offers"`
	LedgerIndex uint32         `json:"ledger_index"`
	LedgerHash  [32]byte       `json:"ledger_hash"`
	Validated   bool           `json:"validated"`
	Marker      string         `json:"marker,omitempty"`
}

// GetAccountOffers retrieves offers for an account
func (s *Service) GetAccountOffers(account string, ledgerIndex string, limit uint32) (*AccountOffersResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Determine which ledger to use
	targetLedger, validated, err := s.getLedgerForQuery(ledgerIndex)
	if err != nil {
		return nil, err
	}

	// Decode the account address
	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil {
		return nil, errors.New("invalid account address: " + err.Error())
	}
	var accountID [20]byte
	copy(accountID[:], accountIDBytes)

	// Set default limit
	if limit == 0 || limit > 400 {
		limit = 200
	}

	// Collect offers by iterating through ledger entries
	var offers []AccountOffer

	targetLedger.ForEach(func(key [32]byte, data []byte) bool {
		// Check if we've reached the limit
		if uint32(len(offers)) >= limit {
			return false
		}

		// Check if this is an Offer entry
		if len(data) < 3 {
			return true
		}

		// Check LedgerEntryType field
		if data[0] != 0x11 { // UInt16 type code 1, field code 1
			return true
		}
		entryType := uint16(data[1])<<8 | uint16(data[2])
		if entryType != 0x006F { // Offer type
			return true
		}

		// Parse the Offer
		offer, err := state.ParseLedgerOfferFromBytes(data)
		if err != nil {
			return true
		}

		// Check if this offer belongs to our account
		offerAccountID, _ := decodeAccountIDLocal(offer.Account)
		if offerAccountID != accountID {
			return true
		}

		// Build offer response
		accountOffer := AccountOffer{
			Flags: offer.Flags,
			Seq:   offer.Sequence,
		}

		// Format TakerGets
		if offer.TakerGets.IsNative() {
			accountOffer.TakerGets = offer.TakerGets.Value()
		} else {
			accountOffer.TakerGets = map[string]string{
				"currency": offer.TakerGets.Currency,
				"issuer":   offer.TakerGets.Issuer,
				"value":    offer.TakerGets.Value(),
			}
		}

		// Format TakerPays
		if offer.TakerPays.IsNative() {
			accountOffer.TakerPays = offer.TakerPays.Value()
		} else {
			accountOffer.TakerPays = map[string]string{
				"currency": offer.TakerPays.Currency,
				"issuer":   offer.TakerPays.Issuer,
				"value":    offer.TakerPays.Value(),
			}
		}

		// Calculate quality
		accountOffer.Quality = calculateOfferQuality(offer.TakerPays, offer.TakerGets)

		if offer.Expiration > 0 {
			accountOffer.Expiration = offer.Expiration
		}

		offers = append(offers, accountOffer)
		return true
	})

	return &AccountOffersResult{
		Account:     account,
		Offers:      offers,
		LedgerIndex: targetLedger.Sequence(),
		LedgerHash:  targetLedger.Hash(),
		Validated:   validated,
	}, nil
}

// AccountObjectsResult contains account objects
type AccountObjectsResult struct {
	Account        string              `json:"account"`
	AccountObjects []AccountObjectItem `json:"account_objects"`
	LedgerIndex    uint32              `json:"ledger_index"`
	LedgerHash     [32]byte            `json:"ledger_hash"`
	Validated      bool                `json:"validated"`
	Marker         string              `json:"marker,omitempty"`
}

// AccountObjectItem represents an account object
type AccountObjectItem struct {
	Index           string `json:"index"`
	LedgerEntryType string `json:"LedgerEntryType"`
	Data            []byte `json:"data"`
}

// GetAccountObjects retrieves all objects owned by an account
func (s *Service) GetAccountObjects(account string, ledgerIndex string, objType string, limit uint32) (*AccountObjectsResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	targetLedger, validated, err := s.getLedgerForQuery(ledgerIndex)
	if err != nil {
		return nil, err
	}

	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil {
		return nil, errors.New("invalid account address: " + err.Error())
	}
	var accountID [20]byte
	copy(accountID[:], accountIDBytes)

	// Normalize type filter from rippled's snake_case to PascalCase
	objType = normalizeObjectType(objType)

	if limit == 0 || limit > 400 {
		limit = 200
	}

	result := &AccountObjectsResult{
		Account:        account,
		AccountObjects: make([]AccountObjectItem, 0),
		LedgerIndex:    targetLedger.Sequence(),
		LedgerHash:     targetLedger.Hash(),
		Validated:      validated,
	}

	// Iterate through ledger and find objects for this account
	count := uint32(0)
	targetLedger.ForEach(func(key [32]byte, data []byte) bool {
		if count >= limit {
			return false
		}

		// Check if this object belongs to the account
		entryType := getLedgerEntryType(data)
		if entryType == "" {
			return true
		}

		// Filter by type if specified
		if objType != "" && entryType != objType {
			return true
		}

		// Check if object is associated with the account
		if !isObjectForAccount(data, accountID, entryType) {
			return true
		}

		result.AccountObjects = append(result.AccountObjects, AccountObjectItem{
			Index:           formatHashHex(key),
			LedgerEntryType: entryType,
			Data:            data,
		})
		count++
		return true
	})

	return result, nil
}

// AccountChannel represents a payment channel for account_channels RPC
type AccountChannel struct {
	ChannelID          string `json:"channel_id"`
	Account            string `json:"account"`
	DestinationAccount string `json:"destination_account"`
	Amount             string `json:"amount"`
	Balance            string `json:"balance"`
	SettleDelay        uint32 `json:"settle_delay"`
	PublicKey          string `json:"public_key,omitempty"`
	PublicKeyHex       string `json:"public_key_hex,omitempty"`
	Expiration         uint32 `json:"expiration,omitempty"`
	CancelAfter        uint32 `json:"cancel_after,omitempty"`
	SourceTag          uint32 `json:"source_tag,omitempty"`
	DestinationTag     uint32 `json:"destination_tag,omitempty"`
	HasSourceTag       bool   `json:"-"`
	HasDestTag         bool   `json:"-"`
}

// AccountChannelsResult contains the result of account_channels RPC
type AccountChannelsResult struct {
	Account     string           `json:"account"`
	Channels    []AccountChannel `json:"channels"`
	LedgerIndex uint32           `json:"ledger_index"`
	LedgerHash  [32]byte         `json:"ledger_hash"`
	Validated   bool             `json:"validated"`
	Marker      string           `json:"marker,omitempty"`
	Limit       uint32           `json:"limit,omitempty"`
}

// GetAccountChannels retrieves payment channels for an account
func (s *Service) GetAccountChannels(account string, destinationAccount string, ledgerIndex string, limit uint32) (*AccountChannelsResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Determine which ledger to use
	targetLedger, validated, err := s.getLedgerForQuery(ledgerIndex)
	if err != nil {
		return nil, err
	}

	// Decode the account address
	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil {
		return nil, errors.New("invalid account address: " + err.Error())
	}
	var accountID [20]byte
	copy(accountID[:], accountIDBytes)

	// Check if account exists
	accountKey := keylet.Account(accountID)
	exists, err := targetLedger.Exists(accountKey)
	if err != nil {
		return nil, errors.New("failed to check account existence: " + err.Error())
	}
	if !exists {
		return nil, errors.New("account not found")
	}

	// Parse destination account if provided
	var destID [20]byte
	hasDestFilter := false
	if destinationAccount != "" {
		_, destIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(destinationAccount)
		if err != nil {
			return nil, errors.New("invalid destination_account address: " + err.Error())
		}
		copy(destID[:], destIDBytes)
		hasDestFilter = true
	}

	// Set default limit (matching rippled's accountChannels tuning)
	if limit == 0 || limit > 400 {
		limit = 256
	}

	// Collect payment channels by iterating through ledger entries
	var channels []AccountChannel

	targetLedger.ForEach(func(key [32]byte, data []byte) bool {
		// Check if we've reached the limit
		if uint32(len(channels)) >= limit {
			return false
		}

		// Check if this is a PayChannel entry
		if len(data) < 3 {
			return true
		}

		// Check LedgerEntryType field
		if data[0] != 0x11 { // UInt16 type code 1, field code 1
			return true
		}
		entryType := uint16(data[1])<<8 | uint16(data[2])
		if entryType != 0x0078 { // PayChannel type
			return true
		}

		// Parse the PayChannel
		payChan, err := state.ParsePayChannelFromBytes(data)
		if err != nil {
			return true
		}

		// Check if this channel belongs to our account (as source)
		if payChan.Account != accountID {
			return true
		}

		// Filter by destination account if specified
		if hasDestFilter && payChan.DestinationID != destID {
			return true
		}

		// Build channel response
		srcAddr, _ := addresscodec.EncodeAccountIDToClassicAddress(payChan.Account[:])
		destAddr, _ := addresscodec.EncodeAccountIDToClassicAddress(payChan.DestinationID[:])

		channel := AccountChannel{
			ChannelID:          formatHashHex(key),
			Account:            srcAddr,
			DestinationAccount: destAddr,
			Amount:             strconv.FormatUint(payChan.Amount, 10),
			Balance:            strconv.FormatUint(payChan.Balance, 10),
			SettleDelay:        payChan.SettleDelay,
		}

		// Add public key if present
		if payChan.PublicKey != "" {
			channel.PublicKeyHex = payChan.PublicKey
			// Convert hex to base58 for public_key field
			pkBytes, err := hexDecode(payChan.PublicKey)
			if err == nil && len(pkBytes) > 0 {
				if encoded, encErr := addresscodec.EncodeNodePublicKey(pkBytes); encErr == nil {
					channel.PublicKey = encoded
				}
			}
		}

		// Add optional fields
		if payChan.Expiration > 0 {
			channel.Expiration = payChan.Expiration
		}
		if payChan.CancelAfter > 0 {
			channel.CancelAfter = payChan.CancelAfter
		}
		if payChan.HasSourceTag {
			channel.SourceTag = payChan.SourceTag
			channel.HasSourceTag = true
		}
		if payChan.HasDestTag {
			channel.DestinationTag = payChan.DestinationTag
			channel.HasDestTag = true
		}

		channels = append(channels, channel)
		return true
	})

	return &AccountChannelsResult{
		Account:     account,
		Channels:    channels,
		LedgerIndex: targetLedger.Sequence(),
		LedgerHash:  targetLedger.Hash(),
		Validated:   validated,
	}, nil
}

// AccountCurrenciesResult contains the result of account_currencies RPC
type AccountCurrenciesResult struct {
	ReceiveCurrencies []string `json:"receive_currencies"`
	SendCurrencies    []string `json:"send_currencies"`
	LedgerIndex       uint32   `json:"ledger_index"`
	LedgerHash        [32]byte `json:"ledger_hash"`
	Validated         bool     `json:"validated"`
}

// GetAccountCurrencies retrieves currencies an account can send and receive
func (s *Service) GetAccountCurrencies(account string, ledgerIndex string) (*AccountCurrenciesResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Determine which ledger to use
	targetLedger, validated, err := s.getLedgerForQuery(ledgerIndex)
	if err != nil {
		return nil, err
	}

	// Decode the account address
	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil {
		return nil, errors.New("invalid account address: " + err.Error())
	}
	var accountID [20]byte
	copy(accountID[:], accountIDBytes)

	// Check if account exists
	accountKey := keylet.Account(accountID)
	exists, err := targetLedger.Exists(accountKey)
	if err != nil {
		return nil, errors.New("failed to check account existence: " + err.Error())
	}
	if !exists {
		return nil, errors.New("account not found")
	}

	// Use maps to collect unique currencies
	receiveCurrencies := make(map[string]bool)
	sendCurrencies := make(map[string]bool)

	targetLedger.ForEach(func(key [32]byte, data []byte) bool {
		// Check if this is a RippleState entry (trust line)
		if len(data) < 3 {
			return true
		}

		// Check LedgerEntryType field
		if data[0] != 0x11 { // UInt16 type code 1, field code 1
			return true
		}
		entryType := uint16(data[1])<<8 | uint16(data[2])
		if entryType != 0x0072 { // RippleState type
			return true
		}

		// Parse the RippleState
		rs, err := state.ParseRippleStateFromBytes(data)
		if err != nil {
			return true
		}

		// Check if this trust line involves our account
		lowID, _ := decodeAccountIDLocal(rs.LowLimit.Issuer)
		highID, _ := decodeAccountIDLocal(rs.HighLimit.Issuer)

		var isLowAccount bool

		if lowID == accountID {
			isLowAccount = true
		} else if highID == accountID {
			isLowAccount = false
		} else {
			return true // Not our account
		}

		currency := rs.Balance.Currency

		// Determine if account can receive and/or send this currency
		// Based on the balance value and limits
		if isLowAccount {
			// We are low account
			// Balance in RippleState: positive = low owes high (negative balance from our perspective)
			// Our perspective balance: negative of rs.Balance
			// receive: if our limit > 0 and balance < limit (there's room to receive)
			// send: if our perspective balance > 0 (we hold some of this currency)

			// Balance value: positive means low owes high
			// From low's perspective: balance is negated
			// If rs.Balance > 0, we owe peer (balance < 0 from our view)
			// If rs.Balance < 0, peer owes us (balance > 0 from our view)

			// Our limit is rs.LowLimit
			// We can receive if: limit > balance from our perspective
			// From our perspective: balance = -rs.Balance
			// So we can receive if: limit > -rs.Balance
			// i.e., limit + rs.Balance > 0

			limit := rs.LowLimit
			balance := rs.Balance.Negate() // Our perspective

			// Can receive if limit > balance (more room to receive)
			if limit.Signum() > 0 && limit.Compare(balance) > 0 {
				receiveCurrencies[currency] = true
			}

			// Can send if balance > 0 (we have some to send)
			if balance.Signum() > 0 {
				sendCurrencies[currency] = true
			}
		} else {
			// We are high account
			// Balance value: positive means low owes high (we hold IOU from low's perspective)
			// From high's perspective: balance = rs.Balance (same sign)

			// Our limit is rs.HighLimit
			// We can receive if: limit > balance
			// We can send if balance > 0

			limit := rs.HighLimit
			balance := rs.Balance

			// Can receive if limit > balance (more room to receive)
			if limit.Signum() > 0 && limit.Compare(balance) > 0 {
				receiveCurrencies[currency] = true
			}

			// Can send if balance > 0 (we have some to send)
			if balance.Signum() > 0 {
				sendCurrencies[currency] = true
			}
		}

		return true
	})

	// Convert maps to sorted slices
	receiveList := make([]string, 0, len(receiveCurrencies))
	for currency := range receiveCurrencies {
		receiveList = append(receiveList, currency)
	}
	// Sort for consistent output
	sortStrings(receiveList)

	sendList := make([]string, 0, len(sendCurrencies))
	for currency := range sendCurrencies {
		sendList = append(sendList, currency)
	}
	sortStrings(sendList)

	return &AccountCurrenciesResult{
		ReceiveCurrencies: receiveList,
		SendCurrencies:    sendList,
		LedgerIndex:       targetLedger.Sequence(),
		LedgerHash:        targetLedger.Hash(),
		Validated:         validated,
	}, nil
}

// sortStrings sorts a slice of strings in place
func sortStrings(s []string) {
	for i := 0; i < len(s)-1; i++ {
		for j := i + 1; j < len(s); j++ {
			if s[i] > s[j] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

// NFTInfo represents an individual NFT
type NFTInfo struct {
	Flags        uint16
	Issuer       string
	NFTokenID    string
	NFTokenTaxon uint32
	URI          string
	NFTSerial    uint32
	TransferFee  uint16
}

// AccountNFTsResult contains the result of account_nfts RPC
type AccountNFTsResult struct {
	Account     string
	AccountNFTs []NFTInfo
	LedgerIndex uint32
	LedgerHash  [32]byte
	Validated   bool
	Marker      string
}

// GetAccountNFTs retrieves NFTs owned by an account
func (s *Service) GetAccountNFTs(account string, ledgerIndex string, limit uint32) (*AccountNFTsResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Determine which ledger to use
	targetLedger, validated, err := s.getLedgerForQuery(ledgerIndex)
	if err != nil {
		return nil, err
	}

	// Decode the account address
	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil {
		return nil, errors.New("invalid account address: " + err.Error())
	}
	var accountID [20]byte
	copy(accountID[:], accountIDBytes)

	// Check if account exists
	accountKey := keylet.Account(accountID)
	exists, err := targetLedger.Exists(accountKey)
	if err != nil {
		return nil, errors.New("failed to check account existence: " + err.Error())
	}
	if !exists {
		return nil, errors.New("account not found")
	}

	// Set default limit
	if limit == 0 || limit > 400 {
		limit = 256
	}

	// Collect NFTs by iterating through ledger entries looking for NFTokenPage objects
	var nfts []NFTInfo

	targetLedger.ForEach(func(key [32]byte, data []byte) bool {
		// Check if we've reached the limit
		if uint32(len(nfts)) >= limit {
			return false
		}

		// Check if this is an NFTokenPage entry
		if len(data) < 3 {
			return true
		}

		// Check LedgerEntryType field
		if data[0] != 0x11 { // UInt16 type code 1, field code 1
			return true
		}
		entryType := uint16(data[1])<<8 | uint16(data[2])
		if entryType != 0x0050 { // NFTokenPage type
			return true
		}

		// Check if this NFTokenPage belongs to our account
		// NFTokenPage key includes the account ID in bytes 0-19
		var pageOwner [20]byte
		copy(pageOwner[:], key[0:20])
		if pageOwner != accountID {
			return true
		}

		// Parse the NFTokenPage
		page, err := state.ParseNFTokenPageFromBytes(data)
		if err != nil {
			return true
		}

		// Extract NFTs from the page
		for _, token := range page.NFTokens {
			if uint32(len(nfts)) >= limit {
				break
			}

			nft := extractNFTInfo(token.NFTokenID, token.URI)
			nfts = append(nfts, nft)
		}

		return true
	})

	return &AccountNFTsResult{
		Account:     account,
		AccountNFTs: nfts,
		LedgerIndex: targetLedger.Sequence(),
		LedgerHash:  targetLedger.Hash(),
		Validated:   validated,
	}, nil
}

// CurrencyBalance represents a currency balance for gateway_balances
type CurrencyBalance struct {
	Currency string
	Value    string
}

// GatewayBalancesResult contains the result of gateway_balances RPC
type GatewayBalancesResult struct {
	Account        string
	Obligations    map[string]string            // currency -> value
	Balances       map[string][]CurrencyBalance // account -> []balance (hotwallets)
	FrozenBalances map[string][]CurrencyBalance // account -> []balance
	Assets         map[string][]CurrencyBalance // account -> []balance
	Locked         map[string]string            // currency -> value (escrows)
	LedgerIndex    uint32
	LedgerHash     [32]byte
	Validated      bool
}

// GetGatewayBalances retrieves obligations and balances for a gateway account
func (s *Service) GetGatewayBalances(account string, hotWallets []string, ledgerIndex string) (*GatewayBalancesResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Determine which ledger to use
	targetLedger, validated, err := s.getLedgerForQuery(ledgerIndex)
	if err != nil {
		return nil, err
	}

	// Decode the account address
	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil {
		return nil, errors.New("invalid account address: " + err.Error())
	}
	var accountID [20]byte
	copy(accountID[:], accountIDBytes)

	// Check if account exists
	accountKey := keylet.Account(accountID)
	exists, err := targetLedger.Exists(accountKey)
	if err != nil {
		return nil, errors.New("failed to check account existence: " + err.Error())
	}
	if !exists {
		return nil, errors.New("account not found")
	}

	// Parse hot wallet addresses
	hotWalletIDs := make(map[[20]byte]bool)
	for _, hw := range hotWallets {
		_, hwIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(hw)
		if err != nil {
			return nil, errors.New("invalid hotwallet address: " + hw)
		}
		var hwID [20]byte
		copy(hwID[:], hwIDBytes)
		hotWalletIDs[hwID] = true
	}

	// Maps to collect results
	obligations := make(map[string]tx.Amount)         // currency -> total obligations
	hotBalances := make(map[string][]CurrencyBalance) // account -> balances
	frozenBalances := make(map[string][]CurrencyBalance)
	assets := make(map[string][]CurrencyBalance)

	// Iterate through all trust lines
	targetLedger.ForEach(func(key [32]byte, data []byte) bool {
		// Check if this is a RippleState entry
		if len(data) < 3 {
			return true
		}

		if data[0] != 0x11 { // UInt16 type code 1, field code 1
			return true
		}
		entryType := uint16(data[1])<<8 | uint16(data[2])
		if entryType != 0x0072 { // RippleState type
			return true
		}

		// Parse the RippleState
		rs, err := state.ParseRippleStateFromBytes(data)
		if err != nil {
			return true
		}

		// Check if this trust line involves our account
		lowID, _ := decodeAccountIDLocal(rs.LowLimit.Issuer)
		highID, _ := decodeAccountIDLocal(rs.HighLimit.Issuer)

		var isLowAccount bool
		var peerID [20]byte

		if lowID == accountID {
			isLowAccount = true
			peerID = highID
		} else if highID == accountID {
			isLowAccount = false
			peerID = lowID
		} else {
			return true // Not our account
		}

		// Check if balance is zero
		if rs.Balance.IsZero() {
			return true
		}

		// Get the balance from the gateway's perspective
		// RippleState balance: positive = low owes high, negative = high owes low
		// Gateway perspective: negative = we owe them (obligations), positive = they owe us (assets)
		var gatewayBalance tx.Amount
		if isLowAccount {
			// We are low, balance from our view is negated
			gatewayBalance = rs.Balance.Negate()
		} else {
			// We are high, balance from our view is the same
			gatewayBalance = rs.Balance
		}

		// Get peer address
		peerAddr, _ := addresscodec.EncodeAccountIDToClassicAddress(peerID[:])

		currency := rs.Balance.Currency

		// Determine if this is frozen
		var isFrozen bool
		if isLowAccount {
			// We are low, check if peer (high) has frozen us
			isFrozen = (rs.Flags & state.LsfHighFreeze) != 0
		} else {
			// We are high, check if peer (low) has frozen us
			isFrozen = (rs.Flags & state.LsfLowFreeze) != 0
		}

		// Check what category this balance falls into
		if hotWalletIDs[peerID] {
			// This is a hot wallet - add to balances
			// For hot wallets, we report the balance they hold (negated from gateway perspective)
			if gatewayBalance.Signum() < 0 {
				// Gateway owes hot wallet (hot wallet holds currency)
				balanceText := gatewayBalance.Negate().Value()
				hotBalances[peerAddr] = append(hotBalances[peerAddr], CurrencyBalance{
					Currency: currency,
					Value:    balanceText,
				})
			}
		} else if gatewayBalance.Signum() > 0 {
			// Gateway has a positive balance (holds currency from peer) - unusual, add to assets
			balanceText := gatewayBalance.Value()
			assets[peerAddr] = append(assets[peerAddr], CurrencyBalance{
				Currency: currency,
				Value:    balanceText,
			})
		} else if isFrozen {
			// This is a frozen obligation
			balanceText := gatewayBalance.Negate().Value()
			frozenBalances[peerAddr] = append(frozenBalances[peerAddr], CurrencyBalance{
				Currency: currency,
				Value:    balanceText,
			})
		} else {
			// Normal obligation - gateway owes this amount
			// Balance is negative (we owe them), so negate for the amount we owe
			owedAmount := gatewayBalance.Negate()
			if existing, ok := obligations[currency]; ok {
				sum, _ := existing.Add(owedAmount)
				obligations[currency] = sum
			} else {
				obligations[currency] = owedAmount
			}
		}

		return true
	})

	// Convert obligations map to string values
	obligationsStr := make(map[string]string)
	for curr, amt := range obligations {
		obligationsStr[curr] = amt.Value()
	}

	result := &GatewayBalancesResult{
		Account:     account,
		LedgerIndex: targetLedger.Sequence(),
		LedgerHash:  targetLedger.Hash(),
		Validated:   validated,
	}

	if len(obligationsStr) > 0 {
		result.Obligations = obligationsStr
	}
	if len(hotBalances) > 0 {
		result.Balances = hotBalances
	}
	if len(frozenBalances) > 0 {
		result.FrozenBalances = frozenBalances
	}
	if len(assets) > 0 {
		result.Assets = assets
	}

	return result, nil
}

// NoRippleCheckResult contains the result of noripple_check RPC
type NoRippleCheckResult struct {
	Problems     []string
	Transactions []SuggestedTransaction
	LedgerIndex  uint32
	LedgerHash   [32]byte
	Validated    bool
}

// SuggestedTransaction represents a suggested transaction to fix NoRipple issues
type SuggestedTransaction struct {
	TransactionType string
	Account         string
	Fee             string
	Sequence        uint32
	SetFlag         uint32
	Flags           uint32
	LimitAmount     map[string]interface{}
}

// TrustSet transaction flags for NoRipple
const (
	tfSetNoRipple   uint32 = 0x00020000
	tfClearNoRipple uint32 = 0x00040000
)

// GetNoRippleCheck checks trust lines for proper NoRipple flag settings
func (s *Service) GetNoRippleCheck(account string, role string, ledgerIndex string, limit uint32, transactions bool) (*NoRippleCheckResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Determine which ledger to use
	targetLedger, validated, err := s.getLedgerForQuery(ledgerIndex)
	if err != nil {
		return nil, err
	}

	// Decode the account address
	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil {
		return nil, errors.New("invalid account address: " + err.Error())
	}
	var accountID [20]byte
	copy(accountID[:], accountIDBytes)

	// Check if account exists and get its flags and sequence
	accountKey := keylet.Account(accountID)
	exists, err := targetLedger.Exists(accountKey)
	if err != nil {
		return nil, errors.New("failed to check account existence: " + err.Error())
	}
	if !exists {
		return nil, errors.New("account not found")
	}

	// Read the account data to get flags and sequence
	data, err := targetLedger.Read(accountKey)
	if err != nil {
		return nil, errors.New("failed to read account: " + err.Error())
	}

	accountRoot, err := state.ParseAccountRootFromBytes(data)
	if err != nil {
		return nil, errors.New("failed to parse account data: " + err.Error())
	}

	// Validate role
	roleGateway := role == "gateway"
	if !roleGateway && role != "user" {
		return nil, errors.New("invalid role: must be 'gateway' or 'user'")
	}

	// Set default limit
	if limit == 0 || limit > 400 {
		limit = 300
	}

	// Check DefaultRipple flag
	bDefaultRipple := (accountRoot.Flags & state.LsfDefaultRipple) != 0

	var problems []string
	var suggestedTxs []SuggestedTransaction
	seq := accountRoot.Sequence

	// Get base fee for suggested transactions
	baseFee, _, _ := s.GetCurrentFees()
	feeStr := strconv.FormatUint(baseFee, 10)

	// Check DefaultRipple setting based on role
	if bDefaultRipple && !roleGateway {
		problems = append(problems, "You appear to have set your default ripple flag even though you are not a gateway. This is not recommended unless you are experimenting")
	} else if roleGateway && !bDefaultRipple {
		problems = append(problems, "You should immediately set your default ripple flag")
		if transactions {
			suggestedTxs = append(suggestedTxs, SuggestedTransaction{
				TransactionType: "AccountSet",
				Account:         account,
				Fee:             feeStr,
				Sequence:        seq,
				SetFlag:         8, // asfDefaultRipple
			})
			seq++
		}
	}

	// Iterate through trust lines and check NoRipple settings
	problemCount := uint32(0)
	targetLedger.ForEach(func(key [32]byte, entryData []byte) bool {
		// Check if we've reached the limit
		if problemCount >= limit {
			return false
		}

		// Check if this is a RippleState entry
		if len(entryData) < 3 {
			return true
		}

		if entryData[0] != 0x11 { // UInt16 type code 1, field code 1
			return true
		}
		entryType := uint16(entryData[1])<<8 | uint16(entryData[2])
		if entryType != 0x0072 { // RippleState type
			return true
		}

		// Parse the RippleState
		rs, err := state.ParseRippleStateFromBytes(entryData)
		if err != nil {
			return true
		}

		// Check if this trust line involves our account
		lowID, _ := decodeAccountIDLocal(rs.LowLimit.Issuer)
		highID, _ := decodeAccountIDLocal(rs.HighLimit.Issuer)

		var isLowAccount bool
		var peerAccount string

		if lowID == accountID {
			isLowAccount = true
			peerAccount = rs.HighLimit.Issuer
		} else if highID == accountID {
			isLowAccount = false
			peerAccount = rs.LowLimit.Issuer
		} else {
			return true // Not our account
		}

		// Check NoRipple flag for this account's side
		var bNoRipple bool
		if isLowAccount {
			bNoRipple = (rs.Flags & state.LsfLowNoRipple) != 0
		} else {
			bNoRipple = (rs.Flags & state.LsfHighNoRipple) != 0
		}

		currency := rs.Balance.Currency

		// Determine if there's a problem
		var problem string
		needFix := false
		if bNoRipple && roleGateway {
			problem = "You should clear the no ripple flag on your " + currency + " line to " + peerAccount
			needFix = true
		} else if !roleGateway && !bNoRipple {
			problem = "You should probably set the no ripple flag on your " + currency + " line to " + peerAccount
			needFix = true
		}

		if needFix {
			problems = append(problems, problem)
			problemCount++

			if transactions {
				// Get our limit for this trust line
				var limitValue string
				if isLowAccount {
					limitValue = rs.LowLimit.Value()
				} else {
					limitValue = rs.HighLimit.Value()
				}

				// Build TrustSet transaction
				var flags uint32
				if bNoRipple {
					flags = tfClearNoRipple
				} else {
					flags = tfSetNoRipple
				}

				suggestedTxs = append(suggestedTxs, SuggestedTransaction{
					TransactionType: "TrustSet",
					Account:         account,
					Fee:             feeStr,
					Sequence:        seq,
					Flags:           flags,
					LimitAmount: map[string]interface{}{
						"currency": currency,
						"issuer":   peerAccount,
						"value":    limitValue,
					},
				})
				seq++
			}
		}

		return true
	})

	return &NoRippleCheckResult{
		Problems:     problems,
		Transactions: suggestedTxs,
		LedgerIndex:  targetLedger.Sequence(),
		LedgerHash:   targetLedger.Hash(),
		Validated:    validated,
	}, nil
}

// extractNFTInfo extracts NFT details from the NFTokenID
func extractNFTInfo(tokenID [32]byte, uri string) NFTInfo {
	// NFTokenID format (32 bytes):
	// Bytes 0-1: Flags (2 bytes, big endian)
	// Bytes 2-3: TransferFee (2 bytes, big endian)
	// Bytes 4-23: Issuer AccountID (20 bytes)
	// Bytes 24-27: Taxon (ciphered, 4 bytes, big endian)
	// Bytes 28-31: Sequence (4 bytes, big endian)

	flags := uint16(tokenID[0])<<8 | uint16(tokenID[1])
	transferFee := uint16(tokenID[2])<<8 | uint16(tokenID[3])

	var issuerID [20]byte
	copy(issuerID[:], tokenID[4:24])
	issuer, _ := addresscodec.EncodeAccountIDToClassicAddress(issuerID[:])

	cipheredTaxon := uint32(tokenID[24])<<24 | uint32(tokenID[25])<<16 | uint32(tokenID[26])<<8 | uint32(tokenID[27])
	sequence := uint32(tokenID[28])<<24 | uint32(tokenID[29])<<16 | uint32(tokenID[30])<<8 | uint32(tokenID[31])

	// Decipher the taxon using the same algorithm
	taxon := cipheredTaxon ^ ((sequence ^ 384160001) * 2357503715)

	return NFTInfo{
		Flags:        flags,
		Issuer:       issuer,
		NFTokenID:    formatHashHex(tokenID),
		NFTokenTaxon: taxon,
		URI:          uri,
		NFTSerial:    sequence,
		TransferFee:  transferFee,
	}
}

// DepositAuthorizedResult contains the result of deposit_authorized RPC
type DepositAuthorizedResult struct {
	SourceAccount      string
	DestinationAccount string
	DepositAuthorized  bool
	LedgerIndex        uint32
	LedgerHash         [32]byte
	Validated          bool
}

// GetDepositAuthorized checks if a source account is authorized to deposit to a destination account.
// When credentials are provided, they are validated against the ledger (existence,
// acceptance, expiry, ownership, duplicates) before checking credential-based
// deposit preauthorization.
// Reference: rippled DepositAuthorized.cpp doDepositAuthorized()
func (s *Service) GetDepositAuthorized(sourceAccount string, destinationAccount string, ledgerIndex string, credentials []string) (*DepositAuthorizedResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Determine which ledger to use
	targetLedger, validated, err := s.getLedgerForQuery(ledgerIndex)
	if err != nil {
		return nil, err
	}

	// Decode the source account address
	_, srcIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(sourceAccount)
	if err != nil {
		return nil, errors.New("invalid source_account address: " + err.Error())
	}
	var srcID [20]byte
	copy(srcID[:], srcIDBytes)

	// Decode the destination account address
	_, dstIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(destinationAccount)
	if err != nil {
		return nil, errors.New("invalid destination_account address: " + err.Error())
	}
	var dstID [20]byte
	copy(dstID[:], dstIDBytes)

	// Check if source account exists in the ledger
	srcKey := keylet.Account(srcID)
	exists, err := targetLedger.Exists(srcKey)
	if err != nil {
		return nil, errors.New("failed to check source account existence: " + err.Error())
	}
	if !exists {
		return nil, errors.New("source account not found")
	}

	// Check if destination account exists and get its flags
	dstKey := keylet.Account(dstID)
	exists, err = targetLedger.Exists(dstKey)
	if err != nil {
		return nil, errors.New("failed to check destination account existence: " + err.Error())
	}
	if !exists {
		return nil, errors.New("destination account not found")
	}

	// Read the destination account data to get flags
	dstData, err := targetLedger.Read(dstKey)
	if err != nil {
		return nil, errors.New("failed to read destination account: " + err.Error())
	}

	dstAccountRoot, err := state.ParseAccountRootFromBytes(dstData)
	if err != nil {
		return nil, errors.New("failed to parse destination account data: " + err.Error())
	}

	// Check if DepositAuth flag is set on destination
	depositAuthRequired := (dstAccountRoot.Flags & state.LsfDepositAuth) != 0

	// If source == destination, deposit is always authorized (self-deposit)
	sameAccount := srcID == dstID

	// Validate credentials on-ledger if provided.
	// Reference: rippled DepositAuthorized.cpp credential validation loop
	var sortedCredPairs []keylet.CredentialPair
	credentialsPresent := len(credentials) > 0
	if credentialsPresent {
		sortedCredPairs, err = validateCredentialsOnLedger(targetLedger, credentials, srcID)
		if err != nil {
			return nil, err
		}
	}

	// Determine authorization status
	depositAuthorized := true
	if depositAuthRequired && !sameAccount {
		// First check direct deposit preauth (account-to-account)
		depositPreauthKey := keylet.DepositPreauth(dstID, srcID)
		exists, err := targetLedger.Exists(depositPreauthKey)
		if err != nil {
			return nil, errors.New("failed to check deposit preauthorization: " + err.Error())
		}
		depositAuthorized = exists

		// If not directly authorized but credentials are present,
		// check credential-based deposit preauthorization.
		if !depositAuthorized && credentialsPresent {
			credPreauthKey := keylet.DepositPreauthCredentials(dstID, sortedCredPairs)
			exists, err := targetLedger.Exists(credPreauthKey)
			if err != nil {
				return nil, errors.New("failed to check credential deposit preauthorization: " + err.Error())
			}
			depositAuthorized = exists
		}
	}

	return &DepositAuthorizedResult{
		SourceAccount:      sourceAccount,
		DestinationAccount: destinationAccount,
		DepositAuthorized:  depositAuthorized,
		LedgerIndex:        targetLedger.Sequence(),
		LedgerHash:         targetLedger.Hash(),
		Validated:          validated,
	}, nil
}

// validateCredentialsOnLedger validates each credential ID against the ledger.
// It checks: existence, acceptance, expiry, ownership (subject == srcAcct), and
// detects duplicates (same issuer+credentialType pair).
// Returns the sorted credential pairs for use in credential-based preauth lookup.
// Errors are prefixed with "bad_credentials: " for the handler to map to rpcBAD_CREDENTIALS.
// Reference: rippled DepositAuthorized.cpp credential validation loop
func validateCredentialsOnLedger(targetLedger *ledger.Ledger, credentials []string, srcAcct [20]byte) ([]keylet.CredentialPair, error) {
	type credKey struct {
		issuer         [20]byte
		credentialType string
	}

	seen := make(map[credKey]struct{})
	pairs := make([]keylet.CredentialPair, 0, len(credentials))

	// Get parent close time for expiry check.
	// rippled uses ledger->info().parentCloseTime
	parentCloseTime := targetLedger.ParentCloseTime()
	parentCloseTimeSecs := uint32(parentCloseTime.Unix())
	// Clamp to max uint32 if negative (shouldn't happen, but be safe)
	if parentCloseTime.Unix() < 0 {
		parentCloseTimeSecs = 0
	}
	if parentCloseTime.Unix() > math.MaxUint32 {
		parentCloseTimeSecs = math.MaxUint32
	}

	for _, credHex := range credentials {
		// Decode the credential hash
		credHashBytes, err := hex.DecodeString(credHex)
		if err != nil {
			return nil, errors.New("bad_credentials: credentials don't exist")
		}
		var credHash [32]byte
		copy(credHash[:], credHashBytes)

		// Look up credential on ledger by its hash
		credKeylet := keylet.CredentialByID(credHash)
		credData, err := targetLedger.Read(credKeylet)
		if err != nil || credData == nil {
			return nil, errors.New("bad_credentials: credentials don't exist")
		}

		// Parse the credential entry
		credEntry, err := credential.ParseCredentialEntry(credData)
		if err != nil {
			return nil, errors.New("bad_credentials: credentials don't exist")
		}

		// Check accepted flag
		// Reference: rippled DepositAuthorized.cpp: if (!(sleCred->getFlags() & lsfAccepted))
		if !credEntry.IsAccepted() {
			return nil, errors.New("bad_credentials: credentials aren't accepted")
		}

		// Check expiry
		// Reference: rippled DepositAuthorized.cpp: if (credentials::checkExpired(sleCred, ...))
		if credEntry.Expiration != nil && parentCloseTimeSecs > *credEntry.Expiration {
			return nil, errors.New("bad_credentials: credentials are expired")
		}

		// Check ownership: subject must match source account
		// Reference: rippled DepositAuthorized.cpp: if ((*sleCred)[sfSubject] != srcAcct)
		if credEntry.Subject != srcAcct {
			return nil, errors.New("bad_credentials: credentials doesn't belong to the root account")
		}

		// Check for duplicates (same issuer + credentialType)
		// Reference: rippled DepositAuthorized.cpp: sorted.emplace(issuer, credType)
		key := credKey{
			issuer:         credEntry.Issuer,
			credentialType: string(credEntry.CredentialType),
		}
		if _, exists := seen[key]; exists {
			return nil, errors.New("bad_credentials: duplicates in credentials")
		}
		seen[key] = struct{}{}

		pairs = append(pairs, keylet.CredentialPair{
			Issuer:         credEntry.Issuer,
			CredentialType: credEntry.CredentialType,
		})
	}

	// Sort pairs by (issuer, credentialType) for deterministic keylet computation.
	// Reference: rippled uses std::set which sorts by (issuer, credType)
	sort.Slice(pairs, func(i, j int) bool {
		cmp := bytes.Compare(pairs[i].Issuer[:], pairs[j].Issuer[:])
		if cmp != 0 {
			return cmp < 0
		}
		return bytes.Compare(pairs[i].CredentialType, pairs[j].CredentialType) < 0
	})

	return pairs, nil
}
