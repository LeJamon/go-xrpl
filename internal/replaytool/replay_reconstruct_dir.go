package replaytool

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	ledgerentry "github.com/LeJamon/go-xrpl/ledger/entry"
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

// dirDelta accumulates the membership changes a ledger makes to one directory
// page. adds is in operation order (needed for append-ordered offer books);
// removes is a set.
type dirDelta struct {
	strategy dirStrategy
	adds     [][32]byte
	removes  map[[32]byte]bool
}

// recordMembership attributes an object's creation (isAdd) or deletion to every
// directory page it belongs to, accumulating the per-page deltas.
func recordMembership(deltas map[[32]byte]*dirDelta, objKey [32]byte, entryType string, fields map[string]any, isAdd bool) {
	for _, p := range directoryPlacements(entryType, fields) {
		d := deltas[p.Key]
		if d == nil {
			d = &dirDelta{strategy: p.Strategy, removes: map[[32]byte]bool{}}
			deltas[p.Key] = d
		}
		if isAdd {
			d.adds = append(d.adds, objKey)
		} else {
			d.removes[objKey] = true
		}
	}
}

// directoryPlacements returns the directory pages an object of entryType with
// the given fields is listed in. The fields come from a CreatedNode's (default-
// filled) NewFields or a DeletedNode's FinalFields, both of which carry the
// node-pointer and owner fields needed to locate each page.
func directoryPlacements(entryType string, fields map[string]any) []dirPlacement {
	var out []dirPlacement
	add := func(k keylet.Keylet, s dirStrategy) { out = append(out, dirPlacement{Key: k.Key, Strategy: s}) }

	switch entryType {
	case "DirectoryNode", "Amendments", "FeeSettings", "NegativeUNL", "LedgerHashes", "AccountRoot":
		// Directory pages and singletons are not themselves listed in a directory.
		return nil

	case "RippleState":
		// A trust line is listed in both endpoints' owner directories.
		if lo, ok := metaIssuer(fields, "LowLimit"); ok {
			add(keylet.OwnerDirPage(lo, metaUint64(fields["LowNode"])), dirSorted)
		}
		if hi, ok := metaIssuer(fields, "HighLimit"); ok {
			add(keylet.OwnerDirPage(hi, metaUint64(fields["HighNode"])), dirSorted)
		}
		return out

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
		return out
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

	return out
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
		members := applyDirDelta(decodeIndexes(obj["Indexes"]), d)
		obj["Indexes"] = encodeIndexes(members)
		if err := putEncoded(state, pageKey, obj); err != nil {
			return fmt.Errorf("re-encoding directory page %x: %w", pageKey[:4], err)
		}
	}
	return nil
}

// applyDirDelta applies one page's membership delta to its prior members,
// reproducing rippled's ordering: removals preserve relative order; additions
// append in operation order, then the whole page is sorted when the directory is
// sorted (dirInsert) rather than append-ordered (dirAppend). A key both added
// and removed in the same ledger ends up absent.
func applyDirDelta(members [][32]byte, d *dirDelta) [][32]byte {
	if len(d.removes) > 0 {
		kept := make([][32]byte, 0, len(members))
		for _, k := range members {
			if !d.removes[k] {
				kept = append(kept, k)
			}
		}
		members = kept
	}

	present := make(map[[32]byte]bool, len(members))
	for _, k := range members {
		present[k] = true
	}
	for _, k := range d.adds {
		if present[k] || d.removes[k] {
			continue
		}
		members = append(members, k)
		present[k] = true
	}

	if d.strategy == dirSorted {
		sort.Slice(members, func(i, j int) bool {
			return bytes.Compare(members[i][:], members[j][:]) < 0
		})
	}
	return members
}

// decodeIndexes parses a directory page's sfIndexes value into 32-byte keys.
func decodeIndexes(v any) [][32]byte {
	var out [][32]byte
	appendHex := func(s string) {
		b, err := hex.DecodeString(s)
		if err == nil && len(b) == 32 {
			var k [32]byte
			copy(k[:], b)
			out = append(out, k)
		}
	}
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok {
				appendHex(s)
			}
		}
	case []string:
		for _, s := range t {
			appendHex(s)
		}
	}
	return out
}

// encodeIndexes renders 32-byte keys as the uppercase-hex string array the
// binary codec expects for sfIndexes.
func encodeIndexes(members [][32]byte) []string {
	out := make([]string, len(members))
	for i, k := range members {
		out[i] = strings.ToUpper(hex.EncodeToString(k[:]))
	}
	return out
}

// metaUint64 reads a UInt64 (hex string) or UInt32 (numeric) metadata field as a
// uint64, returning 0 when the field is absent or unparseable. Node-pointer
// fields are sMD_Default (always present on created/deleted nodes), so an absent
// field attributing to page 0 only arises on malformed input, which the final
// account_hash check catches.
func metaUint64(v any) uint64 {
	switch t := v.(type) {
	case string:
		n, _ := strconv.ParseUint(t, 16, 64)
		return n
	case float64:
		return uint64(t)
	case uint32:
		return uint64(t)
	case uint64:
		return t
	case int:
		return uint64(t)
	case int64:
		return uint64(t)
	}
	return 0
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
