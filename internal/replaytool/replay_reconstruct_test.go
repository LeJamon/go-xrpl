package replaytool

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

const testAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

// testTxHashHex / testLedgerSeq stand in for the transaction hash and ledger
// sequence the reconstruction threads into PreviousTxnID / PreviousTxnLgrSeq.
const testTxHashHex = "AABBCCDDEEFF00112233445566778899AABBCCDDEEFF00112233445566778899"
const testLedgerSeq = uint32(90000000)

func encodeSLE(t *testing.T, m map[string]any) []byte {
	t.Helper()
	h, err := binarycodec.Encode(m)
	if err != nil {
		t.Fatalf("encode %v: %v", m, err)
	}
	b, err := hex.DecodeString(h)
	if err != nil {
		t.Fatalf("decode hex: %v", err)
	}
	return b
}

func encodeMeta(t *testing.T, affected ...map[string]any) []byte {
	t.Helper()
	nodes := make([]any, len(affected))
	for i, a := range affected {
		nodes[i] = a
	}
	return encodeSLE(t, map[string]any{"AffectedNodes": nodes})
}

func mustIndex(t *testing.T, s string) [32]byte {
	t.Helper()
	idx, err := decodeIndex(s)
	if err != nil {
		t.Fatalf("decodeIndex %s: %v", s, err)
	}
	return idx
}

func stateRoot(t *testing.T, entries map[[32]byte][]byte) [32]byte {
	t.Helper()
	m := shamap.New(shamap.TypeState)
	for k, v := range entries {
		if err := m.Put(k, v); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	root, err := m.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	return root
}

func putAll(t *testing.T, entries map[[32]byte][]byte) *shamap.SHAMap {
	t.Helper()
	m := shamap.New(shamap.TypeState)
	for k, v := range entries {
		if err := m.Put(k, v); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	return m
}

// TestReconstructFromMeta_ModifyWithFieldRemoval covers the hardest path: a
// ModifiedNode whose FinalFields is a partial delta (Balance changed) and whose
// PreviousFields names a field removed by the transaction (Domain). The
// reconstruction must overlay the delta onto the pre-object, drop the removed
// field, and re-thread PreviousTxnID/PreviousTxnLgrSeq to this transaction —
// metadata carries neither (sMD_DeleteFinal), so a stale pre-state value must be
// overwritten, byte-for-byte.
func TestReconstructFromMeta_ModifyWithFieldRemoval(t *testing.T) {
	idxHex := "00000000000000000000000000000000000000000000000000000000000000AA"
	idx := mustIndex(t, idxHex)
	staleTxID := "1111111111111111111111111111111111111111111111111111111111111111"

	pre := map[string]any{
		"LedgerEntryType":   "AccountRoot",
		"Account":           testAccount,
		"Balance":           "1000000000",
		"Flags":             0,
		"OwnerCount":        0,
		"Sequence":          1,
		"Domain":            "6578616D706C65",
		"PreviousTxnID":     staleTxID,
		"PreviousTxnLgrSeq": uint32(42),
	}
	post := map[string]any{
		"LedgerEntryType":   "AccountRoot",
		"Account":           testAccount,
		"Balance":           "2000000000",
		"Flags":             0,
		"OwnerCount":        0,
		"Sequence":          1,
		"PreviousTxnID":     testTxHashHex,
		"PreviousTxnLgrSeq": testLedgerSeq,
	}

	preState := putAll(t, map[[32]byte][]byte{idx: encodeSLE(t, pre)})
	wantRoot := stateRoot(t, map[[32]byte][]byte{idx: encodeSLE(t, post)})

	meta := encodeMeta(t, map[string]any{
		"ModifiedNode": map[string]any{
			"LedgerEntryType": "AccountRoot",
			"LedgerIndex":     idxHex,
			"FinalFields":     map[string]any{"Balance": "2000000000"},
			"PreviousFields":  map[string]any{"Balance": "1000000000", "Domain": "6578616D706C65"},
		},
	})

	corrected, err := reconstructFromMeta(preState, []metaTx{{Blob: meta, TxHash: mustIndex(t, testTxHashHex)}}, testLedgerSeq)
	if err != nil {
		t.Fatalf("reconstructFromMeta: %v", err)
	}
	gotRoot, err := corrected.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if gotRoot != wantRoot {
		t.Fatalf("reconstructed root %x != expected %x", gotRoot[:8], wantRoot[:8])
	}
}

// TestReconstructFromMeta_CreateAndDelete covers CreatedNode (NewFields + the
// node-level LedgerEntryType) and DeletedNode, while an untouched object must
// be preserved verbatim.
func TestReconstructFromMeta_CreateAndDelete(t *testing.T) {
	idxKeep := mustIndex(t, "0000000000000000000000000000000000000000000000000000000000000001")
	idxNew := mustIndex(t, "0000000000000000000000000000000000000000000000000000000000000002")
	idxDel := mustIndex(t, "0000000000000000000000000000000000000000000000000000000000000003")

	keep := encodeSLE(t, map[string]any{
		"LedgerEntryType": "AccountRoot", "Account": testAccount,
		"Balance": "10", "Flags": 0, "OwnerCount": 0, "Sequence": 1,
	})
	del := encodeSLE(t, map[string]any{
		"LedgerEntryType": "AccountRoot", "Account": testAccount,
		"Balance": "20", "Flags": 0, "OwnerCount": 0, "Sequence": 2,
	})
	newFields := map[string]any{
		"Account": testAccount, "Balance": "30", "Flags": 0, "OwnerCount": 0, "Sequence": 3,
	}
	// The created AccountRoot is a threaded type, so the reconstruction stamps
	// PreviousTxnID/PreviousTxnLgrSeq even though NewFields omits them.
	created := encodeSLE(t, map[string]any{
		"LedgerEntryType": "AccountRoot", "Account": testAccount,
		"Balance": "30", "Flags": 0, "OwnerCount": 0, "Sequence": 3,
		"PreviousTxnID": testTxHashHex, "PreviousTxnLgrSeq": testLedgerSeq,
	})

	preState := putAll(t, map[[32]byte][]byte{idxKeep: keep, idxDel: del})
	wantRoot := stateRoot(t, map[[32]byte][]byte{idxKeep: keep, idxNew: created})

	meta := encodeMeta(t,
		map[string]any{"CreatedNode": map[string]any{
			"LedgerEntryType": "AccountRoot",
			"LedgerIndex":     hex.EncodeToString(idxNew[:]),
			"NewFields":       newFields,
		}},
		map[string]any{"DeletedNode": map[string]any{
			"LedgerEntryType": "AccountRoot",
			"LedgerIndex":     hex.EncodeToString(idxDel[:]),
			"FinalFields":     map[string]any{"Account": testAccount, "Balance": "20"},
		}},
	)

	corrected, err := reconstructFromMeta(preState, []metaTx{{Blob: meta, TxHash: mustIndex(t, testTxHashHex)}}, testLedgerSeq)
	if err != nil {
		t.Fatalf("reconstructFromMeta: %v", err)
	}
	gotRoot, err := corrected.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if gotRoot != wantRoot {
		t.Fatalf("reconstructed root %x != expected %x", gotRoot[:8], wantRoot[:8])
	}

	if _, found, _ := corrected.Get(idxDel); found {
		t.Fatal("deleted node still present")
	}
	if _, found, _ := corrected.Get(idxNew); !found {
		t.Fatal("created node missing")
	}
}

func TestReconstructFromMeta_EmptyMetaLeavesStateUnchanged(t *testing.T) {
	idx := mustIndex(t, "00000000000000000000000000000000000000000000000000000000000000FF")
	obj := encodeSLE(t, map[string]any{
		"LedgerEntryType": "AccountRoot", "Account": testAccount,
		"Balance": "7", "Flags": 0, "OwnerCount": 0, "Sequence": 1,
	})
	preState := putAll(t, map[[32]byte][]byte{idx: obj})
	preRoot, _ := preState.Hash()

	corrected, err := reconstructFromMeta(preState, []metaTx{{Blob: nil}, {Blob: []byte{}}}, testLedgerSeq)
	if err != nil {
		t.Fatalf("reconstructFromMeta: %v", err)
	}
	gotRoot, _ := corrected.Hash()
	if gotRoot != preRoot {
		t.Fatalf("empty meta changed root: %x != %x", gotRoot[:8], preRoot[:8])
	}
}

func TestDivergingObjects(t *testing.T) {
	idxSame := mustIndex(t, "0000000000000000000000000000000000000000000000000000000000000011")
	idxDiff := mustIndex(t, "0000000000000000000000000000000000000000000000000000000000000012")
	idxOnlyGo := mustIndex(t, "0000000000000000000000000000000000000000000000000000000000000013")

	same := encodeSLE(t, map[string]any{"LedgerEntryType": "AccountRoot", "Account": testAccount, "Balance": "1", "Flags": 0, "OwnerCount": 0, "Sequence": 1})
	goDiff := encodeSLE(t, map[string]any{"LedgerEntryType": "AccountRoot", "Account": testAccount, "Balance": "2", "Flags": 0, "OwnerCount": 0, "Sequence": 1})
	mainDiff := encodeSLE(t, map[string]any{"LedgerEntryType": "AccountRoot", "Account": testAccount, "Balance": "3", "Flags": 0, "OwnerCount": 0, "Sequence": 1})
	goOnly := encodeSLE(t, map[string]any{"LedgerEntryType": "AccountRoot", "Account": testAccount, "Balance": "4", "Flags": 0, "OwnerCount": 0, "Sequence": 1})

	goxrpl := putAll(t, map[[32]byte][]byte{idxSame: same, idxDiff: goDiff, idxOnlyGo: goOnly})
	mainnet := putAll(t, map[[32]byte][]byte{idxSame: same, idxDiff: mainDiff})

	diverging, err := divergingObjects(goxrpl, mainnet)
	if err != nil {
		t.Fatalf("divergingObjects: %v", err)
	}

	byIndex := map[string]divergingObject{}
	for _, d := range diverging {
		byIndex[d.Index] = d
	}
	if _, ok := byIndex[hex.EncodeToString(idxSame[:])]; ok {
		t.Fatal("identical object reported as diverging")
	}

	d, ok := byIndex[hex.EncodeToString(idxDiff[:])]
	if !ok || d.GoXRPL == "" || d.Mainnet == "" || d.GoXRPL == d.Mainnet {
		t.Fatalf("modified object not reported correctly: %+v", d)
	}

	d, ok = byIndex[hex.EncodeToString(idxOnlyGo[:])]
	if !ok || d.GoXRPL == "" || d.Mainnet != "" {
		t.Fatalf("go-only object should have empty mainnet side: %+v", d)
	}
}

// TestReconstructFromMeta_CreatedOfferDefaults is the issue's exact scenario: a
// created Offer whose NewFields omits the soeREQUIRED default-zero fields
// (Flags, BookNode, OwnerNode) and the threaded PreviousTxn pair. The
// reconstruction must restore all of them so the SLE matches mainnet byte-for-byte.
func TestReconstructFromMeta_CreatedOfferDefaults(t *testing.T) {
	idxHex := "00000000000000000000000000000000000000000000000000000000000000C0"
	idx := mustIndex(t, idxHex)
	bookDir := "0000000000000000000000000000000000000000000000000000000000000ABC"

	full := map[string]any{
		"LedgerEntryType":   "Offer",
		"Account":           testAccount,
		"Sequence":          5,
		"TakerPays":         "1000000",
		"TakerGets":         map[string]any{"value": "10", "currency": "USD", "issuer": testAccount},
		"BookDirectory":     bookDir,
		"Flags":             0,
		"BookNode":          "0",
		"OwnerNode":         "0",
		"PreviousTxnID":     testTxHashHex,
		"PreviousTxnLgrSeq": testLedgerSeq,
	}
	wantRoot := stateRoot(t, map[[32]byte][]byte{idx: encodeSLE(t, full)})

	newFields := map[string]any{
		"Account":       testAccount,
		"Sequence":      5,
		"TakerPays":     "1000000",
		"TakerGets":     map[string]any{"value": "10", "currency": "USD", "issuer": testAccount},
		"BookDirectory": bookDir,
	}
	meta := encodeMeta(t, map[string]any{"CreatedNode": map[string]any{
		"LedgerEntryType": "Offer",
		"LedgerIndex":     idxHex,
		"NewFields":       newFields,
	}})

	corrected, err := reconstructFromMeta(putAll(t, nil), []metaTx{{Blob: meta, TxHash: mustIndex(t, testTxHashHex)}}, testLedgerSeq)
	if err != nil {
		t.Fatalf("reconstructFromMeta: %v", err)
	}
	gotRoot, err := corrected.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if gotRoot != wantRoot {
		t.Fatalf("reconstructed offer root %x != expected %x", gotRoot[:8], wantRoot[:8])
	}
}

// TestReconstructFromMeta_DirectoryIndexes covers the directory-page path, whose
// sfIndexes is sMD_Never and so absent from metadata: an owner directory (kept
// sorted) gains a created Ticket, and an order-book directory (insertion-ordered)
// gains a created Offer at its tail. Both pages must be rebuilt byte-for-byte
// from the membership changes, including the threaded PreviousTxn pair.
func TestReconstructFromMeta_DirectoryIndexes(t *testing.T) {
	ownerID, err := state.DecodeAccountID(testAccount)
	if err != nil {
		t.Fatalf("DecodeAccountID: %v", err)
	}
	ownerPage := keylet.OwnerDirPage(ownerID, 0).Key
	ownerPageHex := hex.EncodeToString(ownerPage[:])
	ownerRootHex := strings.ToUpper(hex.EncodeToString(ownerPage[:]))

	keyB := "00000000000000000000000000000000000000000000000000000000000000BB"
	keyD := "00000000000000000000000000000000000000000000000000000000000000DD"

	bookRootHex := "00000000000000000000000000000000000000000000000000000000B0000000"
	bookRoot := mustIndex(t, bookRootHex)
	offer1 := "0000000000000000000000000000000000000000000000000000000000000022"
	offer2 := "0000000000000000000000000000000000000000000000000000000000000011"

	// Pre-state: the two directory pages with their prior contents.
	ownerDirPre := encodeSLE(t, map[string]any{
		"LedgerEntryType": "DirectoryNode",
		"Flags":           0,
		"RootIndex":       ownerRootHex,
		"Owner":           testAccount,
		"Indexes":         []string{keyD},
	})
	bookDirPre := encodeSLE(t, map[string]any{
		"LedgerEntryType": "DirectoryNode",
		"Flags":           0,
		"RootIndex":       bookRootHex,
		"Indexes":         []string{offer1},
	})
	preState := putAll(t, map[[32]byte][]byte{ownerPage: ownerDirPre, bookRoot: bookDirPre})

	meta := encodeMeta(t,
		map[string]any{"ModifiedNode": map[string]any{
			"LedgerEntryType": "DirectoryNode",
			"LedgerIndex":     ownerPageHex,
			"FinalFields":     map[string]any{"Flags": 0, "RootIndex": ownerRootHex, "Owner": testAccount},
		}},
		map[string]any{"ModifiedNode": map[string]any{
			"LedgerEntryType": "DirectoryNode",
			"LedgerIndex":     bookRootHex,
			"FinalFields":     map[string]any{"Flags": 0, "RootIndex": bookRootHex},
		}},
		map[string]any{"CreatedNode": map[string]any{
			"LedgerEntryType": "Ticket",
			"LedgerIndex":     keyB,
			"NewFields":       map[string]any{"Account": testAccount, "OwnerNode": "0", "TicketSequence": 7},
		}},
		map[string]any{"CreatedNode": map[string]any{
			"LedgerEntryType": "Offer",
			"LedgerIndex":     offer2,
			"NewFields": map[string]any{
				"Account":       testAccount,
				"Sequence":      9,
				"TakerPays":     "1000000",
				"TakerGets":     map[string]any{"value": "10", "currency": "USD", "issuer": testAccount},
				"BookDirectory": bookRootHex,
				"BookNode":      "0",
				"OwnerNode":     "3e7", // page 999 of the owner dir: absent here, so untouched
			},
		}},
	)

	corrected, err := reconstructFromMeta(preState, []metaTx{{Blob: meta, TxHash: mustIndex(t, testTxHashHex)}}, testLedgerSeq)
	if err != nil {
		t.Fatalf("reconstructFromMeta: %v", err)
	}

	// Owner directory: sorted insert places keyB before keyD.
	wantOwner := encodeSLE(t, map[string]any{
		"LedgerEntryType":   "DirectoryNode",
		"Flags":             0,
		"RootIndex":         ownerRootHex,
		"Owner":             testAccount,
		"Indexes":           []string{keyB, keyD},
		"PreviousTxnID":     testTxHashHex,
		"PreviousTxnLgrSeq": testLedgerSeq,
	})
	assertEntryBytes(t, corrected, ownerPage, wantOwner, "owner directory")

	// Order book: append keeps insertion order (offer1 then offer2), not sorted.
	wantBook := encodeSLE(t, map[string]any{
		"LedgerEntryType":   "DirectoryNode",
		"Flags":             0,
		"RootIndex":         bookRootHex,
		"Indexes":           []string{offer1, offer2},
		"PreviousTxnID":     testTxHashHex,
		"PreviousTxnLgrSeq": testLedgerSeq,
	})
	assertEntryBytes(t, corrected, bookRoot, wantBook, "order book directory")
}

// TestReconstructFromMeta_EscrowIssuerDir covers the issuer owner-directory an
// IOU escrow is listed in (beyond the owner's and destination's): rippled adds a
// cross-issuer IOU escrow to the issuer's directory to track the locked balance,
// recording IssuerNode. The reconstruction must add the escrow key to that page,
// or the issuer page's sfIndexes diverges from mainnet.
func TestReconstructFromMeta_EscrowIssuerDir(t *testing.T) {
	const destination = "rrrrrrrrrrrrrrrrrrrrBZbvji"
	const issuer = "rrrrrrrrrrrrrrrrrrrrrhoLvTp"

	issuerID, err := state.DecodeAccountID(issuer)
	if err != nil {
		t.Fatalf("DecodeAccountID: %v", err)
	}
	issuerPage := keylet.OwnerDirPage(issuerID, 0).Key
	issuerPageHex := hex.EncodeToString(issuerPage[:])
	issuerRootHex := strings.ToUpper(hex.EncodeToString(issuerPage[:]))

	escrowKey := "00000000000000000000000000000000000000000000000000000000000000E5"
	existingKey := "00000000000000000000000000000000000000000000000000000000000000F0"

	// Pre-state: the issuer's directory page already lists one object.
	issuerDirPre := encodeSLE(t, map[string]any{
		"LedgerEntryType": "DirectoryNode",
		"Flags":           0,
		"RootIndex":       issuerRootHex,
		"Owner":           issuer,
		"Indexes":         []string{existingKey},
	})
	preState := putAll(t, map[[32]byte][]byte{issuerPage: issuerDirPre})

	meta := encodeMeta(t,
		map[string]any{"ModifiedNode": map[string]any{
			"LedgerEntryType": "DirectoryNode",
			"LedgerIndex":     issuerPageHex,
			"FinalFields":     map[string]any{"Flags": 0, "RootIndex": issuerRootHex, "Owner": issuer},
		}},
		map[string]any{"CreatedNode": map[string]any{
			"LedgerEntryType": "Escrow",
			"LedgerIndex":     escrowKey,
			"NewFields": map[string]any{
				"Account":         testAccount,
				"Destination":     destination,
				"Amount":          map[string]any{"value": "10", "currency": "USD", "issuer": issuer},
				"OwnerNode":       "0",
				"DestinationNode": "0",
				"IssuerNode":      "0",
			},
		}},
	)

	corrected, err := reconstructFromMeta(preState, []metaTx{{Blob: meta, TxHash: mustIndex(t, testTxHashHex)}}, testLedgerSeq)
	if err != nil {
		t.Fatalf("reconstructFromMeta: %v", err)
	}

	// Sorted insert: escrowKey (E5) sorts before the existing key (F0).
	wantIssuerDir := encodeSLE(t, map[string]any{
		"LedgerEntryType":   "DirectoryNode",
		"Flags":             0,
		"RootIndex":         issuerRootHex,
		"Owner":             issuer,
		"Indexes":           []string{escrowKey, existingKey},
		"PreviousTxnID":     testTxHashHex,
		"PreviousTxnLgrSeq": testLedgerSeq,
	})
	assertEntryBytes(t, corrected, issuerPage, wantIssuerDir, "issuer directory")
}

// TestReconstructFromMeta_CreatedBookDirectoryXRPSide is the issue's exact
// scenario: a created order-book DirectoryNode whose taker pays in XRP, so its
// TakerPaysCurrency/TakerPaysIssuer pair is all-zero. rippled drops that pair
// from NewFields (a zero Hash160 reports isDefault() true), but the real SLE
// always serializes both sides. The reconstruction must restore the zero pair so
// the directory matches mainnet byte-for-byte and account_hash verifies.
func TestReconstructFromMeta_CreatedBookDirectoryXRPSide(t *testing.T) {
	bookRootHex := "00000000000000000000000000000000000000000000000000000000B0000001"
	bookRoot := mustIndex(t, bookRootHex)
	bookRootUpper := strings.ToUpper(bookRootHex)
	offerKey := "00000000000000000000000000000000000000000000000000000000000000A1"

	const exchangeRate = "5006519F6CF22000"
	const getsCurrency = "0000000000000000000000005553440000000000" // USD
	const getsIssuer = "0123456789ABCDEF0123456789ABCDEF01234567"

	// The real book root SLE: both sides present, the XRP (pays) side all-zero,
	// Indexes listing the offer, and the threaded PreviousTxn pair.
	wantBook := encodeSLE(t, map[string]any{
		"LedgerEntryType":   "DirectoryNode",
		"Flags":             0,
		"RootIndex":         bookRootUpper,
		"ExchangeRate":      exchangeRate,
		"TakerPaysCurrency": zeroHash160,
		"TakerPaysIssuer":   zeroHash160,
		"TakerGetsCurrency": getsCurrency,
		"TakerGetsIssuer":   getsIssuer,
		"Indexes":           []string{offerKey},
		"PreviousTxnID":     testTxHashHex,
		"PreviousTxnLgrSeq": testLedgerSeq,
	})

	// Metadata: the book directory and the offer are both created this ledger.
	// NewFields carries only the non-zero (gets) side and ExchangeRate; the XRP
	// (pays) side and Flags are dropped, and sfIndexes is sMD_Never so absent.
	meta := encodeMeta(t,
		map[string]any{"CreatedNode": map[string]any{
			"LedgerEntryType": "DirectoryNode",
			"LedgerIndex":     bookRootHex,
			"NewFields": map[string]any{
				"RootIndex":         bookRootUpper,
				"ExchangeRate":      exchangeRate,
				"TakerGetsCurrency": getsCurrency,
				"TakerGetsIssuer":   getsIssuer,
			},
		}},
		map[string]any{"CreatedNode": map[string]any{
			"LedgerEntryType": "Offer",
			"LedgerIndex":     offerKey,
			"NewFields": map[string]any{
				"Account":       testAccount,
				"Sequence":      9,
				"TakerPays":     "1000000",
				"TakerGets":     map[string]any{"value": "10", "currency": "USD", "issuer": testAccount},
				"BookDirectory": bookRootHex,
				"BookNode":      "0",
				"OwnerNode":     "0",
			},
		}},
	)

	corrected, err := reconstructFromMeta(putAll(t, nil), []metaTx{{Blob: meta, TxHash: mustIndex(t, testTxHashHex)}}, testLedgerSeq)
	if err != nil {
		t.Fatalf("reconstructFromMeta: %v", err)
	}
	assertEntryBytes(t, corrected, bookRoot, wantBook, "book directory")
}

func TestReconstructFromMeta_VaultCreateDefaultsAndDirectories(t *testing.T) {
	const owner = "rEaWzpDUL2cBckwDJhRENZiKCbNKwG2cAZ"
	const pseudo = "rpRrVjCLggyjBaAYreukcyuWuzb23wuWrn"
	const vaultKeyHex = "DE275CBF520001E340CA0C7D8FD15D5D5CEDAC6559BDF377882BD4AF8712C622"
	const issuanceKeyHex = "39B9656467D5B6F0AE0DD9A96BE0E27F9CAEEF6DBEBA8F7C7ECAC237EE94D8B6"
	const shareMPTID = "000000010F8285BE96FB1972BC582434283C22113532FAB5"

	ownerID, err := state.DecodeAccountID(owner)
	if err != nil {
		t.Fatalf("decode owner: %v", err)
	}
	pseudoID, err := state.DecodeAccountID(pseudo)
	if err != nil {
		t.Fatalf("decode pseudo: %v", err)
	}
	ownerDir := keylet.OwnerDirPage(ownerID, 0).Key
	pseudoDir := keylet.OwnerDirPage(pseudoID, 0).Key
	ownerDirHex := strings.ToUpper(hex.EncodeToString(ownerDir[:]))
	pseudoDirHex := strings.ToUpper(hex.EncodeToString(pseudoDir[:]))
	vaultKey := mustIndex(t, vaultKeyHex)
	issuanceKey := mustIndex(t, issuanceKeyHex)

	meta := encodeMeta(t,
		map[string]any{"CreatedNode": map[string]any{
			"LedgerEntryType": "DirectoryNode",
			"LedgerIndex":     ownerDirHex,
			"NewFields":       map[string]any{"Owner": owner, "RootIndex": ownerDirHex},
		}},
		map[string]any{"CreatedNode": map[string]any{
			"LedgerEntryType": "DirectoryNode",
			"LedgerIndex":     pseudoDirHex,
			"NewFields":       map[string]any{"Owner": pseudo, "RootIndex": pseudoDirHex},
		}},
		map[string]any{"CreatedNode": map[string]any{
			"LedgerEntryType": "Vault",
			"LedgerIndex":     vaultKeyHex,
			"NewFields": map[string]any{
				"Account":          pseudo,
				"Data":             "4D65746144617461",
				"Owner":            owner,
				"Sequence":         uint32(3024998),
				"ShareMPTID":       shareMPTID,
				"WithdrawalPolicy": 1,
			},
		}},
		map[string]any{"CreatedNode": map[string]any{
			"LedgerEntryType": "AccountRoot",
			"LedgerIndex":     "9EDC99BAA48E5FC2A28C355D8CA9A723CD52CC36DC76E946D118D3DB679B8DB5",
			"NewFields": map[string]any{
				"Account":    pseudo,
				"Flags":      uint32(26214400),
				"OwnerCount": uint32(1),
				"VaultID":    vaultKeyHex,
			},
		}},
		map[string]any{"CreatedNode": map[string]any{
			"LedgerEntryType": "MPTokenIssuance",
			"LedgerIndex":     issuanceKeyHex,
			"NewFields": map[string]any{
				"Issuer":   pseudo,
				"Sequence": uint32(1),
			},
		}},
	)

	corrected, err := reconstructFromMeta(putAll(t, nil), []metaTx{{Blob: meta, TxHash: mustIndex(t, testTxHashHex)}}, testLedgerSeq)
	if err != nil {
		t.Fatalf("reconstructFromMeta: %v", err)
	}

	wantOwnerDir := encodeSLE(t, map[string]any{
		"LedgerEntryType": "DirectoryNode", "Flags": 0, "Owner": owner,
		"RootIndex": ownerDirHex, "Indexes": []string{vaultKeyHex},
		"PreviousTxnID": testTxHashHex, "PreviousTxnLgrSeq": testLedgerSeq,
	})
	wantPseudoDir := encodeSLE(t, map[string]any{
		"LedgerEntryType": "DirectoryNode", "Flags": 0, "Owner": pseudo,
		"RootIndex": pseudoDirHex, "Indexes": []string{issuanceKeyHex},
		"PreviousTxnID": testTxHashHex, "PreviousTxnLgrSeq": testLedgerSeq,
	})
	wantVault := encodeSLE(t, map[string]any{
		"LedgerEntryType": "Vault", "Flags": 0, "OwnerNode": "0",
		"Owner": owner, "Account": pseudo, "Sequence": uint32(3024998),
		"Data": "4D65746144617461", "Asset": map[string]any{"currency": "XRP"},
		"ShareMPTID": shareMPTID, "WithdrawalPolicy": 1,
		"PreviousTxnID": testTxHashHex, "PreviousTxnLgrSeq": testLedgerSeq,
	})
	wantPseudo := encodeSLE(t, map[string]any{
		"LedgerEntryType": "AccountRoot", "Account": pseudo, "Balance": "0",
		"Flags": uint32(26214400), "OwnerCount": uint32(1), "Sequence": uint32(0),
		"VaultID": vaultKeyHex, "PreviousTxnID": testTxHashHex,
		"PreviousTxnLgrSeq": testLedgerSeq,
	})
	wantIssuance := encodeSLE(t, map[string]any{
		"LedgerEntryType": "MPTokenIssuance", "Flags": 0, "Issuer": pseudo,
		"Sequence": uint32(1), "OwnerNode": "0", "OutstandingAmount": "0",
		"PreviousTxnID": testTxHashHex, "PreviousTxnLgrSeq": testLedgerSeq,
	})

	assertEntryBytes(t, corrected, ownerDir, wantOwnerDir, "vault owner directory")
	assertEntryBytes(t, corrected, pseudoDir, wantPseudoDir, "pseudo owner directory")
	assertEntryBytes(t, corrected, vaultKey, wantVault, "vault")
	assertEntryBytes(t, corrected, mustIndex(t, "9EDC99BAA48E5FC2A28C355D8CA9A723CD52CC36DC76E946D118D3DB679B8DB5"), wantPseudo, "pseudo account")
	assertEntryBytes(t, corrected, issuanceKey, wantIssuance, "share issuance")
}

func assertEntryBytes(t *testing.T, m *shamap.SHAMap, key [32]byte, want []byte, label string) {
	t.Helper()
	item, found, err := m.Get(key)
	if err != nil || !found || item == nil {
		t.Fatalf("%s: entry missing (found=%v err=%v)", label, found, err)
	}
	if !bytes.Equal(item.Data(), want) {
		t.Fatalf("%s bytes mismatch:\n got %X\nwant %X", label, item.Data(), want)
	}
}
