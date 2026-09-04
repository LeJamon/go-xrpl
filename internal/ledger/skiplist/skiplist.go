// Package skiplist manages the LedgerHashes skip-list SLEs that let a single
// ledger resolve the hashes of its ancestors. It owns the rolling 256-entry
// list (keylet::skip()) updated on every close and the per-64K-window
// historical list (keylet::skip(seq)) updated every 256th ledger.
package skiplist

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/keylet"
	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/shamap"
)

// UpdateOnMap updates the LedgerHashes SLE(s) on a mutable SHAMap: every 256th
// ledger appends parentHash to the historical skiplist (keylet::skip(seq)), and
// every ledger appends it to the rolling-256 skiplist (keylet::skip()).
//
// It asserts the existing SLE is consistent before mutating; a violation means a
// non-chain-advance path mutated it (issue #470: speculative-consensus leakage).
// Failing loudly here prevents emitting a divergent ledger and forking the network.
func UpdateOnMap(stateMap *shamap.SHAMap, ledgerSeq uint32, parentHash [32]byte) error {
	if stateMap == nil {
		return errors.New("nil state map")
	}
	if ledgerSeq == 0 {
		return nil
	}
	prevIndex := ledgerSeq - 1

	// Genesis ledger (seq 1) has no parent to record.
	if prevIndex == 0 {
		return nil
	}

	// Historical skiplist: append without trimming; grows monotonically up to
	// 256 entries (a 64K window holds 65536/256 = 256).
	var writes []*shamap.Item
	if (prevIndex & 0xff) == 0 {
		histKey := keylet.LedgerHashesForSeq(prevIndex)
		fields, hashes, lastSeq, err := ReadLedgerHashesSLE(stateMap, histKey.Key)
		if err != nil {
			return fmt.Errorf("read historical skip list: %w", err)
		}
		if err := assertHistoricalSkipListConsistent(hashes, lastSeq, prevIndex); err != nil {
			return fmt.Errorf("historical LedgerHashes (key %x): %w", histKey.Key, err)
		}
		hashes = append(hashes, parentHash)
		data, err := encode(fields, hashes, prevIndex)
		if err != nil {
			return fmt.Errorf("encode historical skip list: %w", err)
		}
		writes = append(writes, shamap.NewItem(histKey.Key, data))
	}

	// Rolling-256 skiplist: every ledger.
	rollingKey := keylet.LedgerHashes()
	fields, hashes, lastSeq, err := ReadLedgerHashesSLE(stateMap, rollingKey.Key)
	if err != nil {
		return fmt.Errorf("read rolling skip list: %w", err)
	}
	if err := assertSkipListConsistent(hashes, lastSeq, prevIndex); err != nil {
		return fmt.Errorf("rolling LedgerHashes (key %x): %w", rollingKey.Key, err)
	}
	// Trim to 256: drop oldest at capacity.
	if len(hashes) >= 256 {
		hashes = hashes[1:]
	}
	hashes = append(hashes, parentHash)
	data, err := encode(fields, hashes, prevIndex)
	if err != nil {
		return fmt.Errorf("encode rolling skip list: %w", err)
	}
	writes = append(writes, shamap.NewItem(rollingKey.Key, data))

	if err := stateMap.PutItemsAtomically(writes...); err != nil {
		return fmt.Errorf("write skip lists: %w", err)
	}
	return nil
}

// assertSkipListConsistent validates the rolling-256 SLE before appending: an
// existing SLE must describe ledgers 1..prevIndex-1 (LastLedgerSequence ==
// prevIndex-1, len(Hashes) == min(prevIndex-1, 256)). Anything else is a
// non-chain-advance mutation (issue #470). An absent SLE is allowed (first close
// after genesis, or header-only adoption during initial sync).
func assertSkipListConsistent(hashes [][32]byte, lastSeq, prevIndex uint32) error {
	if len(hashes) == 0 && lastSeq == 0 {
		return nil
	}
	wantLastSeq := prevIndex - 1
	if lastSeq != wantLastSeq {
		return fmt.Errorf("existing LastLedgerSequence=%d, want %d (prevIndex-1); state was mutated by a non-chain-advance path",
			lastSeq, wantLastSeq)
	}
	wantLen := min(int(prevIndex-1), 256)
	if len(hashes) != wantLen {
		return fmt.Errorf("existing Hashes length=%d, want %d for prevIndex=%d; state was mutated by a non-chain-advance path",
			len(hashes), wantLen, prevIndex)
	}
	return nil
}

// assertHistoricalSkipListConsistent validates the historical SLE before
// appending: hashes.size() <= 256, and LastLedgerSequence is the most recent
// 256-aligned seq below prevIndex (== prevIndex-256). Catches the same leak class
// as the rolling assertion without crossing the 64K window boundary.
func assertHistoricalSkipListConsistent(hashes [][32]byte, lastSeq, prevIndex uint32) error {
	if len(hashes) == 0 && lastSeq == 0 {
		return nil
	}
	if len(hashes) > 256 {
		return fmt.Errorf("existing Hashes length=%d exceeds 256", len(hashes))
	}
	if wantLastSeq := prevIndex - 256; lastSeq != wantLastSeq {
		return fmt.Errorf("existing LastLedgerSequence=%d, want %d (prevIndex-256); state was mutated by a non-chain-advance path",
			lastSeq, wantLastSeq)
	}
	offset := prevIndex & 0xffff
	wantLen := 0
	if prevIndex>>16 != 0 {
		wantLen = int(offset >> 8)
	} else if offset != 0 {
		wantLen = int(offset>>8) - 1
	}
	if len(hashes) != wantLen {
		return fmt.Errorf("existing Hashes length=%d, want %d for historical page", len(hashes), wantLen)
	}
	return nil
}

// ReadLedgerHashesSLE returns the decoded fields, Hashes, and LastLedgerSequence
// for the LedgerHashes SLE at key, or (nil, nil, 0, nil) when absent.
func ReadLedgerHashesSLE(stateMap *shamap.SHAMap, key [32]byte) (*LedgerHashesFields, [][32]byte, uint32, error) {
	return ReadLedgerHashesSLEContext(context.Background(), stateMap, key)
}

func ReadLedgerHashesSLEContext(ctx context.Context, stateMap *shamap.SHAMap, key [32]byte) (*LedgerHashesFields, [][32]byte, uint32, error) {
	item, found, err := stateMap.GetContext(ctx, key)
	if err != nil {
		return nil, nil, 0, err
	}
	if !found {
		return nil, nil, 0, nil
	}
	fields, hashes, err := decodeLedgerHashes(item.Data())
	if err != nil {
		return nil, nil, 0, fmt.Errorf("decode LedgerHashes: %w", err)
	}

	if !fields.hasLast {
		return nil, nil, 0, errors.New("LedgerHashes missing LastLedgerSequence")
	}
	if len(hashes) == 0 || len(hashes) > 256 {
		return nil, nil, 0, fmt.Errorf("LedgerHashes has invalid Hashes cardinality %d", len(hashes))
	}
	if fields.hasFirst {
		if fields.FirstLedgerSequence == 0 || fields.FirstLedgerSequence > fields.LastLedgerSequence {
			return nil, nil, 0, errors.New("LedgerHashes has invalid FirstLedgerSequence")
		}
	}
	return fields, hashes, fields.LastLedgerSequence, nil
}

// ReadHashes returns the Hashes array from a LedgerHashes SLE, or nil when absent.
// Thin wrapper over ReadLedgerHashesSLE for callers needing only the vector.
func ReadHashes(stateMap *shamap.SHAMap, key [32]byte) ([][32]byte, error) {
	_, hashes, _, err := ReadLedgerHashesSLE(stateMap, key)
	return hashes, err
}

// Write serializes a LedgerHashes SLE to the state map. Existing entries retain
// every decoded optional field; fresh entries leave FirstLedgerSequence absent.
func Write(stateMap *shamap.SHAMap, key [32]byte, fields *LedgerHashesFields, hashes [][32]byte, lastSeq uint32) error {
	data, err := encode(fields, hashes, lastSeq)
	if err != nil {
		return err
	}
	return stateMap.Put(key, data)
}

func encode(fields *LedgerHashesFields, hashes [][32]byte, lastSeq uint32) ([]byte, error) {
	hashHexes := make([]string, len(hashes))
	for i, h := range hashes {
		hashHexes[i] = fmt.Sprintf("%064X", h)
	}

	entry := &ledgerfields.LedgerHashes{}
	if fields == nil {
		entry.SetFlags(0)
	} else {
		entry.SetFlags(fields.Flags)
		if fields.hasFirst {
			entry.SetFirstLedgerSequence(fields.FirstLedgerSequence)
		}
		if fields.hasLedgerIndex {
			entry.SetLedgerIndex(fields.LedgerIndex)
		}
		if fields.hasSponsor {
			entry.SetSponsor(fields.Sponsor)
		}
	}
	entry.SetHashes(hashHexes)
	entry.SetLastLedgerSequence(lastSeq)

	data, err := entry.Encode()
	if err != nil {
		return nil, fmt.Errorf("encode LedgerHashes: %w", err)
	}
	return data, nil
}
