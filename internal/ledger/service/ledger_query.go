package service

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	ledgerselector "github.com/LeJamon/go-xrpl/internal/ledger/selector"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
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
	result := &LedgerRangeResult{
		LedgerFirst: minSeq,
		LedgerLast:  maxSeq,
		Hashes:      make(map[uint32][32]byte),
	}

	// Fill from in-memory history under the lock, then release it before the
	// DB gap-fill: result.Hashes is function-local, so the merge below needs no
	// lock, and a slow DB page must not block consensus close.
	s.mu.RLock()
	for seq := minSeq; seq <= maxSeq; seq++ {
		if l, ok := s.ledgerHistory[seq]; ok {
			result.Hashes[seq] = l.Hash()
		}
	}
	db := s.relationalDB
	s.mu.RUnlock()

	// If we have RelationalDB, fill in gaps
	if db != nil && len(result.Hashes) < int(maxSeq-minSeq+1) {
		hashPairs, err := db.Ledger().GetHashesByRange(ctx,
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
func (s *Service) GetLedgerEntry(ctx context.Context, entryKey [32]byte, ledgerIndex string) (*LedgerEntryResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
		return nil, svcerr.ErrLedgerEntryNotFound
	}

	data, err := targetLedger.Read(k)
	if err != nil {
		return nil, err
	}

	return &LedgerEntryResult{
		Index:       protocol.Hash256Hex(entryKey),
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

// parentCloseTimeRippleEpoch returns the parent ledger's close time in
// Ripple-epoch seconds. Returns 0 for a nil ledger or pre-epoch time
// so EngineConfig.ParentCloseTime stays uint32-safe.
func parentCloseTimeRippleEpoch(parent *ledger.Ledger) uint32 {
	if parent == nil {
		return 0
	}
	return protocol.ToRippleTime(parent.CloseTime())
}

// formatCloseTimeHuman formats close time in XRPL human-readable format
func formatCloseTimeHuman(t time.Time) string {
	return t.UTC().Format("2006-Jan-02 15:04:05.000000000 UTC")
}

// GetLedgerData retrieves all ledger state entries with optional pagination
func (s *Service) GetLedgerData(ctx context.Context, ledgerIndex string, limit uint32, marker string) (*LedgerDataResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	targetLedger, validated, err := s.getLedgerForQuery(ledgerIndex)
	if err != nil {
		return nil, err
	}

	result := &LedgerDataResult{
		LedgerIndex: targetLedger.Sequence(),
		LedgerHash:  targetLedger.Hash(),
		State:       make([]LedgerDataItem, 0),
		Validated:   validated,
	}

	// Parse marker if provided. A present-but-unparseable marker is rejected
	// (rippled returns "Invalid field 'marker', not valid."), not silently
	// treated as a fresh first-page query.
	var startKey [32]byte
	hasMarker := false
	if marker != "" {
		decoded, derr := hex.DecodeString(marker)
		if len(marker) != 64 || derr != nil {
			return nil, svcerr.ErrInvalidMarker
		}
		copy(startKey[:], decoded)
		hasMarker = true
	}

	// Include ledger header info only on first query (no marker)
	if !hasMarker {
		hdr := targetLedger.Header()
		result.LedgerHeader = &LedgerHeaderInfo{
			AccountHash:         hdr.AccountHash,
			CloseFlags:          hdr.CloseFlags,
			CloseTime:           protocol.RippleSeconds(hdr.CloseTime),
			CloseTimeHuman:      formatCloseTimeHuman(hdr.CloseTime),
			CloseTimeISO:        protocol.FormatCloseTimeISO(hdr.CloseTime),
			CloseTimeResolution: hdr.CloseTimeResolution,
			Closed:              targetLedger.IsClosed() || targetLedger.IsValidated(),
			LedgerHash:          hdr.Hash,
			LedgerIndex:         hdr.LedgerIndex,
			ParentCloseTime:     protocol.RippleSeconds(hdr.ParentCloseTime),
			ParentHash:          hdr.ParentHash,
			TotalCoins:          hdr.Drops,
			TransactionHash:     hdr.TxHash,
		}
	}

	// Resume strictly after the marker via the state map's upper bound: a
	// since-deleted marker continues from the next entry (no O(n) rescan, no
	// silent empty page). The zero startKey starts from the first entry.
	count := uint32(0)

	err = targetLedger.IterateStateFrom(ctx, startKey, func(key [32]byte, data []byte) bool {
		if count >= limit {
			// One entry past the page → more remain. Resume is strictly-greater
			// than the marker, so emit the first un-emitted key minus one; the
			// next page then begins exactly at that entry, matching rippled.
			result.Marker = protocol.Hash256Hex(ledger.DecrementKey(key))
			return false
		}
		result.State = append(result.State, LedgerDataItem{
			Index: protocol.Hash256Hex(key),
			Data:  data,
		})
		count++
		return true
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

func (s *Service) getLedgerForQuery(ledgerIndex string) (*ledger.Ledger, bool, error) {
	selection, err := ledgerselector.Parse(ledgerIndex)
	if err != nil {
		return nil, false, serviceLedgerSelectorError(selection, err)
	}

	current := func() (*ledger.Ledger, bool, error) {
		l := s.openLedger
		return l, l != nil, nil
	}
	result, err := ledgerselector.Resolve(selection, ledgerselector.Callbacks[*ledger.Ledger]{
		Absent:  current,
		Current: current,
		Closed: func() (*ledger.Ledger, bool, error) {
			l := s.closedLedger
			return l, l != nil, nil
		},
		Validated: func() (*ledger.Ledger, bool, error) {
			l := s.validatedLedger
			return l, l != nil, nil
		},
		BySequence: func(sequence uint32) (*ledger.Ledger, bool, error) {
			if l := s.ledgerHistory[sequence]; l != nil {
				return l, true, nil
			}
			if s.openLedger != nil && s.openLedger.Sequence() == sequence {
				return s.openLedger, true, nil
			}
			return nil, false, nil
		},
		ByHash: func(hash [32]byte) (*ledger.Ledger, bool, error) {
			sequence, ok := s.ledgerByHash[hash]
			if !ok {
				return nil, false, nil
			}
			l := s.ledgerHistory[sequence]
			return l, l != nil, nil
		},
	})
	if err != nil {
		return nil, false, serviceLedgerSelectorError(selection, err)
	}
	return result.Value, result.Validated, nil
}

func serviceLedgerSelectorError(selection ledgerselector.Selector, err error) error {
	switch {
	case errors.Is(err, ledgerselector.ErrInvalidHash):
		return ErrInvalidLedgerHash
	case errors.Is(err, ledgerselector.ErrInvalidIndex), errors.Is(err, ledgerselector.ErrInvalidSelector):
		return ErrInvalidLedgerIndex
	case errors.Is(err, ledgerselector.ErrLedgerNotFound):
		switch selection.Kind() {
		case ledgerselector.KindAbsent, ledgerselector.KindCurrent, ledgerselector.KindClosed, ledgerselector.KindValidated:
			return ErrNoOpenLedger
		default:
			return ErrLedgerNotFound
		}
	default:
		return err
	}
}
