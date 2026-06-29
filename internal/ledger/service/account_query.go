package service

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/credential"
	"github.com/LeJamon/go-xrpl/keylet"
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

// parseDirMarker decodes the resume marker shared by account_lines,
// account_offers and account_channels: "<entryKey>,<ownerNode>" — last entry's
// ledger key plus the owner-directory page it sat on. Empty means start from the
// beginning; malformed yields svcerr.ErrInvalidMarker.
func parseDirMarker(marker string) (afterKey [32]byte, page uint64, present bool, err error) {
	if marker == "" {
		return afterKey, 0, false, nil
	}
	keyStr, rest, found := strings.Cut(marker, ",")
	if !found {
		return afterKey, 0, false, svcerr.ErrInvalidMarker
	}
	var key [32]byte
	if derr := decodeHex32Into(keyStr, &key); derr != nil {
		return afterKey, 0, false, svcerr.ErrInvalidMarker
	}
	// hint is the second comma-delimited field; ignore trailing content.
	pageStr, _, _ := strings.Cut(rest, ",")
	p, perr := strconv.ParseUint(pageStr, 10, 64)
	if perr != nil {
		return afterKey, 0, false, svcerr.ErrInvalidMarker
	}
	return key, p, true, nil
}

func formatDirMarker(key [32]byte, page uint64) string {
	return formatHashHex(key) + "," + strconv.FormatUint(page, 10)
}

// withAccountQuery runs the shared account_* preamble — context check, read lock,
// ledger resolution, address decode — then invokes fn with the resolved ledger,
// account ID and validated flag while the read lock is held.
func withAccountQuery[T any](
	s *Service,
	ctx context.Context,
	account string,
	ledgerIndex string,
	fn func(targetLedger *ledger.Ledger, accountID [20]byte, validated bool) (T, error),
) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	// resolves shortcuts / numeric index / ledger_hash to the target ledger.
	targetLedger, validated, err := s.getLedgerForQuery(ledgerIndex)
	if err != nil {
		return zero, err
	}

	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil {
		return zero, fmt.Errorf("%w: %v", svcerr.ErrAccountMalformed, err)
	}
	var accountID [20]byte
	copy(accountID[:], accountIDBytes)

	return fn(targetLedger, accountID, validated)
}

// GetAccountInfo retrieves account information from the ledger
func (s *Service) GetAccountInfo(ctx context.Context, account string, ledgerIndex string) (*AccountInfoResult, error) {
	return withAccountQuery(s, ctx, account, ledgerIndex, func(targetLedger *ledger.Ledger, accountID [20]byte, validated bool) (*AccountInfoResult, error) {
		accountKey := keylet.Account(accountID)

		exists, err := targetLedger.Exists(accountKey)
		if err != nil {
			return nil, fmt.Errorf("failed to check account existence: %w", err)
		}
		if !exists {
			return nil, svcerr.ErrAccountNotFound
		}

		data, err := targetLedger.Read(accountKey)
		if err != nil {
			return nil, fmt.Errorf("failed to read account: %w", err)
		}

		accountRoot, err := state.ParseAccountRoot(data)
		if err != nil {
			return nil, fmt.Errorf("failed to parse account data: %w", err)
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
	})
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
func (s *Service) GetAccountLines(ctx context.Context, account string, ledgerIndex string, peer string, limit uint32, marker string) (*AccountLinesResult, error) {
	return withAccountQuery(s, ctx, account, ledgerIndex, func(targetLedger *ledger.Ledger, accountID [20]byte, validated bool) (*AccountLinesResult, error) {
		afterKey, hintPage, hasMarker, err := parseDirMarker(marker)
		if err != nil {
			return nil, err
		}

		var peerID [20]byte
		hasPeer := false
		if peer != "" {
			_, peerIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(peer)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid peer address: %v", svcerr.ErrAccountMalformed, err)
			}
			copy(peerID[:], peerIDBytes)
			hasPeer = true
		}

		// Default an unset limit; the caller (handler ClampLimit) owns the upper bound.
		if limit == 0 {
			limit = 200
		}

		var lines []TrustLine
		visit := func(_ [32]byte, data []byte) {
			if state.EntryType(data) != "RippleState" {
				return
			}
			rs, perr := state.ParseRippleState(data)
			if perr != nil {
				return
			}

			// Owner-directory membership guarantees one side matches; default is defensive.
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
				return
			}

			if hasPeer {
				peerAccountID, _ := decodeAccountIDLocal(peerAccount)
				if peerAccountID != peerID {
					return
				}
			}

			line := TrustLine{
				Account:  peerAccount,
				Currency: rs.Balance.Currency,
			}

			// Balance from our account's perspective: positive = peer owes us.
			if isLowAccount {
				line.Balance = rs.Balance.Negate().Value()
				line.Limit = rs.LowLimit.Value()
				line.LimitPeer = rs.HighLimit.Value()
				line.NoRipple = (rs.Flags & state.LsfLowNoRipple) != 0
				line.NoRipplePeer = (rs.Flags & state.LsfHighNoRipple) != 0
				line.Authorized = (rs.Flags & state.LsfLowAuth) != 0
				line.PeerAuthorized = (rs.Flags & state.LsfHighAuth) != 0
				line.Freeze = (rs.Flags & state.LsfLowFreeze) != 0
				line.FreezePeer = (rs.Flags & state.LsfHighFreeze) != 0
				line.QualityIn = rs.LowQualityIn
				line.QualityOut = rs.LowQualityOut
			} else {
				line.Balance = rs.Balance.Value()
				line.Limit = rs.HighLimit.Value()
				line.LimitPeer = rs.LowLimit.Value()
				line.NoRipple = (rs.Flags & state.LsfHighNoRipple) != 0
				line.NoRipplePeer = (rs.Flags & state.LsfLowNoRipple) != 0
				line.Authorized = (rs.Flags & state.LsfHighAuth) != 0
				line.PeerAuthorized = (rs.Flags & state.LsfLowAuth) != 0
				line.Freeze = (rs.Flags & state.LsfHighFreeze) != 0
				line.FreezePeer = (rs.Flags & state.LsfLowFreeze) != 0
				line.QualityIn = rs.HighQualityIn
				line.QualityOut = rs.HighQualityOut
			}

			lines = append(lines, line)
		}

		markerStr, found, err := ownerDirAfter(ctx, targetLedger, accountID, limit, afterKey, hintPage, hasMarker, visit)
		if err != nil {
			return nil, err
		}
		if hasMarker && !found {
			return nil, svcerr.ErrInvalidMarker
		}

		return &AccountLinesResult{
			Account:     account,
			Lines:       lines,
			LedgerIndex: targetLedger.Sequence(),
			LedgerHash:  targetLedger.Hash(),
			Validated:   validated,
			Marker:      markerStr,
		}, nil
	})
}

// AccountOffer represents an offer from account_offers RPC
type AccountOffer struct {
	Flags      uint32 `json:"flags"`
	Seq        uint32 `json:"seq"`
	TakerGets  any    `json:"taker_gets"`
	TakerPays  any    `json:"taker_pays"`
	Quality    string `json:"quality"`
	Expiration uint32 `json:"expiration,omitempty"`
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
func (s *Service) GetAccountOffers(ctx context.Context, account string, ledgerIndex string, limit uint32, marker string) (*AccountOffersResult, error) {
	return withAccountQuery(s, ctx, account, ledgerIndex, func(targetLedger *ledger.Ledger, accountID [20]byte, validated bool) (*AccountOffersResult, error) {
		afterKey, hintPage, hasMarker, err := parseDirMarker(marker)
		if err != nil {
			return nil, err
		}

		// Default an unset limit; the caller (handler ClampLimit) owns the upper bound.
		if limit == 0 {
			limit = 200
		}

		var offers []AccountOffer
		visit := func(_ [32]byte, data []byte) {
			if state.EntryType(data) != "Offer" {
				return
			}
			offer, perr := state.ParseLedgerOffer(data)
			if perr != nil {
				return
			}

			accountOffer := AccountOffer{
				Flags: offer.Flags,
				Seq:   offer.Sequence,
			}

			if offer.TakerGets.IsNative() {
				accountOffer.TakerGets = offer.TakerGets.Value()
			} else {
				accountOffer.TakerGets = map[string]string{
					"currency": offer.TakerGets.Currency,
					"issuer":   offer.TakerGets.Issuer,
					"value":    offer.TakerGets.Value(),
				}
			}

			if offer.TakerPays.IsNative() {
				accountOffer.TakerPays = offer.TakerPays.Value()
			} else {
				accountOffer.TakerPays = map[string]string{
					"currency": offer.TakerPays.Currency,
					"issuer":   offer.TakerPays.Issuer,
					"value":    offer.TakerPays.Value(),
				}
			}

			accountOffer.Quality = qualityFromBookDir(offer.BookDirectory)

			if offer.Expiration > 0 {
				accountOffer.Expiration = offer.Expiration
			}

			offers = append(offers, accountOffer)
		}

		markerStr, found, err := ownerDirAfter(ctx, targetLedger, accountID, limit, afterKey, hintPage, hasMarker, visit)
		if err != nil {
			return nil, err
		}
		if hasMarker && !found {
			return nil, svcerr.ErrInvalidMarker
		}

		return &AccountOffersResult{
			Account:     account,
			Offers:      offers,
			LedgerIndex: targetLedger.Sequence(),
			LedgerHash:  targetLedger.Hash(),
			Validated:   validated,
			Marker:      markerStr,
		}, nil
	})
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

// GetAccountObjects enumerates an account's owned objects, paginated by an opaque
// marker: NFTokenPages first (not linked into the owner directory), then
// owner-directory entries. Marker is "<dirIndex>,<entryIndex>" ("0,<pageKey>" in
// the NFTokenPage region). limit counts every directory entry visited, not just
// type-filter matches, so a filtered page can come back short with a marker.
func (s *Service) GetAccountObjects(ctx context.Context, account string, ledgerIndex string, objType string, limit uint32, marker string) (*AccountObjectsResult, error) {
	return withAccountQuery(s, ctx, account, ledgerIndex, func(targetLedger *ledger.Ledger, accountID [20]byte, validated bool) (*AccountObjectsResult, error) {
		// account_objects returns actNotFound for a missing account, not empty.
		accountKey := keylet.Account(accountID)
		exists, err := targetLedger.Exists(accountKey)
		if err != nil {
			return nil, fmt.Errorf("failed to check account existence: %w", err)
		}
		if !exists {
			return nil, svcerr.ErrAccountNotFound
		}

		// Normalize type filter from rippled's snake_case to PascalCase
		objType = normalizeObjectType(objType)

		// Default an unset limit; the caller (handler ClampLimit) owns the upper bound.
		if limit == 0 {
			limit = 200
		}

		var dirIndex, entryIndex [32]byte
		if marker != "" {
			di, ei, ok := parseAccountObjectsMarker(marker)
			if !ok {
				return nil, svcerr.ErrInvalidMarker
			}
			dirIndex, entryIndex = di, ei
		}

		result := &AccountObjectsResult{
			Account:        account,
			AccountObjects: make([]AccountObjectItem, 0),
			LedgerIndex:    targetLedger.Sequence(),
			LedgerHash:     targetLedger.Hash(),
			Validated:      validated,
		}

		if err := enumerateAccountObjects(ctx, targetLedger, accountID, objType, dirIndex, entryIndex, limit, result); err != nil {
			return nil, err
		}
		return result, nil
	})
}

// parseAccountObjectsMarker splits an account_objects marker into its
// "<dirIndex>,<entryIndex>" halves. Each half is either the literal "0" (zero)
// or exactly 64 hex chars, matching rippled uint256::parseHex.
func parseAccountObjectsMarker(marker string) (dirIndex, entryIndex [32]byte, ok bool) {
	di, ei, found := strings.Cut(marker, ",")
	if !found {
		return dirIndex, entryIndex, false
	}
	if dirIndex, ok = markerUint256(di); !ok {
		return dirIndex, entryIndex, false
	}
	if entryIndex, ok = markerUint256(ei); !ok {
		return dirIndex, entryIndex, false
	}
	return dirIndex, entryIndex, true
}

// markerUint256 parses one marker component: "0" yields the zero value, any
// other value must be exactly 64 hex characters.
func markerUint256(s string) ([32]byte, bool) {
	var out [32]byte
	if s == "0" {
		return out, true
	}
	if err := decodeHex32Into(s, &out); err != nil {
		return out, false
	}
	return out, true
}

// enumerateAccountObjects walks an account's NFTokenPages then owner directory
// into result, resuming from (dirIndex, entryIndex) and visiting at most limit
// entries (charged per directory entry, not per type-match); sets result.Marker
// when more remain. A missing dirIndex page or absent entryIndex is an invalid
// marker.
func enumerateAccountObjects(ctx context.Context, l *ledger.Ledger, accountID [20]byte, objType string, dirIndex, entryIndex [32]byte, limit uint32, result *AccountObjectsResult) error {
	var zero [32]byte
	wantType := func(t string) bool { return objType == "" || t == objType }

	if dirIndex != zero {
		d, err := l.Read(keylet.Keylet{Key: dirIndex})
		if err != nil {
			return err
		}
		if d == nil {
			return svcerr.ErrInvalidMarker
		}
	}

	firstNFTPage := keylet.NFTokenPageMin(accountID).Key
	iterateNFT := (objType == "" || objType == "NFTokenPage") && dirIndex == zero
	if iterateNFT && entryIndex != zero {
		var maskedHigh [32]byte
		copy(maskedHigh[:20], entryIndex[:20])
		if maskedHigh != firstNFTPage {
			iterateNFT = false
		}
	}

	mlimit := limit

	if iterateNFT {
		first := firstNFTPage
		if entryIndex != zero {
			first = entryIndex
		}
		maxKey := keylet.NFTokenPageMax(accountID).Key
		pageKey, pageData, ok, err := l.Succ(first)
		if err != nil {
			return err
		}
		for ok && bytes.Compare(pageKey[:], maxKey[:]) <= 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			result.AccountObjects = append(result.AccountObjects, AccountObjectItem{
				Index:           formatHashHex(pageKey),
				LedgerEntryType: "NFTokenPage",
				Data:            pageData,
			})

			page, perr := state.ParseNFTokenPage(pageData)
			hasNext := perr == nil && page.NextPageMin != zero
			var nextKey [32]byte
			var nextData []byte
			nextOK := false
			if hasNext {
				nextKey = page.NextPageMin
				nextData, err = l.Read(keylet.Keylet{Key: nextKey})
				if err != nil {
					return err
				}
				nextOK = nextData != nil
			}

			mlimit--
			if mlimit == 0 && nextOK {
				result.Marker = "0," + formatHashHex(pageKey)
				return nil
			}

			if !hasNext || !nextOK {
				break
			}
			pageKey, pageData = nextKey, nextData
		}
		entryIndex = zero
	}

	root := keylet.OwnerDir(accountID).Key
	found := false
	if dirIndex == zero {
		dirIndex = root
		found = true
	}

	dirData, err := l.Read(keylet.Keylet{Key: dirIndex})
	if err != nil {
		return err
	}
	if dirData == nil {
		return nil
	}
	dir, err := state.ParseDirectoryNode(dirData)
	if err != nil {
		return err
	}

	i := uint32(0)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries := dir.Indexes
		start := 0
		if !found {
			start = indexOfHash(entries, entryIndex)
			if start < 0 {
				return svcerr.ErrInvalidMarker
			}
			found = true
		}

		// NFTokenPages exactly filled the limit; resume at the first dir entry.
		if i == mlimit && mlimit < limit {
			result.Marker = formatHashHex(dirIndex) + "," + formatHashHex(entries[start])
			return nil
		}

		for idx := start; idx < len(entries); idx++ {
			itemKey := entries[idx]
			data, rerr := l.Read(keylet.Keylet{Key: itemKey})
			if rerr != nil {
				return rerr
			}
			if data != nil {
				if t := state.EntryType(data); wantType(t) {
					result.AccountObjects = append(result.AccountObjects, AccountObjectItem{
						Index:           formatHashHex(itemKey),
						LedgerEntryType: t,
						Data:            data,
					})
				}
			}
			i++
			if i == mlimit {
				if idx+1 < len(entries) {
					result.Marker = formatHashHex(dirIndex) + "," + formatHashHex(entries[idx+1])
					return nil
				}
				break
			}
		}

		nodeIndex := dir.IndexNext
		if nodeIndex == 0 {
			return nil
		}
		dirIndex = keylet.DirPage(root, nodeIndex).Key
		dirData, err = l.Read(keylet.Keylet{Key: dirIndex})
		if err != nil {
			return err
		}
		if dirData == nil {
			return nil
		}
		dir, err = state.ParseDirectoryNode(dirData)
		if err != nil {
			return err
		}
		if i == mlimit {
			if len(dir.Indexes) > 0 {
				result.Marker = formatHashHex(dirIndex) + "," + formatHashHex(dir.Indexes[0])
			}
			return nil
		}
	}
}

// indexOfHash returns the position of key in entries, or -1 if absent.
func indexOfHash(entries [][32]byte, key [32]byte) int {
	for i := range entries {
		if entries[i] == key {
			return i
		}
	}
	return -1
}

// ownerDirAfter walks accountID's owner directory, invoking visit per entry and
// resuming strictly after afterKey when hasMarker (jumping to hintPage first, else
// scanning from the root). limit is charged per entry visited, not per entry kept,
// so a filtered page can come back short but still carry a marker. Returns the
// "<entryKey>,<ownerNode>" resume marker when an entry beyond the limit exists,
// and found=false when a non-empty marker could not be located.
func ownerDirAfter(
	ctx context.Context,
	l *ledger.Ledger,
	accountID [20]byte,
	limit uint32,
	afterKey [32]byte,
	hintPage uint64,
	hasMarker bool,
	visit func(key [32]byte, data []byte),
) (marker string, found bool, err error) {
	root := keylet.OwnerDir(accountID).Key

	readPage := func(page uint64) (*state.DirectoryNode, error) {
		key := root
		if page != 0 {
			key = keylet.DirPage(root, page).Key
		}
		data, rerr := l.Read(keylet.Keylet{Key: key})
		if rerr != nil || data == nil {
			return nil, rerr
		}
		return state.ParseDirectoryNode(data)
	}

	// Resolve the starting page. When resuming, prefer the hint page if it still
	// holds afterKey; otherwise scan from the root and skip to afterKey.
	page := uint64(0)
	found = !hasMarker
	if hasMarker && hintPage != 0 {
		hp, herr := readPage(hintPage)
		if herr != nil {
			return "", false, herr
		}
		if hp != nil && indexOfHash(hp.Indexes, afterKey) >= 0 {
			page = hintPage
		}
	}

	var count uint32
	var markerKey [32]byte
	var markerPage uint64
	hasMore := false

	for {
		if cerr := ctx.Err(); cerr != nil {
			return "", found, cerr
		}
		node, perr := readPage(page)
		if perr != nil {
			return "", found, perr
		}
		if node == nil {
			break
		}
		for _, key := range node.Indexes {
			if !found {
				if key == afterKey {
					found = true
				}
				continue
			}
			data, rerr := l.Read(keylet.Keylet{Key: key})
			if rerr != nil {
				return "", found, rerr
			}
			if data == nil {
				// Null-SLE entry: skip without charging the limit.
				continue
			}
			if count >= limit {
				// A further entry exists beyond the page limit.
				hasMore = true
				break
			}
			count++
			visit(key, data)
			if count == limit {
				markerKey = key
				markerPage = page
			}
		}
		if hasMore {
			break
		}
		next := node.IndexNext
		if next == 0 {
			break
		}
		page = next
	}

	if hasMore {
		marker = formatDirMarker(markerKey, markerPage)
	}
	return marker, found, nil
}

// OwnerInfoResult groups an account's owner-directory offers and trust lines.
type OwnerInfoResult struct {
	Offers      []AccountObjectItem
	RippleLines []AccountObjectItem
	LedgerIndex uint32
	LedgerHash  [32]byte
	Validated   bool
}

// GetOwnerInfo walks the account's owner directory and groups offers and trust
// lines. Unlike GetAccountObjects it follows every page with no object cap; a
// missing owner directory yields empty slices.
func (s *Service) GetOwnerInfo(ctx context.Context, account string, ledgerIndex string) (*OwnerInfoResult, error) {
	return withAccountQuery(s, ctx, account, ledgerIndex, func(targetLedger *ledger.Ledger, accountID [20]byte, validated bool) (*OwnerInfoResult, error) {
		result := &OwnerInfoResult{
			Offers:      make([]AccountObjectItem, 0),
			RippleLines: make([]AccountObjectItem, 0),
			LedgerIndex: targetLedger.Sequence(),
			LedgerHash:  targetLedger.Hash(),
			Validated:   validated,
		}

		dirKey := keylet.OwnerDir(accountID)
		walkErr := state.DirForEach(targetLedger, dirKey, func(itemKey [32]byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			data, err := targetLedger.Read(keylet.Keylet{Key: itemKey})
			if err != nil || data == nil {
				return nil
			}
			entryType := state.EntryType(data)
			switch entryType {
			case "Offer":
				result.Offers = append(result.Offers, AccountObjectItem{
					Index:           formatHashHex(itemKey),
					LedgerEntryType: entryType,
					Data:            data,
				})
			case "RippleState":
				result.RippleLines = append(result.RippleLines, AccountObjectItem{
					Index:           formatHashHex(itemKey),
					LedgerEntryType: entryType,
					Data:            data,
				})
			}
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}

		return result, nil
	})
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
func (s *Service) GetAccountChannels(ctx context.Context, account string, destinationAccount string, ledgerIndex string, limit uint32, marker string) (*AccountChannelsResult, error) {
	return withAccountQuery(s, ctx, account, ledgerIndex, func(targetLedger *ledger.Ledger, accountID [20]byte, validated bool) (*AccountChannelsResult, error) {
		afterKey, hintPage, hasMarker, err := parseDirMarker(marker)
		if err != nil {
			return nil, err
		}

		accountKey := keylet.Account(accountID)
		exists, err := targetLedger.Exists(accountKey)
		if err != nil {
			return nil, fmt.Errorf("failed to check account existence: %w", err)
		}
		if !exists {
			return nil, svcerr.ErrAccountNotFound
		}

		var destID [20]byte
		hasDestFilter := false
		if destinationAccount != "" {
			_, destIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(destinationAccount)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid destination_account address: %v", svcerr.ErrAccountMalformed, err)
			}
			copy(destID[:], destIDBytes)
			hasDestFilter = true
		}

		// Default an unset limit; the caller (handler ClampLimit) owns the upper bound.
		if limit == 0 {
			limit = 256
		}

		var channels []AccountChannel
		visit := func(key [32]byte, data []byte) {
			if state.EntryType(data) != "PayChannel" {
				return
			}
			payChan, perr := state.ParsePayChannel(data)
			if perr != nil {
				return
			}

			// account_channels reports only channels the account is source of.
			if payChan.Account != accountID {
				return
			}

			if hasDestFilter && payChan.DestinationID != destID {
				return
			}

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

			if payChan.PublicKey != "" {
				channel.PublicKeyHex = payChan.PublicKey
				pkBytes, derr := hex.DecodeString(payChan.PublicKey)
				if derr == nil && len(pkBytes) > 0 {
					if encoded, encErr := addresscodec.EncodeNodePublicKey(pkBytes); encErr == nil {
						channel.PublicKey = encoded
					}
				}
			}

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
		}

		markerStr, found, err := ownerDirAfter(ctx, targetLedger, accountID, limit, afterKey, hintPage, hasMarker, visit)
		if err != nil {
			return nil, err
		}
		if hasMarker && !found {
			return nil, svcerr.ErrInvalidMarker
		}

		return &AccountChannelsResult{
			Account:     account,
			Channels:    channels,
			LedgerIndex: targetLedger.Sequence(),
			LedgerHash:  targetLedger.Hash(),
			Validated:   validated,
			Marker:      markerStr,
		}, nil
	})
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
func (s *Service) GetAccountCurrencies(ctx context.Context, account string, ledgerIndex string) (*AccountCurrenciesResult, error) {
	return withAccountQuery(s, ctx, account, ledgerIndex, func(targetLedger *ledger.Ledger, accountID [20]byte, validated bool) (*AccountCurrenciesResult, error) {
		accountKey := keylet.Account(accountID)
		exists, err := targetLedger.Exists(accountKey)
		if err != nil {
			return nil, fmt.Errorf("failed to check account existence: %w", err)
		}
		if !exists {
			return nil, svcerr.ErrAccountNotFound
		}

		receiveCurrencies := make(map[string]bool)
		sendCurrencies := make(map[string]bool)

		dirKey := keylet.OwnerDir(accountID)
		walkErr := state.DirForEach(targetLedger, dirKey, func(itemKey [32]byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			data, err := targetLedger.Read(keylet.Keylet{Key: itemKey})
			if err != nil || data == nil {
				return nil
			}
			if state.EntryType(data) != "RippleState" {
				return nil
			}

			rs, err := state.ParseRippleState(data)
			if err != nil {
				return nil
			}

			// Owner-directory membership implies ownership; low/high selects our side.
			lowID, _ := decodeAccountIDLocal(rs.LowLimit.Issuer)
			highID, _ := decodeAccountIDLocal(rs.HighLimit.Issuer)

			// balance is from our perspective; receivable while balance < our limit,
			// sendable while -balance < peer limit.
			var balance, ownLimit, peerLimit tx.Amount
			if lowID == accountID {
				balance, ownLimit, peerLimit = rs.Balance, rs.LowLimit, rs.HighLimit
			} else if highID == accountID {
				balance, ownLimit, peerLimit = rs.Balance.Negate(), rs.HighLimit, rs.LowLimit
			} else {
				return nil // not our account
			}

			currency := rs.Balance.Currency
			if balance.Compare(ownLimit) < 0 {
				receiveCurrencies[currency] = true
			}
			if balance.Negate().Compare(peerLimit) < 0 {
				sendCurrencies[currency] = true
			}

			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}

		// RippleState can't carry XRP; drop the reserved sentinel like rippled.
		delete(receiveCurrencies, "XRP")
		delete(sendCurrencies, "XRP")

		receiveList := make([]string, 0, len(receiveCurrencies))
		for currency := range receiveCurrencies {
			receiveList = append(receiveList, currency)
		}
		sort.Strings(receiveList)

		sendList := make([]string, 0, len(sendCurrencies))
		for currency := range sendCurrencies {
			sendList = append(sendList, currency)
		}
		sort.Strings(sendList)

		return &AccountCurrenciesResult{
			ReceiveCurrencies: receiveList,
			SendCurrencies:    sendList,
			LedgerIndex:       targetLedger.Sequence(),
			LedgerHash:        targetLedger.Hash(),
			Validated:         validated,
		}, nil
	})
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
func (s *Service) GetAccountNFTs(ctx context.Context, account string, ledgerIndex string, limit uint32) (*AccountNFTsResult, error) {
	return withAccountQuery(s, ctx, account, ledgerIndex, func(targetLedger *ledger.Ledger, accountID [20]byte, validated bool) (*AccountNFTsResult, error) {
		accountKey := keylet.Account(accountID)
		exists, err := targetLedger.Exists(accountKey)
		if err != nil {
			return nil, fmt.Errorf("failed to check account existence: %w", err)
		}
		if !exists {
			return nil, svcerr.ErrAccountNotFound
		}

		// Default an unset limit; the caller (handler ClampLimit) owns the upper bound.
		if limit == 0 {
			limit = 256
		}

		var nfts []NFTInfo

		targetLedger.ForEachCtx(ctx, func(key [32]byte, data []byte) bool {
			if ctx.Err() != nil {
				return false
			}
			if uint32(len(nfts)) >= limit {
				return false
			}

			if len(data) < 3 {
				return true
			}

			if data[0] != 0x11 { // UInt16 type code 1, field code 1
				return true
			}
			entryType := uint16(data[1])<<8 | uint16(data[2])
			if entryType != 0x0050 { // NFTokenPage type
				return true
			}

			// NFTokenPage key has the owner account ID in bytes 0-19.
			var pageOwner [20]byte
			copy(pageOwner[:], key[0:20])
			if pageOwner != accountID {
				return true
			}

			page, err := state.ParseNFTokenPage(data)
			if err != nil {
				return true
			}

			for _, token := range page.NFTokens {
				if uint32(len(nfts)) >= limit {
					break
				}

				nft := extractNFTInfo(token.NFTokenID, token.URI)
				nfts = append(nfts, nft)
			}

			return true
		})
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		return &AccountNFTsResult{
			Account:     account,
			AccountNFTs: nfts,
			LedgerIndex: targetLedger.Sequence(),
			LedgerHash:  targetLedger.Hash(),
			Validated:   validated,
		}, nil
	})
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

// addEscrowLocked sums an Escrow into locked by currency ("XRP" or the IOU code).
// MPT escrows are skipped — no currency to sum under (rippled instead errors).
func addEscrowLocked(locked map[string]tx.Amount, data []byte) {
	esc, err := state.ParseEscrow(data)
	if err != nil {
		return
	}

	var amount tx.Amount
	var currency string
	switch {
	case esc.IsXRP:
		if esc.Amount > state.MaxNativeDrops {
			return
		}
		amount = state.NewXRPAmountFromInt(int64(esc.Amount))
		currency = "XRP"
	case esc.IOUAmount != nil && !esc.IOUAmount.IsMPT():
		amount = *esc.IOUAmount
		currency = amount.Currency
	default:
		return
	}

	existing, ok := locked[currency]
	if !ok {
		locked[currency] = amount
		return
	}
	locked[currency] = addLockedSaturating(existing, amount)
}

// addLockedSaturating returns existing+add, clamping to the largest IOU amount on
// overflow rather than panicking, matching rippled's saturating escrow sum.
func addLockedSaturating(existing, add tx.Amount) (result tx.Amount) {
	defer func() {
		if recover() != nil {
			result = state.NewIssuedAmountFromValue(state.MaxMantissa, state.MaxExponent, add.Currency, add.Issuer)
		}
	}()
	sum, err := existing.Add(add)
	if err != nil {
		return existing
	}
	return sum
}

// GetGatewayBalances retrieves obligations and balances for a gateway account
func (s *Service) GetGatewayBalances(ctx context.Context, account string, hotWallets []string, ledgerIndex string) (*GatewayBalancesResult, error) {
	return withAccountQuery(s, ctx, account, ledgerIndex, func(targetLedger *ledger.Ledger, accountID [20]byte, validated bool) (*GatewayBalancesResult, error) {
		accountKey := keylet.Account(accountID)
		exists, err := targetLedger.Exists(accountKey)
		if err != nil {
			return nil, fmt.Errorf("failed to check account existence: %w", err)
		}
		if !exists {
			return nil, svcerr.ErrAccountNotFound
		}

		hotWalletIDs := make(map[[20]byte]bool)
		for _, hw := range hotWallets {
			_, hwIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(hw)
			if err != nil {
				return nil, fmt.Errorf("%w: %s", svcerr.ErrInvalidHotWallet, hw)
			}
			var hwID [20]byte
			copy(hwID[:], hwIDBytes)
			hotWalletIDs[hwID] = true
		}

		obligations := make(map[string]tx.Amount)         // currency -> total obligations
		hotBalances := make(map[string][]CurrencyBalance) // account -> balances
		frozenBalances := make(map[string][]CurrencyBalance)
		assets := make(map[string][]CurrencyBalance)
		locked := make(map[string]tx.Amount) // currency -> total escrowed

		// Walk the gateway's owner directory: trust lines and escrows only.
		dirKey := keylet.OwnerDir(accountID)
		walkErr := state.DirForEach(targetLedger, dirKey, func(itemKey [32]byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			data, err := targetLedger.Read(keylet.Keylet{Key: itemKey})
			if err != nil || data == nil {
				return nil
			}

			entryType := state.EntryType(data)
			if entryType == "Escrow" {
				addEscrowLocked(locked, data)
				return nil
			}
			if entryType != "RippleState" {
				return nil
			}

			rs, err := state.ParseRippleState(data)
			if err != nil {
				return nil
			}

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
				return nil // Not our account
			}

			if rs.Balance.IsZero() {
				return nil
			}

			// Balance from the gateway's perspective: keep as-is if low, negate if high.
			// Negative = we owe (obligations), positive = they owe us (assets).
			var gatewayBalance tx.Amount
			if isLowAccount {
				gatewayBalance = rs.Balance
			} else {
				gatewayBalance = rs.Balance.Negate()
			}

			peerAddr, _ := addresscodec.EncodeAccountIDToClassicAddress(peerID[:])

			currency := rs.Balance.Currency

			// Gateway froze the line if it set the freeze flag on its own side.
			var isFrozen bool
			if isLowAccount {
				isFrozen = (rs.Flags & state.LsfLowFreeze) != 0
			} else {
				isFrozen = (rs.Flags & state.LsfHighFreeze) != 0
			}

			if hotWalletIDs[peerID] {
				// Hot wallet: report the balance they hold (negated from gateway side).
				if gatewayBalance.Signum() < 0 {
					balanceText := gatewayBalance.Negate().Value()
					hotBalances[peerAddr] = append(hotBalances[peerAddr], CurrencyBalance{
						Currency: currency,
						Value:    balanceText,
					})
				}
			} else if gatewayBalance.Signum() > 0 {
				// Gateway holds currency from peer (unusual) — asset.
				balanceText := gatewayBalance.Value()
				assets[peerAddr] = append(assets[peerAddr], CurrencyBalance{
					Currency: currency,
					Value:    balanceText,
				})
			} else if isFrozen {
				balanceText := gatewayBalance.Negate().Value()
				frozenBalances[peerAddr] = append(frozenBalances[peerAddr], CurrencyBalance{
					Currency: currency,
					Value:    balanceText,
				})
			} else {
				// Normal obligation: negate the negative balance to the amount owed.
				owedAmount := gatewayBalance.Negate()
				if existing, ok := obligations[currency]; ok {
					sum, _ := existing.Add(owedAmount)
					obligations[currency] = sum
				} else {
					obligations[currency] = owedAmount
				}
			}

			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}

		obligationsStr := make(map[string]string)
		for curr, amt := range obligations {
			obligationsStr[curr] = amt.Value()
		}

		lockedStr := make(map[string]string, len(locked))
		for curr, amt := range locked {
			lockedStr[curr] = amt.Value()
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
		if len(lockedStr) > 0 {
			result.Locked = lockedStr
		}

		return result, nil
	})
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
	LimitAmount     map[string]any
}

// TrustSet transaction flags for NoRipple
const (
	tfSetNoRipple   uint32 = 0x00020000
	tfClearNoRipple uint32 = 0x00040000
)

// errNoRippleLimitReached stops the owner-directory walk in GetNoRippleCheck once
// limit problems have been collected.
var errNoRippleLimitReached = errors.New("noripple_check limit reached")

// GetNoRippleCheck checks trust lines for proper NoRipple flag settings
func (s *Service) GetNoRippleCheck(ctx context.Context, account string, role string, ledgerIndex string, limit uint32, transactions bool) (*NoRippleCheckResult, error) {
	return withAccountQuery(s, ctx, account, ledgerIndex, func(targetLedger *ledger.Ledger, accountID [20]byte, validated bool) (*NoRippleCheckResult, error) {
		accountKey := keylet.Account(accountID)
		exists, err := targetLedger.Exists(accountKey)
		if err != nil {
			return nil, fmt.Errorf("failed to check account existence: %w", err)
		}
		if !exists {
			return nil, svcerr.ErrAccountNotFound
		}

		data, err := targetLedger.Read(accountKey)
		if err != nil {
			return nil, fmt.Errorf("failed to read account: %w", err)
		}

		accountRoot, err := state.ParseAccountRoot(data)
		if err != nil {
			return nil, fmt.Errorf("failed to parse account data: %w", err)
		}

		roleGateway := role == "gateway"
		if !roleGateway && role != "user" {
			return nil, errors.New("invalid role: must be 'gateway' or 'user'")
		}

		// Default an unset limit; the caller (handler ClampLimit) owns the upper bound.
		if limit == 0 {
			limit = 300
		}

		bDefaultRipple := (accountRoot.Flags & state.LsfDefaultRipple) != 0

		var problems []string
		var suggestedTxs []SuggestedTransaction
		seq := accountRoot.Sequence

		baseFee, _, _ := s.GetCurrentFees()
		feeStr := strconv.FormatUint(baseFee, 10)

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

		// Walk owner directory checking NoRipple, capping reported problems at limit.
		problemCount := uint32(0)
		dirKey := keylet.OwnerDir(accountID)
		walkErr := state.DirForEach(targetLedger, dirKey, func(itemKey [32]byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if problemCount >= limit {
				return errNoRippleLimitReached
			}

			entryData, err := targetLedger.Read(keylet.Keylet{Key: itemKey})
			if err != nil || entryData == nil {
				return nil
			}
			if state.EntryType(entryData) != "RippleState" {
				return nil
			}

			rs, err := state.ParseRippleState(entryData)
			if err != nil {
				return nil
			}

			// Owner-directory membership implies ownership; low/high selects our side.
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
				return nil // Not our account
			}

			var bNoRipple bool
			if isLowAccount {
				bNoRipple = (rs.Flags & state.LsfLowNoRipple) != 0
			} else {
				bNoRipple = (rs.Flags & state.LsfHighNoRipple) != 0
			}

			currency := rs.Balance.Currency

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
					var limitValue string
					if isLowAccount {
						limitValue = rs.LowLimit.Value()
					} else {
						limitValue = rs.HighLimit.Value()
					}

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
						LimitAmount: map[string]any{
							"currency": currency,
							"issuer":   peerAccount,
							"value":    limitValue,
						},
					})
					seq++
				}
			}

			return nil
		})
		if walkErr != nil && !errors.Is(walkErr, errNoRippleLimitReached) {
			return nil, walkErr
		}

		return &NoRippleCheckResult{
			Problems:     problems,
			Transactions: suggestedTxs,
			LedgerIndex:  targetLedger.Sequence(),
			LedgerHash:   targetLedger.Hash(),
			Validated:    validated,
		}, nil
	})
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

// GetDepositAuthorized reports whether sourceAccount may deposit to
// destinationAccount. Supplied credentials are validated on-ledger (existence,
// acceptance, expiry, ownership, duplicates) before the credential-based
// preauth check.
func (s *Service) GetDepositAuthorized(ctx context.Context, sourceAccount string, destinationAccount string, ledgerIndex string, credentials []string) (*DepositAuthorizedResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	targetLedger, validated, err := s.getLedgerForQuery(ledgerIndex)
	if err != nil {
		return nil, err
	}

	_, srcIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(sourceAccount)
	if err != nil {
		return nil, fmt.Errorf("invalid source_account address: %w", err)
	}
	var srcID [20]byte
	copy(srcID[:], srcIDBytes)

	_, dstIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(destinationAccount)
	if err != nil {
		return nil, fmt.Errorf("invalid destination_account address: %w", err)
	}
	var dstID [20]byte
	copy(dstID[:], dstIDBytes)

	srcKey := keylet.Account(srcID)
	exists, err := targetLedger.Exists(srcKey)
	if err != nil {
		return nil, fmt.Errorf("failed to check source account existence: %w", err)
	}
	if !exists {
		return nil, svcerr.ErrSrcAccountNotFound
	}

	dstKey := keylet.Account(dstID)
	exists, err = targetLedger.Exists(dstKey)
	if err != nil {
		return nil, fmt.Errorf("failed to check destination account existence: %w", err)
	}
	if !exists {
		return nil, svcerr.ErrDstAccountNotFound
	}

	dstData, err := targetLedger.Read(dstKey)
	if err != nil {
		return nil, fmt.Errorf("failed to read destination account: %w", err)
	}

	dstAccountRoot, err := state.ParseAccountRoot(dstData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse destination account data: %w", err)
	}

	depositAuthRequired := (dstAccountRoot.Flags & state.LsfDepositAuth) != 0

	// Self-deposit is always authorized.
	sameAccount := srcID == dstID

	// Validate credentials on-ledger if provided.
	var sortedCredPairs []keylet.CredentialPair
	credentialsPresent := len(credentials) > 0
	if credentialsPresent {
		sortedCredPairs, err = validateCredentialsOnLedger(targetLedger, credentials, srcID)
		if err != nil {
			return nil, err
		}
	}

	depositAuthorized := true
	if depositAuthRequired && !sameAccount {
		// Direct account-to-account preauth.
		depositPreauthKey := keylet.DepositPreauth(dstID, srcID)
		exists, err := targetLedger.Exists(depositPreauthKey)
		if err != nil {
			return nil, fmt.Errorf("failed to check deposit preauthorization: %w", err)
		}
		depositAuthorized = exists

		// Fall back to credential-based preauth.
		if !depositAuthorized && credentialsPresent {
			credPreauthKey := keylet.DepositPreauthCredentials(dstID, sortedCredPairs)
			exists, err := targetLedger.Exists(credPreauthKey)
			if err != nil {
				return nil, fmt.Errorf("failed to check credential deposit preauthorization: %w", err)
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

// validateCredentialsOnLedger checks each credential ID against the ledger
// (existence, acceptance, expiry, ownership subject==srcAcct, duplicate
// issuer+type) and returns the sorted pairs for the preauth lookup. Errors wrap
// svcerr.ErrBadCredentials so the handler maps them via errors.Is.
func validateCredentialsOnLedger(targetLedger *ledger.Ledger, credentials []string, srcAcct [20]byte) ([]keylet.CredentialPair, error) {
	type credKey struct {
		issuer         [20]byte
		credentialType string
	}

	seen := make(map[credKey]struct{})
	pairs := make([]keylet.CredentialPair, 0, len(credentials))

	// Parent close time in Ripple-epoch seconds for the expiry check.
	var parentCloseTimeSecs uint32
	if t := targetLedger.ParentCloseTime(); !t.IsZero() {
		switch secs := toRippleTime(t); {
		case secs > math.MaxUint32:
			parentCloseTimeSecs = math.MaxUint32
		case secs > 0:
			parentCloseTimeSecs = uint32(secs)
		}
	}

	for _, credHex := range credentials {
		credHashBytes, err := hex.DecodeString(credHex)
		if err != nil {
			return nil, fmt.Errorf("%w: credentials don't exist", svcerr.ErrBadCredentials)
		}
		var credHash [32]byte
		copy(credHash[:], credHashBytes)

		credKeylet := keylet.CredentialByID(credHash)
		credData, err := targetLedger.Read(credKeylet)
		if err != nil || credData == nil {
			return nil, fmt.Errorf("%w: credentials don't exist", svcerr.ErrBadCredentials)
		}

		credEntry, err := credential.ParseCredentialEntry(credData)
		if err != nil {
			return nil, fmt.Errorf("%w: credentials don't exist", svcerr.ErrBadCredentials)
		}

		// Check accepted flag.
		if !credEntry.IsAccepted() {
			return nil, fmt.Errorf("%w: credentials aren't accepted", svcerr.ErrBadCredentials)
		}

		// Check expiry.
		if credential.CheckCredentialExpired(credEntry, parentCloseTimeSecs) {
			return nil, fmt.Errorf("%w: credentials are expired", svcerr.ErrBadCredentials)
		}

		// Check ownership: subject must match source account.
		if credEntry.Subject != srcAcct {
			return nil, fmt.Errorf("%w: credentials doesn't belong to the root account", svcerr.ErrBadCredentials)
		}

		// Check for duplicates (same issuer + credentialType).
		key := credKey{
			issuer:         credEntry.Issuer,
			credentialType: string(credEntry.CredentialType),
		}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("%w: duplicates in credentials", svcerr.ErrBadCredentials)
		}
		seen[key] = struct{}{}

		pairs = append(pairs, keylet.CredentialPair{
			Issuer:         credEntry.Issuer,
			CredentialType: credEntry.CredentialType,
		})
	}

	// Sort by (issuer, credentialType) for a deterministic keylet.
	sort.Slice(pairs, func(i, j int) bool {
		cmp := bytes.Compare(pairs[i].Issuer[:], pairs[j].Issuer[:])
		if cmp != 0 {
			return cmp < 0
		}
		return bytes.Compare(pairs[i].CredentialType, pairs[j].CredentialType) < 0
	})

	return pairs, nil
}
