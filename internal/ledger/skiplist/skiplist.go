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

// UpdateOnMap updates the LedgerHashes SLE(s) on a mutable SHAMap.
// Matches rippled's updateSkipList() in Ledger.cpp:878-943.
//
// Two operations:
// 1. Every 256th ledger: append parentHash to the historical skiplist (keylet::skip(seq))
// 2. Every ledger: append parentHash to the rolling-256 skiplist (keylet::skip())
//
// The function asserts the existing SLE — read from stateMap, which on the
// consensus close path is the snapshot of the just-closed parent — is
// internally consistent before mutating it. Specifically, an existing
// LastLedgerSequence must equal prevIndex-1 (i.e. the parent's own parent
// seq) and the existing Hashes vector must have the right length. If either
// invariant is violated, the SLE was mutated by a path other than a clean
// chain advance — typically a leak from a speculative consensus attempt
// (see issue #470). Failing the close loudly here prevents goxrpl from
// emitting a divergent ledger and forking the network.
func UpdateOnMap(stateMap *shamap.SHAMap, ledgerSeq uint32, parentHash [32]byte) error {
	prevIndex := ledgerSeq - 1

	// Genesis ledger (seq 1) has no parent to record
	if prevIndex == 0 {
		return nil
	}

	// Operation 1: Historical skiplist (every 256th ledger).
	// rippled appends without trimming; the historical list grows
	// monotonically and never rolls. The size cap mirrors rippled's
	// XRPL_ASSERT(hashes.size() <= 256) at Ledger.cpp:904-906 — a 64K
	// window holds at most 65536/256 = 256 entries.
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

	// Operation 2: Rolling 256 skiplist (every ledger)
	rollingKey := keylet.LedgerHashes()
	fields, hashes, lastSeq, err := ReadLedgerHashesSLE(stateMap, rollingKey.Key)
	if err != nil {
		return fmt.Errorf("read rolling skip list: %w", err)
	}
	if err := assertSkipListConsistent(hashes, lastSeq, prevIndex); err != nil {
		return fmt.Errorf("rolling LedgerHashes (key %x): %w", rollingKey.Key, err)
	}
	// Trim to 256: remove oldest if at capacity
	if len(hashes) >= 256 {
		hashes = hashes[1:]
	}
	hashes = append(hashes, parentHash)
	if err := Write(stateMap, rollingKey.Key, fields, hashes, prevIndex); err != nil {
		return fmt.Errorf("write rolling skip list: %w", err)
	}

	return nil
}

// assertSkipListConsistent validates the parent's rolling-256 LedgerHashes
// SLE before we append to it. An existing SLE must describe ledgers
// 1..prevIndex-1 — equivalently, LastLedgerSequence == prevIndex-1 and
// len(Hashes) == min(prevIndex-1, 256). Anything else means the SLE was
// mutated by a path that isn't a clean chain advance (issue #470 traces
// this to speculative-build leakage during consensus).
//
// An absent SLE is allowed: this is the first close after a fresh genesis,
// or the parent state was never threaded through updateSkipList (initial
// sync header-only adoption). Either way, we have nothing to validate.
func assertSkipListConsistent(hashes [][32]byte, lastSeq, prevIndex uint32) error {
	if len(hashes) == 0 && lastSeq == 0 {
		// Absent SLE — first append, nothing to assert.
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

// assertHistoricalSkipListConsistent validates the per-64K-window historical
// LedgerHashes SLE before appending. Rippled's only invariant here is
// `hashes.size() <= 256` (Ledger.cpp:904-906); we additionally require
// LastLedgerSequence to be the most recent 256-aligned seq strictly below
// prevIndex, which catches the same leak class as the rolling assertion
// without crossing window boundaries (a window covers 65536 ledgers, so
// within a single SLE lastSeq always == prevIndex - 256 after the prior
// append).
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

// ReadLedgerHashesSLE returns the decoded field map, Hashes, and
// LastLedgerSequence for the LedgerHashes SLE at key, or (nil, nil, 0, nil)
// when absent. The field map lets callers preserve every present field —
// notably the optional FirstLedgerSequence — when rewriting the SLE.
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

// decodeUint32Field reads a STI_UINT32 field from a binarycodec-decoded
// SLE. binarycodec/types.UInt32.ToJSON returns uint32, so that is the
// only type we expect; any other type is a codec-drift signal worth
// surfacing rather than silently coercing.
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

// ReadHashes reads and decodes the Hashes array from an existing
// LedgerHashes SLE in the state map. Returns nil if the entry doesn't exist.
// Thin wrapper over ReadLedgerHashesSLE that drops the LastLedgerSequence
// — kept for callers that only need the vector.
func ReadHashes(stateMap *shamap.SHAMap, key [32]byte) ([][32]byte, error) {
	_, hashes, _, err := ReadLedgerHashesSLE(stateMap, key)
	return hashes, err
}

// Write serializes a LedgerHashes SLE and writes it to the state map.
//
// When fields is non-nil — the decoded map of an existing SLE — it updates
// only Hashes and LastLedgerSequence and preserves every other present field,
// mirroring rippled's in-place updateSkipList (Ledger.cpp:878-943). This keeps
// the optional FirstLedgerSequence (and any future field) intact across the
// per-close rewrite; rebuilding from a fixed field set would silently drop it
// and shift the state-tree leaf, diverging account_hash.
//
// When fields is nil — no existing SLE — it creates a fresh entry. rippled's
// updateSkipList likewise sets only Hashes and LastLedgerSequence on a newly
// created SLE, so FirstLedgerSequence is intentionally absent here.
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
