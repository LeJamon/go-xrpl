package replaytool

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/statecompare"
	ledgerentry "github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/protocol"
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
	rules *amendment.Rules,
) (*shamap.SHAMap, bool, error) {
	txs, err := client.Transactions(ctx, ledgerIndex)
	if err != nil {
		return nil, false, fmt.Errorf("getting transactions: %w", err)
	}
	metas := make([]metaTx, len(txs))
	for i, t := range txs {
		metas[i] = metaTx{Blob: t.MetaBlob, TxHash: t.TxHash}
	}

	corrected, err := reconstructFromMetaWithRules(preState, metas, ledgerIndex, rules)
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
func reconstructFromMetaWithRules(preState *shamap.SHAMap, metas []metaTx, ledgerIndex uint32, rules *amendment.Rules) (*shamap.SHAMap, error) {
	if rules == nil {
		return nil, errors.New("reconstruction requires parent amendment rules")
	}
	corrected, err := preState.SnapshotMutable()
	if err != nil {
		return nil, fmt.Errorf("snapshotting pre-state: %w", err)
	}

	// Directory page sfIndexes is sMD_Never, so it cannot be overlaid from a
	// metadata delta; it is reconstructed from the per-page membership changes
	// collected while applying every affected node, then written in a second pass.
	deltas := map[[32]byte]*dirDelta{}
	deletedDirs := map[[32]byte]bool{}
	pendingDirectories := map[[32]byte]struct{}{}

	for i, m := range metas {
		if len(m.Blob) == 0 {
			return nil, fmt.Errorf("metadata for tx %d is empty", i)
		}
		meta, err := binarycodec.Decode(hex.EncodeToString(m.Blob))
		if err != nil {
			return nil, fmt.Errorf("decoding metadata for tx %d: %w", i, err)
		}
		affected, ok := meta["AffectedNodes"].([]any)
		if !ok {
			return nil, fmt.Errorf("metadata for tx %d has invalid AffectedNodes", i)
		}
		brokerAccounts := loanBrokerAccountsFromMeta(affected)
		for _, node := range affected {
			if err := applyAffectedNodeWithRules(corrected, node, m.TxHash, ledgerIndex, rules, deltas, deletedDirs, pendingDirectories, brokerAccounts); err != nil {
				return nil, fmt.Errorf("applying metadata for tx %d: %w", i, err)
			}
		}
	}

	if err := reconstructDirIndexes(corrected, deltas, deletedDirs); err != nil {
		return nil, fmt.Errorf("reconstructing directory pages: %w", err)
	}
	if err := validatePendingDirectories(corrected, pendingDirectories); err != nil {
		return nil, err
	}
	return corrected, nil
}

// applyAffectedNode applies one metadata AffectedNode (CreatedNode /
// ModifiedNode / DeletedNode) to the state map. Created objects are completed
// with the soeREQUIRED default-zero fields metadata omits; created and modified
// objects of threaded types are stamped with PreviousTxnID/PreviousTxnLgrSeq.
// Directory membership changes are accumulated into deltas for the second pass.
func applyAffectedNodeWithRules(
	state *shamap.SHAMap,
	node any,
	txHash [32]byte,
	ledgerSeq uint32,
	rules *amendment.Rules,
	deltas map[[32]byte]*dirDelta,
	deletedDirs map[[32]byte]bool,
	pendingDirectories map[[32]byte]struct{},
	brokerAccounts map[[32]byte][20]byte,
) error {
	kind, fields, err := affectedNodeFields(node)
	if err != nil {
		return err
	}
	idxHex, ok := fields["LedgerIndex"].(string)
	if !ok {
		return fmt.Errorf("%s has invalid LedgerIndex", kind)
	}
	idx, err := protocol.Hash256FromHex(idxHex)
	if err != nil {
		return fmt.Errorf("bad LedgerIndex %q: %w", idxHex, err)
	}
	entryType, ok := fields["LedgerEntryType"].(string)
	if !ok || !ledgerentry.HasTypedName(entryType) {
		return fmt.Errorf("%s %s has invalid LedgerEntryType %v", kind, idxHex, fields["LedgerEntryType"])
	}

	switch kind {
	case "DeletedNode":
		final, err := metadataObject(fields, "FinalFields")
		if err != nil {
			return err
		}
		if err := recordMembership(state, deltas, idx, entryType, final, false, brokerAccounts); err != nil {
			return fmt.Errorf("recording deleted %s membership: %w", idxHex, err)
		}
		if entryType == "DirectoryNode" {
			deletedDirs[idx] = true
			delete(pendingDirectories, idx)
		}
		if err := state.Delete(idx); err != nil {
			return fmt.Errorf("deleting %s: %w", idxHex, err)
		}

	case "CreatedNode":
		newFields, err := metadataObject(fields, "NewFields")
		if err != nil {
			return err
		}
		if item, found, err := state.Get(idx); err != nil {
			return fmt.Errorf("checking created %s: %w", idxHex, err)
		} else if found && item != nil {
			return fmt.Errorf("created %s already exists", idxHex)
		}
		obj := copyFields(newFields)
		if let, ok := fields["LedgerEntryType"]; ok {
			obj["LedgerEntryType"] = let
		}
		if entryType == "DirectoryNode" {
			delete(deletedDirs, idx)
			pendingDirectories[idx] = struct{}{}
		}
		if err := fillCreatedDefaults(obj, entryType); err != nil {
			return fmt.Errorf("completing created %s: %w", idxHex, err)
		}
		if err := fillBookDirectoryDefaults(obj, entryType); err != nil {
			return fmt.Errorf("completing created %s: %w", idxHex, err)
		}
		threadPreviousTxn(obj, entryType, txHash, ledgerSeq, rules)
		if err := recordMembership(state, deltas, idx, entryType, obj, true, brokerAccounts); err != nil {
			return fmt.Errorf("recording created %s membership: %w", idxHex, err)
		}
		if err := putEncoded(state, idx, obj); err != nil {
			return fmt.Errorf("creating %s: %w", idxHex, err)
		}

	case "ModifiedNode":
		obj, err := currentObject(state, idx, fields)
		if err != nil {
			return fmt.Errorf("reading %s: %w", idxHex, err)
		}
		final, err := metadataObject(fields, "FinalFields")
		if err != nil {
			return err
		}
		previous, err := metadataObject(fields, "PreviousFields")
		if err != nil {
			return err
		}
		// A field present in PreviousFields but absent from FinalFields
		// was removed by the transaction.
		for k := range previous {
			if _, kept := final[k]; !kept {
				delete(obj, k)
			}
		}
		maps.Copy(obj, final)
		threadPreviousTxn(obj, entryType, txHash, ledgerSeq, rules)
		if err := putEncoded(state, idx, obj); err != nil {
			return fmt.Errorf("modifying %s: %w", idxHex, err)
		}
	}
	return nil
}

func affectedNodeFields(node any) (string, map[string]any, error) {
	affectedNode, ok := node.(map[string]any)
	if !ok || len(affectedNode) != 1 {
		return "", nil, errors.New("affected node must contain exactly one node wrapper")
	}
	for kind, body := range affectedNode {
		switch kind {
		case "CreatedNode", "ModifiedNode", "DeletedNode":
		default:
			return "", nil, fmt.Errorf("unknown affected node wrapper %q", kind)
		}
		fields, ok := body.(map[string]any)
		if !ok {
			return "", nil, fmt.Errorf("%s body is not an object", kind)
		}
		return kind, fields, nil
	}
	panic("unreachable")
}

func metadataObject(fields map[string]any, name string) (map[string]any, error) {
	v, present := fields[name]
	if !present {
		return map[string]any{}, nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s is not an object", name)
	}
	return obj, nil
}

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
	return nil, shamap.ErrItemNotFound
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
	if obj["LedgerEntryType"] != "DirectoryNode" || obj["Indexes"] != nil {
		if err := validateEncodedEntry(obj, raw); err != nil {
			return err
		}
	}
	return state.Put(idx, raw)
}

func validateEncodedEntry(obj map[string]any, raw []byte) error {
	entryType, ok := obj["LedgerEntryType"].(string)
	if !ok {
		return errors.New("encoded object has invalid LedgerEntryType")
	}
	entry := ledgerentry.NewByName(entryType)
	if entry == nil {
		return fmt.Errorf("encoded object has unknown LedgerEntryType %q", entryType)
	}
	if err := entry.Decode(raw); err != nil {
		return fmt.Errorf("validating %s object: %w", entryType, err)
	}
	return nil
}

func validatePendingDirectories(state *shamap.SHAMap, pending map[[32]byte]struct{}) error {
	for key := range pending {
		item, found, err := state.Get(key)
		if err != nil {
			return fmt.Errorf("reading reconstructed directory %x: %w", key, err)
		}
		if !found || item == nil {
			return fmt.Errorf("reconstructed directory %x is missing", key)
		}
		obj, err := binarycodec.DecodeBytes(item.Data())
		if err != nil {
			return fmt.Errorf("decoding reconstructed directory %x: %w", key, err)
		}
		if err := validateEncodedEntry(obj, item.Data()); err != nil {
			return fmt.Errorf("reconstructed directory %x: %w", key, err)
		}
	}
	return nil
}

// divergingObjects returns the objects that differ between goXRPL's post-state
// and mainnet's reconstructed post-state, with both serialized sides and a
// decoded view for readability.
const (
	maxDiagnosticObjects = 1000
	maxDiagnosticBytes   = 16 << 20
)

func divergingObjects(goxrpl, mainnet *shamap.SHAMap) ([]divergingObject, error) {
	objects, _, err := divergingObjectsContext(context.Background(), goxrpl, mainnet)
	return objects, err
}

func divergingObjectsContext(ctx context.Context, goxrpl, mainnet *shamap.SHAMap) ([]divergingObject, bool, error) {
	return divergingObjectsContextBounded(ctx, goxrpl, mainnet, maxDiagnosticBytes)
}

func divergingObjectsContextBounded(ctx context.Context, goxrpl, mainnet *shamap.SHAMap, maxBytes int) ([]divergingObject, bool, error) {
	differences, err := goxrpl.CompareContext(ctx, mainnet, maxDiagnosticObjects)
	if err != nil {
		return nil, false, err
	}
	out := make([]divergingObject, 0, differences.Len())
	used := 0
	for _, difference := range differences.Differences {
		size := 0
		if difference.FirstItem != nil {
			size += difference.FirstItem.DataSize()
		}
		if difference.SecondItem != nil {
			size += difference.SecondItem.DataSize()
		}
		if size > maxBytes-used {
			return out, false, nil
		}
		used += size
		obj := divergingObject{Index: hex.EncodeToString(difference.Key[:])}
		if difference.FirstItem != nil {
			data := difference.FirstItem.Data()
			obj.GoXRPL = hex.EncodeToString(data)
			obj.GoXRPLDecoded = decodeEntryData(obj.GoXRPL)
		}
		if difference.SecondItem != nil {
			data := difference.SecondItem.Data()
			obj.Mainnet = hex.EncodeToString(data)
			obj.MainnetDecoded = decodeEntryData(obj.Mainnet)
		}
		out = append(out, obj)
	}
	return out, differences.Complete, nil
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
