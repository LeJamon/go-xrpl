package replaytool

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/statecompare"
	"github.com/LeJamon/go-xrpl/shamap"
)

// reconstructMainnetState derives mainnet's exact post-transaction account
// state for a ledger by applying the per-transaction metadata deltas to the
// (mainnet-correct) pre-state. It returns the reconstructed map and whether its
// root hash matches mainnet's expected account_hash.
//
// Transaction metadata stores deltas, not full objects (rippled emits only
// changed/always fields per ApplyStateTable.cpp), so each ModifiedNode is
// overlaid onto the decoded pre-object; CreatedNode/DeletedNode are applied
// directly. The account_hash check is the gate that makes "reset to ground
// truth and continue" safe: replay only resumes when the reconstruction is
// byte-exact, never on a best-effort approximation.
func reconstructMainnetState(
	ctx context.Context,
	client *statecompare.Client,
	preState *shamap.SHAMap,
	ledgerIndex uint32,
	expectedAccountHash [32]byte,
) (*shamap.SHAMap, bool, error) {
	txs, err := client.GetTransactions(ctx, ledgerIndex)
	if err != nil {
		return nil, false, fmt.Errorf("getting transactions: %w", err)
	}
	metas := make([]metaTx, len(txs))
	for i, t := range txs {
		metas[i] = metaTx{Blob: t.MetaBlob, TxHash: t.TxHash}
	}

	corrected, err := reconstructFromMeta(preState, metas, ledgerIndex)
	if err != nil {
		return nil, false, err
	}

	root, err := corrected.Hash()
	if err != nil {
		return nil, false, fmt.Errorf("computing reconstructed root: %w", err)
	}
	return corrected, root == expectedAccountHash, nil
}

// metaTx pairs a transaction's metadata blob with its hash. The hash is threaded
// into PreviousTxnID on every SLE the transaction created or modified — a field
// metadata never carries (sMD_DeleteFinal) but the real state SLE always does.
type metaTx struct {
	Blob   []byte
	TxHash [32]byte
}

// reconstructFromMeta applies an ordered list of per-transaction metadata to a
// copy of preState and returns the resulting state map. metas are in ledger
// (tx_index) order. A second pass rebuilds directory page contents (sfIndexes),
// which metadata never carries.
func reconstructFromMeta(preState *shamap.SHAMap, metas []metaTx, ledgerIndex uint32) (*shamap.SHAMap, error) {
	corrected, err := preState.Snapshot(true)
	if err != nil {
		return nil, fmt.Errorf("snapshotting pre-state: %w", err)
	}

	// Directory page sfIndexes is sMD_Never, so it cannot be overlaid from a
	// metadata delta; it is reconstructed from the per-page membership changes
	// collected while applying every affected node, then written in a second pass.
	deltas := map[[32]byte]*dirDelta{}
	deletedDirs := map[[32]byte]bool{}

	for i, m := range metas {
		if len(m.Blob) == 0 {
			continue
		}
		meta, err := binarycodec.Decode(hex.EncodeToString(m.Blob))
		if err != nil {
			return nil, fmt.Errorf("decoding metadata for tx %d: %w", i, err)
		}
		affected, ok := meta["AffectedNodes"].([]any)
		if !ok {
			continue
		}
		for _, node := range affected {
			if err := applyAffectedNode(corrected, node, m.TxHash, ledgerIndex, deltas, deletedDirs); err != nil {
				return nil, fmt.Errorf("applying metadata for tx %d: %w", i, err)
			}
		}
	}

	if err := reconstructDirIndexes(corrected, deltas, deletedDirs); err != nil {
		return nil, fmt.Errorf("reconstructing directory pages: %w", err)
	}
	return corrected, nil
}

// applyAffectedNode applies one metadata AffectedNode (CreatedNode /
// ModifiedNode / DeletedNode) to the state map. Created objects are completed
// with the soeREQUIRED default-zero fields metadata omits; created and modified
// objects of threaded types are stamped with PreviousTxnID/PreviousTxnLgrSeq.
// Directory membership changes are accumulated into deltas for the second pass.
func applyAffectedNode(
	state *shamap.SHAMap,
	node any,
	txHash [32]byte,
	ledgerSeq uint32,
	deltas map[[32]byte]*dirDelta,
	deletedDirs map[[32]byte]bool,
) error {
	affectedNode, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	for kind, body := range affectedNode {
		fields, ok := body.(map[string]any)
		if !ok {
			continue
		}
		idxHex, _ := fields["LedgerIndex"].(string)
		idx, err := decodeIndex(idxHex)
		if err != nil {
			return fmt.Errorf("bad LedgerIndex %q: %w", idxHex, err)
		}
		entryType, _ := fields["LedgerEntryType"].(string)

		switch kind {
		case "DeletedNode":
			recordMembership(deltas, idx, entryType, asMap(fields["FinalFields"]), false)
			if entryType == "DirectoryNode" {
				deletedDirs[idx] = true
			}
			if err := state.Delete(idx); err != nil && !errors.Is(err, shamap.ErrItemNotFound) {
				return fmt.Errorf("deleting %s: %w", idxHex, err)
			}

		case "CreatedNode":
			obj := copyFields(fields["NewFields"])
			if let, ok := fields["LedgerEntryType"]; ok {
				obj["LedgerEntryType"] = let
			}
			fillRequiredDefaults(obj, entryType)
			threadPreviousTxn(obj, entryType, txHash, ledgerSeq)
			recordMembership(deltas, idx, entryType, obj, true)
			if err := putEncoded(state, idx, obj); err != nil {
				return fmt.Errorf("creating %s: %w", idxHex, err)
			}

		case "ModifiedNode":
			obj, err := currentObject(state, idx, fields)
			if err != nil {
				return fmt.Errorf("reading %s: %w", idxHex, err)
			}
			final := asMap(fields["FinalFields"])
			previous := asMap(fields["PreviousFields"])
			// A field present in PreviousFields but absent from FinalFields
			// was removed by the transaction.
			for k := range previous {
				if _, kept := final[k]; !kept {
					delete(obj, k)
				}
			}
			maps.Copy(obj, final)
			threadPreviousTxn(obj, entryType, txHash, ledgerSeq)
			if err := putEncoded(state, idx, obj); err != nil {
				return fmt.Errorf("modifying %s: %w", idxHex, err)
			}
		}
	}
	return nil
}

// currentObject returns the decoded object at idx, or an empty object carrying
// the AffectedNode's LedgerEntryType when the entry is not yet present.
func currentObject(state *shamap.SHAMap, idx [32]byte, fields map[string]any) (map[string]any, error) {
	item, found, err := state.Get(idx)
	if err != nil {
		return nil, err
	}
	if found && item != nil {
		decoded, err := binarycodec.Decode(hex.EncodeToString(item.Data()))
		if err != nil {
			return nil, fmt.Errorf("decoding object: %w", err)
		}
		return decoded, nil
	}
	obj := map[string]any{}
	if let, ok := fields["LedgerEntryType"]; ok {
		obj["LedgerEntryType"] = let
	}
	return obj, nil
}

// putEncoded encodes obj to canonical SLE bytes and stores it at idx.
func putEncoded(state *shamap.SHAMap, idx [32]byte, obj map[string]any) error {
	encoded, err := binarycodec.Encode(obj)
	if err != nil {
		return fmt.Errorf("encoding object: %w", err)
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("decoding encoded hex: %w", err)
	}
	return state.Put(idx, raw)
}

// divergingObjects returns the objects that differ between goXRPL's post-state
// and mainnet's reconstructed post-state, with both serialized sides and a
// decoded view for readability.
func divergingObjects(goxrpl, mainnet *shamap.SHAMap) ([]divergingObject, error) {
	keys, err := goxrpl.FindDifference(mainnet)
	if err != nil {
		return nil, err
	}
	out := make([]divergingObject, 0, len(keys))
	for _, key := range keys {
		obj := divergingObject{Index: hex.EncodeToString(key[:])}
		if item, found, err := goxrpl.Get(key); err == nil && found && item != nil {
			obj.GoXRPL = hex.EncodeToString(item.Data())
			obj.GoXRPLDecoded = decodeEntryData(obj.GoXRPL)
		}
		if item, found, err := mainnet.Get(key); err == nil && found && item != nil {
			obj.Mainnet = hex.EncodeToString(item.Data())
			obj.MainnetDecoded = decodeEntryData(obj.Mainnet)
		}
		out = append(out, obj)
	}
	return out, nil
}

// decodeIndex parses a 32-byte hex ledger index.
func decodeIndex(s string) ([32]byte, error) {
	var idx [32]byte
	b, err := hex.DecodeString(s)
	if err != nil {
		return idx, err
	}
	if len(b) != 32 {
		return idx, fmt.Errorf("expected 32 bytes, got %d", len(b))
	}
	copy(idx[:], b)
	return idx, nil
}

// asMap returns v as a map[string]any, or an empty map when v is absent.
func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// copyFields returns a shallow copy of v as a map[string]any so the caller can
// mutate it without aliasing the decoded metadata.
func copyFields(v any) map[string]any {
	src := asMap(v)
	out := make(map[string]any, len(src))
	maps.Copy(out, src)
	return out
}
