package replaytool

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	ledgerentry "github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

// A DirectoryNode page's sfIndexes (its membership list) is sMD_Never: it never
// appears in transaction metadata, so it cannot be overlaid from a metadata
// delta the way every other field is. We instead reconstruct it from the objects
// the ledger added to / removed from each page, mirroring rippled's directory
// machinery:
//
//   - Owner directories, and every directory other than order books (including
//     NFToken-offer directories), are kept sorted by key on each insert
//     (ApplyView dirInsert), and removals preserve order.
//   - Order-book directories preserve insertion order, appending new offers to
//     the tail (ApplyView dirAppend); removals preserve order.
//
// An object's final OwnerNode/BookNode (etc.) pin it to a specific page, so we
// never have to simulate page splitting — each added/removed key is attributed
// to the page its own node-pointer names.

// dirStrategy is how a directory page orders its sfIndexes.
type dirStrategy int

const (
	dirSorted dirStrategy = iota // dirInsert: page kept sorted by key
	dirAppend                    // dirAppend: offer books, insertion order
)

// dirPlacement is one directory page an object is listed in.
type dirPlacement struct {
	Key      [32]byte
	Strategy dirStrategy
}

type dirOperation struct {
	key [32]byte
	add bool
}

type dirDelta struct {
	strategy   dirStrategy
	operations []dirOperation
}

// recordMembership attributes an object's creation (isAdd) or deletion to every
// directory page it belongs to, accumulating the per-page deltas.
func recordMembership(state *shamap.SHAMap, deltas map[[32]byte]*dirDelta, objKey [32]byte, entryType string, fields map[string]any, isAdd bool, brokerAccounts map[[32]byte][20]byte) error {
	placements, err := directoryPlacements(state, entryType, fields, brokerAccounts)
	if err != nil {
		return err
	}
	for _, p := range placements {
		d := deltas[p.Key]
		if d == nil {
			d = &dirDelta{strategy: p.Strategy}
			deltas[p.Key] = d
		}
		d.operations = append(d.operations, dirOperation{key: objKey, add: isAdd})
	}
	return nil
}

// directoryPlacements returns the directory pages an object of entryType with
// the given fields is listed in. The fields come from a CreatedNode's (default-
// filled) NewFields or a DeletedNode's FinalFields, both of which carry the
// node-pointer and owner fields needed to locate each page.
func directoryPlacements(state *shamap.SHAMap, entryType string, fields map[string]any, brokerAccounts map[[32]byte][20]byte) ([]dirPlacement, error) {
	for name, value := range fields {
		if name == "Flags" || strings.HasSuffix(name, "Node") {
			if _, err := parseMetaUint64(value); err != nil {
				return nil, fmt.Errorf("%s has invalid %s: %w", entryType, name, err)
			}
		}
	}
	var out []dirPlacement
	add := func(k keylet.Keylet, s dirStrategy) { out = append(out, dirPlacement{Key: k.Key, Strategy: s}) }

	switch entryType {
	case "DirectoryNode", "Amendments", "FeeSettings", "NegativeUNL", "LedgerHashes", "AccountRoot":
		// Directory pages and singletons are not themselves listed in a directory.
		return nil, nil

	case "RippleState":
		// A trust line is listed in both endpoints' owner directories.
		if lo, ok := metaIssuer(fields, "LowLimit"); ok {
			add(keylet.OwnerDirPage(lo, metaUint64(fields["LowNode"])), dirSorted)
		}
		if hi, ok := metaIssuer(fields, "HighLimit"); ok {
			add(keylet.OwnerDirPage(hi, metaUint64(fields["HighNode"])), dirSorted)
		}
		return out, nil

	case "Credential":
		// The issuer always lists the credential in its directory. The subject
		// lists it too, except for a self-issued credential (subject == issuer),
		// which carries no SubjectNode and is listed once.
		if iss, ok := metaAccountID(fields, "Issuer"); ok {
			add(keylet.OwnerDirPage(iss, metaUint64(fields["IssuerNode"])), dirSorted)
		}
		if _, has := fields["SubjectNode"]; has {
			if sub, ok := metaAccountID(fields, "Subject"); ok {
				add(keylet.OwnerDirPage(sub, metaUint64(fields["SubjectNode"])), dirSorted)
			}
		}
		return out, nil

	case "Delegate":
		delegator, ok := metaAccountID(fields, "Account")
		if !ok {
			return nil, fmt.Errorf("Delegate has invalid Account")
		}
		authorized, ok := metaAccountID(fields, "Authorize")
		if !ok {
			return nil, fmt.Errorf("Delegate has invalid Authorize")
		}
		add(keylet.OwnerDirPage(delegator, metaUint64(fields["OwnerNode"])), dirSorted)
		add(keylet.OwnerDirPage(authorized, metaUint64(fields["DestinationNode"])), dirSorted)
		return out, nil

	case "Vault":
		if owner, ok := metaAccountID(fields, "Owner"); ok {
			add(keylet.OwnerDirPage(owner, metaUint64(fields["OwnerNode"])), dirSorted)
		}
		return out, nil

	case "Loan":
		borrower, ok := metaAccountID(fields, "Borrower")
		if !ok {
			return nil, fmt.Errorf("Loan has invalid Borrower")
		}
		brokerID, ok := metaHash256(fields, "LoanBrokerID")
		if !ok {
			return nil, fmt.Errorf("Loan has invalid LoanBrokerID")
		}
		broker, err := loanBrokerAccount(state, brokerID, brokerAccounts)
		if err != nil {
			return nil, err
		}
		add(keylet.OwnerDirPage(borrower, metaUint64(fields["OwnerNode"])), dirSorted)
		add(keylet.OwnerDirPage(broker, metaUint64(fields["LoanBrokerNode"])), dirSorted)
		return out, nil

	case "LoanBroker":
		owner, ok := metaAccountID(fields, "Owner")
		if !ok {
			return nil, fmt.Errorf("LoanBroker has invalid Owner")
		}
		vaultID, ok := metaHash256(fields, "VaultID")
		if !ok {
			return nil, fmt.Errorf("LoanBroker has invalid VaultID")
		}
		vault, err := vaultDirectoryAccount(state, vaultID)
		if err != nil {
			return nil, err
		}
		add(keylet.OwnerDirPage(owner, metaUint64(fields["OwnerNode"])), dirSorted)
		add(keylet.OwnerDirPage(vault, metaUint64(fields["VaultNode"])), dirSorted)
		return out, nil

	case "MPTokenIssuance":
		if issuer, ok := metaAccountID(fields, "Issuer"); ok {
			add(keylet.OwnerDirPage(issuer, metaUint64(fields["OwnerNode"])), dirSorted)
		}
		return out, nil
	}

	// Owner directory: the object's owner lists it at OwnerNode. The owner is the
	// Account field, or Owner for the types that have no Account.
	if owner, ok := metaAccountID(fields, "Account"); ok {
		add(keylet.OwnerDirPage(owner, metaUint64(fields["OwnerNode"])), dirSorted)
	} else if owner, ok := metaAccountID(fields, "Owner"); ok {
		add(keylet.OwnerDirPage(owner, metaUint64(fields["OwnerNode"])), dirSorted)
	}

	switch entryType {
	case "Offer":
		// Offers are additionally listed in their order-book directory, which is
		// append-ordered.
		if book, ok := metaHash256(fields, "BookDirectory"); ok {
			add(keylet.DirPage(book, metaUint64(fields["BookNode"])), dirAppend)
		}
		if raw, present := fields["AdditionalBooks"]; present {
			books, ok := raw.([]any)
			if !ok {
				return nil, fmt.Errorf("Offer has invalid AdditionalBooks type %T", raw)
			}
			for i, rawBook := range books {
				wrapper, ok := rawBook.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("Offer AdditionalBooks[%d] has invalid type %T", i, rawBook)
				}
				bookFields, ok := wrapper["Book"].(map[string]any)
				if !ok {
					return nil, fmt.Errorf("Offer AdditionalBooks[%d] has invalid Book", i)
				}
				book, ok := metaHash256(bookFields, "BookDirectory")
				if !ok {
					return nil, fmt.Errorf("Offer AdditionalBooks[%d] has invalid BookDirectory", i)
				}
				node, err := parseMetaUint64(bookFields["BookNode"])
				if err != nil {
					return nil, fmt.Errorf("Offer AdditionalBooks[%d] has invalid BookNode: %w", i, err)
				}
				add(keylet.DirPage(book, node), dirAppend)
			}
		}

	case "NFTokenOffer":
		// NFToken offers are additionally listed in the per-token buy or sell
		// offer directory.
		if nft, ok := metaHash256(fields, "NFTokenID"); ok {
			root := keylet.NFTBuys(nft)
			if metaUint64(fields["Flags"])&uint64(ledgerentry.LsfSellNFToken) != 0 {
				root = keylet.NFTSells(nft)
			}
			add(keylet.DirPage(root.Key, metaUint64(fields["NFTokenOfferNode"])), dirSorted)
		}

	case "Check", "Escrow", "PayChannel":
		// When threaded to the destination (DestinationNode present), the object
		// is also listed in the destination's owner directory.
		if dest, ok := metaAccountID(fields, "Destination"); ok {
			if _, has := fields["DestinationNode"]; has {
				add(keylet.OwnerDirPage(dest, metaUint64(fields["DestinationNode"])), dirSorted)
			}
		}
		// An IOU escrow is additionally listed in the issuer's owner directory
		// (IssuerNode present) to track the locked balance.
		if entryType == "Escrow" {
			if iss, ok := metaIssuer(fields, "Amount"); ok {
				if _, has := fields["IssuerNode"]; has {
					add(keylet.OwnerDirPage(iss, metaUint64(fields["IssuerNode"])), dirSorted)
				}
			}
		}
	}

	return out, nil
}

func loanBrokerAccount(state *shamap.SHAMap, brokerID [32]byte, brokerAccounts map[[32]byte][20]byte) ([20]byte, error) {
	if account, ok := brokerAccounts[brokerID]; ok {
		return account, nil
	}
	brokerKey := keylet.LoanBrokerByID(brokerID).Key
	item, found, err := state.Get(brokerKey)
	if err != nil {
		return [20]byte{}, fmt.Errorf("reading LoanBroker %X: %w", brokerID, err)
	}
	if !found || item == nil {
		return [20]byte{}, fmt.Errorf("LoanBroker %X not found", brokerID)
	}
	broker, err := binarycodec.Decode(hex.EncodeToString(item.Data()))
	if err != nil {
		return [20]byte{}, fmt.Errorf("decoding LoanBroker %X: %w", brokerID, err)
	}
	if broker["LedgerEntryType"] != "LoanBroker" {
		return [20]byte{}, fmt.Errorf("LoanBroker %X resolved to %v", brokerID, broker["LedgerEntryType"])
	}
	account, ok := metaAccountID(broker, "Account")
	if !ok {
		return [20]byte{}, fmt.Errorf("LoanBroker %X has invalid Account", brokerID)
	}
	return account, nil
}

func vaultDirectoryAccount(state *shamap.SHAMap, vaultID [32]byte) ([20]byte, error) {
	vaultKey := keylet.VaultByID(vaultID).Key
	item, found, err := state.Get(vaultKey)
	if err != nil {
		return [20]byte{}, fmt.Errorf("reading Vault %X: %w", vaultID, err)
	}
	if !found || item == nil {
		return [20]byte{}, fmt.Errorf("Vault %X not found", vaultID)
	}
	vault, err := binarycodec.Decode(hex.EncodeToString(item.Data()))
	if err != nil {
		return [20]byte{}, fmt.Errorf("decoding Vault %X: %w", vaultID, err)
	}
	if vault["LedgerEntryType"] != "Vault" {
		return [20]byte{}, fmt.Errorf("Vault %X resolved to %v", vaultID, vault["LedgerEntryType"])
	}
	account, ok := metaAccountID(vault, "Account")
	if !ok {
		return [20]byte{}, fmt.Errorf("Vault %X has invalid Account", vaultID)
	}
	return account, nil
}

func loanBrokerAccountsFromMeta(affected []any) map[[32]byte][20]byte {
	accounts := make(map[[32]byte][20]byte)
	for _, node := range affected {
		affectedNode, ok := node.(map[string]any)
		if !ok {
			continue
		}
		for kind, body := range affectedNode {
			fields, ok := body.(map[string]any)
			if !ok || fields["LedgerEntryType"] != "LoanBroker" {
				continue
			}
			index, ok := fields["LedgerIndex"].(string)
			if !ok {
				continue
			}
			brokerID, err := protocol.Hash256FromHex(index)
			if err != nil {
				continue
			}
			var values map[string]any
			switch kind {
			case "CreatedNode":
				values = asMap(fields["NewFields"])
			case "DeletedNode", "ModifiedNode":
				values = asMap(fields["FinalFields"])
			}
			if account, ok := metaAccountID(values, "Account"); ok {
				accounts[brokerID] = account
			}
		}
	}
	return accounts
}

// reconstructDirIndexes rewrites the sfIndexes of every directory page touched
// this ledger, applying the accumulated membership deltas to the page's prior
// contents (already present in state from the metadata pass). Deleted pages are
// skipped.
func reconstructDirIndexes(state *shamap.SHAMap, deltas map[[32]byte]*dirDelta, deleted map[[32]byte]bool) error {
	for pageKey, d := range deltas {
		if deleted[pageKey] {
			continue
		}
		item, found, err := state.Get(pageKey)
		if err != nil {
			return fmt.Errorf("reading directory page %x: %w", pageKey[:4], err)
		}
		if !found || item == nil {
			continue
		}
		obj, err := binarycodec.Decode(hex.EncodeToString(item.Data()))
		if err != nil {
			return fmt.Errorf("decoding directory page %x: %w", pageKey[:4], err)
		}
		members, err := decodeIndexes(obj["Indexes"], true)
		if err != nil {
			return fmt.Errorf("decoding directory page %x indexes: %w", pageKey[:4], err)
		}
		members = applyDirDelta(members, d)
		obj["Indexes"] = encodeIndexes(members)
		if err := putEncoded(state, pageKey, obj); err != nil {
			return fmt.Errorf("re-encoding directory page %x: %w", pageKey[:4], err)
		}
	}
	return nil
}

// applyDirDelta applies one page's membership operations in transaction order.
// Removals preserve relative order. Sorted-directory additions sort the page
// before insertion; append-ordered additions go to the tail.
func applyDirDelta(members [][32]byte, d *dirDelta) [][32]byte {
	present := make(map[[32]byte]bool, len(members))
	for _, k := range members {
		present[k] = true
	}
	for _, op := range d.operations {
		if op.add {
			if !present[op.key] {
				if d.strategy == dirSorted {
					sort.Slice(members, func(i, j int) bool {
						return bytes.Compare(members[i][:], members[j][:]) < 0
					})
					at := sort.Search(len(members), func(i int) bool {
						return bytes.Compare(members[i][:], op.key[:]) >= 0
					})
					members = append(members, [32]byte{})
					copy(members[at+1:], members[at:])
					members[at] = op.key
				} else {
					members = append(members, op.key)
				}
				present[op.key] = true
			}
			continue
		}
		if !present[op.key] {
			continue
		}
		for i, key := range members {
			if key == op.key {
				members = append(members[:i], members[i+1:]...)
				break
			}
		}
		delete(present, op.key)
	}

	return members
}

// decodeIndexes parses a directory page's sfIndexes value into 32-byte keys.
func decodeIndexes(v any, allowMissing bool) ([][32]byte, error) {
	var out [][32]byte
	appendHex := func(s string) error {
		k, err := protocol.Hash256FromHex(s)
		if err != nil {
			return err
		}
		out = append(out, k)
		return nil
	}
	switch t := v.(type) {
	case []any:
		for i, e := range t {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("index %d has type %T", i, e)
			}
			if err := appendHex(s); err != nil {
				return nil, fmt.Errorf("index %d: %w", i, err)
			}
		}
	case []string:
		for i, s := range t {
			if err := appendHex(s); err != nil {
				return nil, fmt.Errorf("index %d: %w", i, err)
			}
		}
	case nil:
		if !allowMissing {
			return nil, errors.New("missing Indexes")
		}
	default:
		return nil, fmt.Errorf("unexpected type %T", v)
	}
	return out, nil
}

// encodeIndexes renders 32-byte keys as the uppercase-hex string array the
// binary codec expects for sfIndexes.
func encodeIndexes(members [][32]byte) []string {
	out := make([]string, len(members))
	for i, k := range members {
		out[i] = protocol.Hash256Hex(k)
	}
	return out
}

func metaUint64(v any) uint64 {
	if v == nil {
		return 0
	}
	n, _ := parseMetaUint64(v)
	return n
}

func parseMetaUint64(v any) (uint64, error) {
	switch t := v.(type) {
	case string:
		n, err := strconv.ParseUint(t, 16, 64)
		return n, err
	case float64:
		if t < 0 || t >= math.Exp2(64) || math.Trunc(t) != t {
			return 0, fmt.Errorf("%v is outside uint64", t)
		}
		return uint64(t), nil
	case uint32:
		return uint64(t), nil
	case uint64:
		return t, nil
	case int:
		if t < 0 {
			return 0, fmt.Errorf("%d is outside uint64", t)
		}
		return uint64(t), nil
	case int64:
		if t < 0 {
			return 0, fmt.Errorf("%d is outside uint64", t)
		}
		return uint64(t), nil
	case nil:
		return 0, errors.New("missing value")
	default:
		return 0, fmt.Errorf("unexpected type %T", v)
	}
}

// metaAccountID decodes a classic-address metadata field into an account ID.
func metaAccountID(fields map[string]any, key string) ([20]byte, bool) {
	s, ok := fields[key].(string)
	if !ok || s == "" {
		return [20]byte{}, false
	}
	id, err := state.DecodeAccountID(s)
	if err != nil {
		return [20]byte{}, false
	}
	return id, true
}

// metaIssuer decodes the issuer account out of an amount/limit metadata field.
func metaIssuer(fields map[string]any, key string) ([20]byte, bool) {
	m, ok := fields[key].(map[string]any)
	if !ok {
		return [20]byte{}, false
	}
	return metaAccountID(m, "issuer")
}

// metaHash256 decodes a 32-byte hex metadata field.
func metaHash256(fields map[string]any, key string) ([32]byte, bool) {
	s, ok := fields[key].(string)
	if !ok {
		return [32]byte{}, false
	}
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return [32]byte{}, false
	}
	var h [32]byte
	copy(h[:], b)
	return h, true
}
