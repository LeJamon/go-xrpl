// Package skiplist manages the LedgerHashes skip-list SLEs that let a single
// ledger resolve the hashes of its ancestors. It owns the rolling 256-entry
// list (keylet::skip()) updated on every close and the per-64K-window
// historical list (keylet::skip(seq)) updated every 256th ledger.
package skiplist

import (
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
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

// ReadLedgerHashesSLE returns the decoded field map, Hashes, and LastLedgerSequence
// for the LedgerHashes SLE at key, or (nil, nil, 0, nil) when absent. The field map
// lets callers preserve every present field (notably optional FirstLedgerSequence).
func ReadLedgerHashesSLE(stateMap *shamap.SHAMap, key [32]byte) (map[string]any, [][32]byte, uint32, error) {
	item, found, err := stateMap.Get(key)
	if err != nil {
		return nil, nil, 0, err
	}
	if !found {
		return nil, nil, 0, nil
	}
	jsonObj, err := binarycodec.DecodeBytes(item.Data())
	if err != nil {
		return nil, nil, 0, fmt.Errorf("decode LedgerHashes: %w", err)
	}

	hashes, err := decodeHashesField(jsonObj)
	if err != nil {
		return nil, nil, 0, err
	}
	lastSeq, err := decodeUint32Field(jsonObj, "LastLedgerSequence")
	if err != nil {
		return nil, nil, 0, err
	}
	return jsonObj, hashes, lastSeq, nil
}

func decodeHashesField(jsonObj map[string]any) ([][32]byte, error) {
	rawHashes, ok := jsonObj["Hashes"]
	if !ok {
		return nil, nil
	}
	var hashStrings []string
	switch v := rawHashes.(type) {
	case []string:
		hashStrings = v
	case []any:
		hashStrings = make([]string, len(v))
		for i, h := range v {
			s, ok := h.(string)
			if !ok {
				return nil, fmt.Errorf("hash entry is not a string")
			}
			hashStrings[i] = s
		}
	default:
		return nil, fmt.Errorf("Hashes field has unexpected type %T", rawHashes)
	}

	result := make([][32]byte, 0, len(hashStrings))
	for _, hashStr := range hashStrings {
		hashBytes, err := hex.DecodeString(hashStr)
		if err != nil {
			return nil, fmt.Errorf("decode hash hex: %w", err)
		}
		var hash [32]byte
		copy(hash[:], hashBytes)
		result = append(result, hash)
	}
	return result, nil
}

// decodeUint32Field reads a STI_UINT32 field. binarycodec returns uint32, so any
// other type is a codec-drift signal worth surfacing rather than coercing.
func decodeUint32Field(jsonObj map[string]any, name string) (uint32, error) {
	raw, ok := jsonObj[name]
	if !ok {
		return 0, nil
	}
	v, ok := raw.(uint32)
	if !ok {
		return 0, fmt.Errorf("%s field has unexpected type %T (want uint32)", name, raw)
	}
	return v, nil
}

// ReadHashes returns the Hashes array from a LedgerHashes SLE, or nil when absent.
// Thin wrapper over ReadLedgerHashesSLE for callers needing only the vector.
func ReadHashes(stateMap *shamap.SHAMap, key [32]byte) ([][32]byte, error) {
	_, hashes, _, err := ReadLedgerHashesSLE(stateMap, key)
	return hashes, err
}

// Write serializes a LedgerHashes SLE to the state map. When fields is non-nil (an
// existing SLE's decoded map) it updates only Hashes and LastLedgerSequence and
// preserves every other present field — notably optional FirstLedgerSequence;
// rebuilding from a fixed field set would drop it and diverge account_hash. When
// fields is nil it creates a fresh entry (FirstLedgerSequence intentionally absent).
func Write(stateMap *shamap.SHAMap, key [32]byte, fields map[string]any, hashes [][32]byte, lastSeq uint32) error {
	hashHexes := make([]string, len(hashes))
	for i, h := range hashes {
		hashHexes[i] = fmt.Sprintf("%064X", h)
	}

	jsonObj := fields
	if jsonObj == nil {
		jsonObj = map[string]any{
			"LedgerEntryType": "LedgerHashes",
			"Flags":           uint32(0),
		}
	}
	jsonObj["Hashes"] = hashHexes
	jsonObj["LastLedgerSequence"] = lastSeq

	data, err := binarycodec.EncodeBytes(jsonObj)
	if err != nil {
		return fmt.Errorf("encode LedgerHashes: %w", err)
	}

	return stateMap.Put(key, data)
}
