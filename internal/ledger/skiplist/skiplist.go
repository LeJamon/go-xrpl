// Package skiplist manages the LedgerHashes skip-list SLEs that let a single
// ledger resolve the hashes of its ancestors. It owns the rolling 256-entry
// list (keylet::skip()) updated on every close and the per-64K-window
// historical list (keylet::skip(seq)) updated every 256th ledger.
package skiplist

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields"
	"github.com/LeJamon/go-xrpl/keylet"
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
	prevIndex := ledgerSeq - 1

	// Genesis ledger (seq 1) has no parent to record.
	if prevIndex == 0 {
		return nil
	}

	// Historical skiplist: append without trimming; grows monotonically up to
	// 256 entries (a 64K window holds 65536/256 = 256).
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
		if err := Write(stateMap, histKey.Key, fields, hashes, prevIndex); err != nil {
			return fmt.Errorf("write historical skip list: %w", err)
		}
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
	if err := Write(stateMap, rollingKey.Key, fields, hashes, prevIndex); err != nil {
		return fmt.Errorf("write rolling skip list: %w", err)
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
	return nil
}

// ReadLedgerHashesSLE returns the decoded entry, Hashes, and LastLedgerSequence
// for the LedgerHashes SLE at key, or (nil, nil, 0, nil) when absent.
func ReadLedgerHashesSLE(stateMap *shamap.SHAMap, key [32]byte) (*ledgerfields.LedgerHashes, [][32]byte, uint32, error) {
	return ReadLedgerHashesSLEContext(context.Background(), stateMap, key)
}

func ReadLedgerHashesSLEContext(ctx context.Context, stateMap *shamap.SHAMap, key [32]byte) (*ledgerfields.LedgerHashes, [][32]byte, uint32, error) {
	item, found, err := stateMap.GetContext(ctx, key)
	if err != nil {
		return nil, nil, 0, err
	}
	if !found {
		return nil, nil, 0, nil
	}
	entry := &ledgerfields.LedgerHashes{}
	if err := entry.Decode(item.Data()); err != nil {
		return nil, nil, 0, fmt.Errorf("decode LedgerHashes: %w", err)
	}

	hashes, err := decodeHashesField(entry.Hashes)
	if err != nil {
		return nil, nil, 0, err
	}
	return entry, hashes, entry.LastLedgerSequence, nil
}

func decodeHashesField(hashStrings []string) ([][32]byte, error) {
	result := make([][32]byte, 0, len(hashStrings))
	for _, hashStr := range hashStrings {
		hashBytes, err := hex.DecodeString(hashStr)
		if err != nil {
			return nil, fmt.Errorf("decode hash hex: %w", err)
		}
		if len(hashBytes) != 32 {
			return nil, fmt.Errorf("decoded hash length %d, want 32", len(hashBytes))
		}
		var hash [32]byte
		copy(hash[:], hashBytes)
		result = append(result, hash)
	}
	return result, nil
}

// ReadHashes returns the Hashes array from a LedgerHashes SLE, or nil when absent.
// Thin wrapper over ReadLedgerHashesSLE for callers needing only the vector.
func ReadHashes(stateMap *shamap.SHAMap, key [32]byte) ([][32]byte, error) {
	_, hashes, _, err := ReadLedgerHashesSLE(stateMap, key)
	return hashes, err
}

// Write serializes a LedgerHashes SLE to the state map. Existing entries retain
// every decoded optional field; fresh entries leave FirstLedgerSequence absent.
func Write(stateMap *shamap.SHAMap, key [32]byte, entry *ledgerfields.LedgerHashes, hashes [][32]byte, lastSeq uint32) error {
	hashHexes := make([]string, len(hashes))
	for i, h := range hashes {
		hashHexes[i] = fmt.Sprintf("%064X", h)
	}

	if entry == nil {
		entry = &ledgerfields.LedgerHashes{}
		entry.SetFlags(0)
	}
	entry.SetHashes(hashHexes)
	entry.SetLastLedgerSequence(lastSeq)

	data, err := entry.Encode()
	if err != nil {
		return fmt.Errorf("encode LedgerHashes: %w", err)
	}

	return stateMap.Put(key, data)
}
