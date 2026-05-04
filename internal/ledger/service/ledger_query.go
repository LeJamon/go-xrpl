package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/protocol"
	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

// LedgerRangeResult contains ledger hashes for a range
type LedgerRangeResult struct {
	LedgerFirst uint32              `json:"ledger_first"`
	LedgerLast  uint32              `json:"ledger_last"`
	Hashes      map[uint32][32]byte `json:"hashes"`
}

// GetLedgerRange retrieves ledger hashes for a range of sequences.
// The supplied ctx is forwarded to the relational DB lookup.
func (s *Service) GetLedgerRange(ctx context.Context, minSeq, maxSeq uint32) (*LedgerRangeResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := &LedgerRangeResult{
		LedgerFirst: minSeq,
		LedgerLast:  maxSeq,
		Hashes:      make(map[uint32][32]byte),
	}

	// Try in-memory first
	for seq := minSeq; seq <= maxSeq; seq++ {
		if l, ok := s.ledgerHistory[seq]; ok {
			result.Hashes[seq] = l.Hash()
		}
	}

	// If we have RelationalDB, fill in gaps
	if s.relationalDB != nil && len(result.Hashes) < int(maxSeq-minSeq+1) {
		hashPairs, err := s.relationalDB.Ledger().GetHashesByRange(ctx,
			relationaldb.LedgerIndex(minSeq),
			relationaldb.LedgerIndex(maxSeq))
		if err == nil {
			for seq, pair := range hashPairs {
				if _, exists := result.Hashes[uint32(seq)]; !exists {
					result.Hashes[uint32(seq)] = [32]byte(pair.LedgerHash)
				}
			}
		}
	}

	return result, nil
}

// LedgerEntryResult contains a single ledger entry
type LedgerEntryResult struct {
	Index       string   `json:"index"`
	LedgerIndex uint32   `json:"ledger_index"`
	LedgerHash  [32]byte `json:"ledger_hash"`
	Node        []byte   `json:"node"`
	NodeBinary  string   `json:"node_binary,omitempty"`
	Validated   bool     `json:"validated"`
}

// GetLedgerEntry retrieves a specific ledger entry by its index/key
func (s *Service) GetLedgerEntry(entryKey [32]byte, ledgerIndex string) (*LedgerEntryResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	targetLedger, validated, err := s.getLedgerForQuery(ledgerIndex)
	if err != nil {
		return nil, err
	}

	k := keylet.Keylet{Key: entryKey}
	exists, err := targetLedger.Exists(k)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.New("entry not found")
	}

	data, err := targetLedger.Read(k)
	if err != nil {
		return nil, err
	}

	return &LedgerEntryResult{
		Index:       formatHashHex(entryKey),
		LedgerIndex: targetLedger.Sequence(),
		LedgerHash:  targetLedger.Hash(),
		Node:        data,
		Validated:   validated,
	}, nil
}

// LedgerDataResult contains ledger state data
type LedgerDataResult struct {
	LedgerIndex uint32           `json:"ledger_index"`
	LedgerHash  [32]byte         `json:"ledger_hash"`
	State       []LedgerDataItem `json:"state"`
	Marker      string           `json:"marker,omitempty"`
	Validated   bool             `json:"validated"`
	// Ledger header information for first query (without marker)
	LedgerHeader *LedgerHeaderInfo `json:"ledger,omitempty"`
}

// LedgerHeaderInfo contains complete ledger header data for responses
type LedgerHeaderInfo struct {
	AccountHash         [32]byte `json:"account_hash"`
	CloseFlags          uint8    `json:"close_flags"`
	CloseTime           int64    `json:"close_time"`       // Seconds since Ripple epoch
	CloseTimeHuman      string   `json:"close_time_human"` // Human-readable format
	CloseTimeISO        string   `json:"close_time_iso"`   // ISO 8601 format
	CloseTimeResolution uint32   `json:"close_time_resolution"`
	Closed              bool     `json:"closed"`
	LedgerHash          [32]byte `json:"ledger_hash"`
	LedgerIndex         uint32   `json:"ledger_index"`
	ParentCloseTime     int64    `json:"parent_close_time"`
	ParentHash          [32]byte `json:"parent_hash"`
	TotalCoins          uint64   `json:"total_coins"` // Total XRP drops
	TransactionHash     [32]byte `json:"transaction_hash"`
}

// LedgerDataItem represents a single state entry
type LedgerDataItem struct {
	Index string `json:"index"`
	Data  []byte `json:"data"`
}

// toRippleTime converts a time.Time to seconds since Ripple epoch
func toRippleTime(t time.Time) int64 {
	return t.Unix() - protocol.RippleEpochUnix
}

// formatCloseTimeHuman formats close time in XRPL human-readable format
func formatCloseTimeHuman(t time.Time) string {
	return t.UTC().Format("2006-Jan-02 15:04:05.000000000 UTC")
}

// formatCloseTimeISO formats close time in ISO 8601 format
func formatCloseTimeISO(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// GetLedgerData retrieves all ledger state entries with optional pagination
func (s *Service) GetLedgerData(ledgerIndex string, limit uint32, marker string) (*LedgerDataResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	targetLedger, validated, err := s.getLedgerForQuery(ledgerIndex)
	if err != nil {
		return nil, err
	}

	if limit == 0 || limit > 2048 {
		limit = 256
	}

	result := &LedgerDataResult{
		LedgerIndex: targetLedger.Sequence(),
		LedgerHash:  targetLedger.Hash(),
		State:       make([]LedgerDataItem, 0, limit),
		Validated:   validated,
	}

	// Parse marker if provided
	var startKey [32]byte
	hasMarker := false
	if marker != "" {
		if len(marker) == 64 {
			decoded, err := hexDecode(marker)
			if err == nil && len(decoded) == 32 {
				copy(startKey[:], decoded)
				hasMarker = true
			}
		}
	}

	// Include ledger header info only on first query (no marker)
	if !hasMarker {
		hdr := targetLedger.Header()
		result.LedgerHeader = &LedgerHeaderInfo{
			AccountHash:         hdr.AccountHash,
			CloseFlags:          hdr.CloseFlags,
			CloseTime:           toRippleTime(hdr.CloseTime),
			CloseTimeHuman:      formatCloseTimeHuman(hdr.CloseTime),
			CloseTimeISO:        formatCloseTimeISO(hdr.CloseTime),
			CloseTimeResolution: hdr.CloseTimeResolution,
			Closed:              targetLedger.IsClosed() || targetLedger.IsValidated(),
			LedgerHash:          hdr.Hash,
			LedgerIndex:         hdr.LedgerIndex,
			ParentCloseTime:     toRippleTime(hdr.ParentCloseTime),
			ParentHash:          hdr.ParentHash,
			TotalCoins:          hdr.Drops,
			TransactionHash:     hdr.TxHash,
		}
	}

	count := uint32(0)
	var lastKey [32]byte
	passedMarker := !hasMarker

	err = targetLedger.ForEach(func(key [32]byte, data []byte) bool {
		// Skip until we pass the marker
		if !passedMarker {
			if key == startKey {
				passedMarker = true
			}
			return true
		}

		if count >= limit {
			result.Marker = formatHashHex(lastKey)
			return false
		}

		result.State = append(result.State, LedgerDataItem{
			Index: formatHashHex(key),
			Data:  data,
		})
		lastKey = key
		count++
		return true
	})

	if err != nil {
		return nil, err
	}

	return result, nil
}

// getLedgerForQuery is a helper function to get ledger for query
func (s *Service) getLedgerForQuery(ledgerIndex string) (*ledger.Ledger, bool, error) {
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
		seq, err := strconv.ParseUint(ledgerIndex, 10, 32)
		if err != nil {
			return nil, false, errors.New("invalid ledger_index")
		}
		var ok bool
		targetLedger, ok = s.ledgerHistory[uint32(seq)]
		if !ok {
			return nil, false, ErrLedgerNotFound
		}
		validated = targetLedger.IsValidated()
	}

	if targetLedger == nil {
		return nil, false, ErrNoOpenLedger
	}

	return targetLedger, validated, nil
}
