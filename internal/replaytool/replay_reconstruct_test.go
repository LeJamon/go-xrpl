package replaytool

import (
	"bytes"
	"encoding/hex"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
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
	idx, err := protocol.Hash256FromHex(s)
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

func TestReconstructFromMeta_CreatedLoanBrokerDefaults(t *testing.T) {
	const (
		ledgerIndexHex = "75F04A09A3F45F989E015B92A39F8B70B99857D31D5D61955AEB16190B7E7341"
		txHashHex      = "B40964DF5BC8EA1DF5EDC1EDC2F44463CDBCD45B3D273D68D7AC72A397F4E7B9"
		canonicalHex   = "11008822000000002400325E0E2500325E1C203D00000001340000000000000000301E000000000000000055B40964DF5BC8EA1DF5EDC1EDC2F44463CDBCD45B3D273D68D7AC72A397F4E7B9502315032ADB6CACAE93D1DA6C73FBF10E67071133303154EA69F2956A0BC4137D858114B87371891D6215AB177F93F8A84DC5CCBB48C0788214E6D10E126C40BA4428FEE64BC2C4F3F2C31AB9B8"
		vaultAccount   = "rpRrVjCLggyjBaAYreukcyuWuzb23wuWrn"
		ledgerSeq      = uint32(3_300_892)
	)

	full, err := binarycodec.Decode(canonicalHex)
	if err != nil {
		t.Fatalf("Decode canonical LoanBroker: %v", err)
	}
	owner, ok := full["Owner"].(string)
	if !ok {
		t.Fatalf("canonical LoanBroker Owner = %T, want string", full["Owner"])
	}
	vaultIDHex, ok := full["VaultID"].(string)
	if !ok {
		t.Fatalf("canonical LoanBroker VaultID = %T, want string", full["VaultID"])
	}
	ownerID, err := state.DecodeAccountID(owner)
	if err != nil {
		t.Fatalf("Decode owner: %v", err)
	}
	vaultAccountID, err := state.DecodeAccountID(vaultAccount)
	if err != nil {
		t.Fatalf("Decode vault account: %v", err)
	}
	ledgerIndex := mustIndex(t, ledgerIndexHex)
	vaultID := mustIndex(t, vaultIDHex)
	ownerDir := keylet.OwnerDirPage(ownerID, 0).Key
	vaultDir := keylet.OwnerDirPage(vaultAccountID, 0).Key

	newFields := maps.Clone(full)
	for _, field := range []string{
		"LedgerEntryType",
		"Flags",
		"OwnerNode",
		"VaultNode",
		"PreviousTxnID",
		"PreviousTxnLgrSeq",
	} {
		delete(newFields, field)
	}
	meta := encodeMeta(t,
		map[string]any{"ModifiedNode": map[string]any{
			"LedgerEntryType": "DirectoryNode",
			"LedgerIndex":     protocol.Hash256Hex(ownerDir),
			"FinalFields": map[string]any{
				"Flags": 0, "Owner": owner, "RootIndex": protocol.Hash256Hex(ownerDir),
			},
		}},
		map[string]any{"ModifiedNode": map[string]any{
			"LedgerEntryType": "DirectoryNode",
			"LedgerIndex":     protocol.Hash256Hex(vaultDir),
			"FinalFields": map[string]any{
				"Flags": 0, "Owner": vaultAccount, "RootIndex": protocol.Hash256Hex(vaultDir),
			},
		}},
		map[string]any{"CreatedNode": map[string]any{
			"LedgerEntryType": "LoanBroker",
			"LedgerIndex":     ledgerIndexHex,
			"NewFields":       newFields,
		}},
	)

	corrected, err := reconstructFromMeta(
		putAll(t, map[[32]byte][]byte{
			keylet.VaultByID(vaultID).Key: vaultSLE(t, vaultAccount, owner),
			ownerDir: encodeSLE(t, map[string]any{
				"LedgerEntryType": "DirectoryNode", "Flags": 0,
				"Owner": owner, "RootIndex": protocol.Hash256Hex(ownerDir),
			}),
			vaultDir: encodeSLE(t, map[string]any{
				"LedgerEntryType": "DirectoryNode", "Flags": 0,
				"Owner": vaultAccount, "RootIndex": protocol.Hash256Hex(vaultDir),
			}),
		}),
		[]metaTx{{Blob: meta, TxHash: mustIndex(t, txHashHex)}},
		ledgerSeq,
	)
	if err != nil {
		t.Fatalf("reconstructFromMeta: %v", err)
	}
	item, found, err := corrected.Get(ledgerIndex)
	if err != nil {
		t.Fatalf("Get LoanBroker: %v", err)
	}
	if !found {
		t.Fatal("reconstructed LoanBroker is missing")
	}
	if got := strings.ToUpper(hex.EncodeToString(item.Data())); got != canonicalHex {
		t.Fatalf("reconstructed LoanBroker\n got: %s\nwant: %s", got, canonicalHex)
	}
	assertDirectoryMembers(t, corrected, ownerDir, ledgerIndex)
	assertDirectoryMembers(t, corrected, vaultDir, ledgerIndex)
}

func TestApplyAffectedNode_AMMCreateDefaults(t *testing.T) {
	const (
		ammIndex   = "00000000000000000000000000000000000000000000000000000000000000A0"
		line1Index = "00000000000000000000000000000000000000000000000000000000000000A1"
		line2Index = "00000000000000000000000000000000000000000000000000000000000000A2"
	)

	stateMap := putAll(t, nil)
	deltas := map[[32]byte]*dirDelta{}
	deletedDirs := map[[32]byte]bool{}
	txHash := mustIndex(t, testTxHashHex)

	asset2 := map[string]any{
		"currency": "USD",
		"issuer":   testAccount,
	}
	lpTokens := map[string]any{
		"value":    "1000",
		"currency": "039C99CD9AB0B70B32ECDA51EAAE471625608EA2",
		"issuer":   testAccount,
	}
	ammNode := map[string]any{"CreatedNode": map[string]any{
		"LedgerEntryType": "AMM",
		"LedgerIndex":     ammIndex,
		"NewFields": map[string]any{
			"Account":        testAccount,
			"LPTokenBalance": lpTokens,
			"Asset2":         asset2,
		},
	}}

	lineFields := func(currency string) map[string]any {
		return map[string]any{
			"Flags": uint32(0x00110000),
			"Balance": map[string]any{
				"value":    "10",
				"currency": currency,
				"issuer":   state.AccountOneAddress,
			},
			"LowLimit": map[string]any{
				"value":    "0",
				"currency": currency,
				"issuer":   testAccount,
			},
			"HighLimit": map[string]any{
				"value":    "0",
				"currency": currency,
				"issuer":   state.AccountOneAddress,
			},
		}
	}
	line1Fields := lineFields("USD")
	line2Fields := lineFields("EUR")
	line1Node := map[string]any{"CreatedNode": map[string]any{
		"LedgerEntryType": "RippleState",
		"LedgerIndex":     line1Index,
		"NewFields":       line1Fields,
	}}
	line2Node := map[string]any{"CreatedNode": map[string]any{
		"LedgerEntryType": "RippleState",
		"LedgerIndex":     line2Index,
		"NewFields":       line2Fields,
	}}

	for _, node := range []map[string]any{ammNode, line1Node, line2Node} {
		if err := applyAffectedNode(stateMap, node, txHash, testLedgerSeq, deltas, deletedDirs, nil); err != nil {
			t.Fatalf("applyAffectedNode: %v", err)
		}
	}

	wantAMM := map[string]any{
		"LedgerEntryType":   "AMM",
		"Account":           testAccount,
		"Flags":             0,
		"LPTokenBalance":    lpTokens,
		"Asset":             map[string]any{"currency": "XRP"},
		"Asset2":            asset2,
		"OwnerNode":         "0",
		"PreviousTxnID":     testTxHashHex,
		"PreviousTxnLgrSeq": testLedgerSeq,
	}
	assertEntryBytes(t, stateMap, mustIndex(t, ammIndex), encodeSLE(t, wantAMM), "AMM")

	for _, line := range []struct {
		index  string
		fields map[string]any
	}{
		{line1Index, line1Fields},
		{line2Index, line2Fields},
	} {
		want := copyFields(line.fields)
		want["LedgerEntryType"] = "RippleState"
		want["LowNode"] = "0"
		want["HighNode"] = "0"
		want["PreviousTxnID"] = testTxHashHex
		want["PreviousTxnLgrSeq"] = testLedgerSeq
		assertEntryBytes(t, stateMap, mustIndex(t, line.index), encodeSLE(t, want), "RippleState")
	}
}

func TestApplyAffectedNode_AMMAsset2Default(t *testing.T) {
	const index = "00000000000000000000000000000000000000000000000000000000000000AB"
	asset := map[string]any{
		"mpt_issuance_id": "BAADF00DBAADF00DBAADF00DBAADF00DBAADF00DBAADF00D",
	}
	lpTokens := map[string]any{
		"value":    "1000",
		"currency": "039C99CD9AB0B70B32ECDA51EAAE471625608EA2",
		"issuer":   testAccount,
	}
	node := map[string]any{"CreatedNode": map[string]any{
		"LedgerEntryType": "AMM",
		"LedgerIndex":     index,
		"NewFields": map[string]any{
			"Account":        testAccount,
			"LPTokenBalance": lpTokens,
			"Asset":          asset,
		},
	}}
	stateMap := putAll(t, nil)

	err := applyAffectedNode(
		stateMap,
		node,
		mustIndex(t, testTxHashHex),
		testLedgerSeq,
		map[[32]byte]*dirDelta{},
		map[[32]byte]bool{},
		nil,
	)
	if err != nil {
		t.Fatalf("applyAffectedNode: %v", err)
	}

	want := map[string]any{
		"LedgerEntryType":   "AMM",
		"Account":           testAccount,
		"Flags":             0,
		"LPTokenBalance":    lpTokens,
		"Asset":             asset,
		"Asset2":            map[string]any{"currency": "XRP"},
		"OwnerNode":         "0",
		"PreviousTxnID":     testTxHashHex,
		"PreviousTxnLgrSeq": testLedgerSeq,
	}
	assertEntryBytes(t, stateMap, mustIndex(t, index), encodeSLE(t, want), "AMM")
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

func TestFillCreatedDefaults_EscrowDirectoryNodes(t *testing.T) {
	const destination = "rrrrrrrrrrrrrrrrrrrrBZbvji"
	const issuer = "rrrrrrrrrrrrrrrrrrrrrhoLvTp"

	tests := []struct {
		name       string
		fields     map[string]any
		wantDest   any
		wantIssuer any
	}{
		{
			name: "cross-account page zero",
			fields: map[string]any{
				"Account": testAccount, "Destination": destination, "Amount": "10000",
			},
			wantDest: "0",
		},
		{
			name: "self escrow",
			fields: map[string]any{
				"Account": testAccount, "Destination": testAccount, "Amount": "10000",
			},
		},
		{
			name: "self IOU escrow with third-party issuer",
			fields: map[string]any{
				"Account": testAccount, "Destination": testAccount,
				"Amount": map[string]any{"value": "10", "currency": "USD", "issuer": issuer},
			},
			wantIssuer: "0",
		},
		{
			name: "explicit nonzero directory pages",
			fields: map[string]any{
				"Account": testAccount, "Destination": destination,
				"Amount":          map[string]any{"value": "10", "currency": "USD", "issuer": issuer},
				"DestinationNode": "2", "IssuerNode": "3",
			},
			wantDest: "2", wantIssuer: "3",
		},
		{
			name: "third-party IOU issuer page zero",
			fields: map[string]any{
				"Account": testAccount, "Destination": destination,
				"Amount": map[string]any{"value": "10", "currency": "USD", "issuer": issuer},
			},
			wantDest: "0", wantIssuer: "0",
		},
		{
			name: "destination is IOU issuer",
			fields: map[string]any{
				"Account": testAccount, "Destination": destination,
				"Amount": map[string]any{"value": "10", "currency": "USD", "issuer": destination},
			},
			wantDest: "0",
		},
		{
			name: "MPT has no issuer directory",
			fields: map[string]any{
				"Account": testAccount, "Destination": destination,
				"Amount": map[string]any{"value": "10", "mpt_issuance_id": "000000000000000000000000000000000000000000000001"},
			},
			wantDest: "0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fillCreatedDefaults(tt.fields, "Escrow")
			if got := tt.fields["DestinationNode"]; got != tt.wantDest {
				t.Fatalf("DestinationNode = %v, want %v", got, tt.wantDest)
			}
			if got := tt.fields["IssuerNode"]; got != tt.wantIssuer {
				t.Fatalf("IssuerNode = %v, want %v", got, tt.wantIssuer)
			}
		})
	}
}

func TestReconstructFromMeta_EscrowDestinationDirPageZero(t *testing.T) {
	const destination = "rrrrrrrrrrrrrrrrrrrrBZbvji"
	const escrowKeyHex = "07D546EC9389810CEEC2D5C843171BE9F07EAEA05039E5466BF4AC5B19AEAE7C"

	destinationID, err := state.DecodeAccountID(destination)
	if err != nil {
		t.Fatalf("DecodeAccountID: %v", err)
	}
	destinationPage := keylet.OwnerDirPage(destinationID, 0).Key
	destinationPageHex := strings.ToUpper(hex.EncodeToString(destinationPage[:]))
	escrowKey := mustIndex(t, escrowKeyHex)

	meta := encodeMeta(t,
		map[string]any{"CreatedNode": map[string]any{
			"LedgerEntryType": "DirectoryNode",
			"LedgerIndex":     destinationPageHex,
			"NewFields":       map[string]any{"Owner": destination, "RootIndex": destinationPageHex},
		}},
		map[string]any{"CreatedNode": map[string]any{
			"LedgerEntryType": "Escrow",
			"LedgerIndex":     escrowKeyHex,
			"NewFields": map[string]any{
				"Account": testAccount, "Destination": destination, "Amount": "10000",
			},
		}},
	)

	corrected, err := reconstructFromMeta(putAll(t, nil), []metaTx{{Blob: meta, TxHash: mustIndex(t, testTxHashHex)}}, testLedgerSeq)
	if err != nil {
		t.Fatalf("reconstructFromMeta: %v", err)
	}

	wantEscrow := encodeSLE(t, map[string]any{
		"LedgerEntryType": "Escrow", "Flags": 0, "OwnerNode": "0", "DestinationNode": "0",
		"Account": testAccount, "Destination": destination, "Amount": "10000",
		"PreviousTxnID": testTxHashHex, "PreviousTxnLgrSeq": testLedgerSeq,
	})
	wantDestinationDir := encodeSLE(t, map[string]any{
		"LedgerEntryType": "DirectoryNode", "Flags": 0, "Owner": destination,
		"RootIndex": destinationPageHex, "Indexes": []string{escrowKeyHex},
		"PreviousTxnID": testTxHashHex, "PreviousTxnLgrSeq": testLedgerSeq,
	})
	assertEntryBytes(t, corrected, escrowKey, wantEscrow, "escrow")
	assertEntryBytes(t, corrected, destinationPage, wantDestinationDir, "destination directory")
}

func TestReconstructFromMeta_DirectoryDeletedThenRecreated(t *testing.T) {
	const destination = "rrrrrrrrrrrrrrrrrrrrBZbvji"
	const escrowKeyHex = "07D546EC9389810CEEC2D5C843171BE9F07EAEA05039E5466BF4AC5B19AEAE7C"
	const deletedTxHashHex = "1111111111111111111111111111111111111111111111111111111111111111"
	const priorTxHashHex = "2222222222222222222222222222222222222222222222222222222222222222"

	destinationID, err := state.DecodeAccountID(destination)
	if err != nil {
		t.Fatalf("DecodeAccountID: %v", err)
	}
	authorizedID, err := state.DecodeAccountID(testAccount)
	if err != nil {
		t.Fatalf("DecodeAccountID: %v", err)
	}
	destinationPage := keylet.OwnerDirPage(destinationID, 0).Key
	destinationPageHex := strings.ToUpper(hex.EncodeToString(destinationPage[:]))
	oldKey := keylet.DepositPreauth(destinationID, authorizedID).Key
	oldKeyHex := strings.ToUpper(hex.EncodeToString(oldKey[:]))
	escrowKey := mustIndex(t, escrowKeyHex)

	preState := putAll(t, map[[32]byte][]byte{
		destinationPage: encodeSLE(t, map[string]any{
			"LedgerEntryType": "DirectoryNode", "Flags": 0, "Owner": destination,
			"RootIndex": destinationPageHex, "Indexes": []string{oldKeyHex},
		}),
		oldKey: encodeSLE(t, map[string]any{
			"LedgerEntryType": "DepositPreauth", "Flags": 0,
			"Account": destination, "Authorize": testAccount, "OwnerNode": "0",
			"PreviousTxnID": priorTxHashHex, "PreviousTxnLgrSeq": testLedgerSeq - 1,
		}),
	})
	deleteMeta := encodeMeta(t,
		map[string]any{"DeletedNode": map[string]any{
			"LedgerEntryType": "DepositPreauth",
			"LedgerIndex":     oldKeyHex,
			"FinalFields":     map[string]any{"Account": destination, "Authorize": testAccount, "Flags": 0, "OwnerNode": "0"},
		}},
		map[string]any{"DeletedNode": map[string]any{
			"LedgerEntryType": "DirectoryNode",
			"LedgerIndex":     destinationPageHex,
			"FinalFields":     map[string]any{"Flags": 0, "Owner": destination, "RootIndex": destinationPageHex},
		}},
	)
	createMeta := encodeMeta(t,
		map[string]any{"CreatedNode": map[string]any{
			"LedgerEntryType": "DirectoryNode",
			"LedgerIndex":     destinationPageHex,
			"NewFields":       map[string]any{"Owner": destination, "RootIndex": destinationPageHex},
		}},
		map[string]any{"CreatedNode": map[string]any{
			"LedgerEntryType": "Escrow",
			"LedgerIndex":     escrowKeyHex,
			"NewFields": map[string]any{
				"Account": testAccount, "Destination": destination, "Amount": "10000",
			},
		}},
	)

	corrected, err := reconstructFromMeta(preState, []metaTx{
		{Blob: deleteMeta, TxHash: mustIndex(t, deletedTxHashHex)},
		{Blob: createMeta, TxHash: mustIndex(t, testTxHashHex)},
	}, testLedgerSeq)
	if err != nil {
		t.Fatalf("reconstructFromMeta: %v", err)
	}

	wantDestinationDir := encodeSLE(t, map[string]any{
		"LedgerEntryType": "DirectoryNode", "Flags": 0, "Owner": destination,
		"RootIndex": destinationPageHex, "Indexes": []string{escrowKeyHex},
		"PreviousTxnID": testTxHashHex, "PreviousTxnLgrSeq": testLedgerSeq,
	})
	assertEntryBytes(t, corrected, destinationPage, wantDestinationDir, "recreated destination directory")
	if _, found, err := corrected.Get(escrowKey); err != nil || !found {
		t.Fatalf("recreated escrow missing (found=%v err=%v)", found, err)
	}
	if _, found, err := corrected.Get(oldKey); err != nil || found {
		t.Fatalf("deleted deposit preauth present (found=%v err=%v)", found, err)
	}
}

func TestRecordMembership_LastOperationWins(t *testing.T) {
	const account = "rrrrrrrrrrrrrrrrrrrrBZbvji"
	objectKey := mustIndex(t, "00000000000000000000000000000000000000000000000000000000000000D1")
	otherKey := mustIndex(t, "00000000000000000000000000000000000000000000000000000000000000D2")
	accountID, err := state.DecodeAccountID(account)
	if err != nil {
		t.Fatalf("DecodeAccountID: %v", err)
	}
	pageKey := keylet.OwnerDirPage(accountID, 0).Key
	fields := map[string]any{"Account": account, "OwnerNode": "0"}

	t.Run("remove then add", func(t *testing.T) {
		deltas := map[[32]byte]*dirDelta{}
		if err := recordMembership(nil, deltas, objectKey, "DID", fields, false, nil); err != nil {
			t.Fatalf("record remove: %v", err)
		}
		if err := recordMembership(nil, deltas, objectKey, "DID", fields, true, nil); err != nil {
			t.Fatalf("record add: %v", err)
		}
		members := applyDirDelta([][32]byte{objectKey}, deltas[pageKey])
		if len(members) != 1 || members[0] != objectKey {
			t.Fatalf("members = %x, want recreated key", members)
		}
	})

	t.Run("add then remove", func(t *testing.T) {
		deltas := map[[32]byte]*dirDelta{}
		if err := recordMembership(nil, deltas, objectKey, "DID", fields, true, nil); err != nil {
			t.Fatalf("record add: %v", err)
		}
		if err := recordMembership(nil, deltas, objectKey, "DID", fields, false, nil); err != nil {
			t.Fatalf("record remove: %v", err)
		}
		members := applyDirDelta(nil, deltas[pageKey])
		if len(members) != 0 {
			t.Fatalf("members = %x, want empty", members)
		}
	})

	t.Run("append remove then add", func(t *testing.T) {
		delta := &dirDelta{
			strategy: dirAppend,
			operations: []dirOperation{
				{key: objectKey, add: false},
				{key: objectKey, add: true},
			},
		}
		members := applyDirDelta([][32]byte{objectKey, otherKey}, delta)
		if len(members) != 2 || members[0] != otherKey || members[1] != objectKey {
			t.Fatalf("members = %x, want existing key then re-added key", members)
		}
	})
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
				"Account":     testAccount,
				"Destination": destination,
				"Amount":      map[string]any{"value": "10", "currency": "USD", "issuer": issuer},
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
	wantEscrow := encodeSLE(t, map[string]any{
		"LedgerEntryType": "Escrow", "Flags": 0, "OwnerNode": "0",
		"DestinationNode": "0", "IssuerNode": "0", "Account": testAccount,
		"Destination":   destination,
		"Amount":        map[string]any{"value": "10", "currency": "USD", "issuer": issuer},
		"PreviousTxnID": testTxHashHex, "PreviousTxnLgrSeq": testLedgerSeq,
	})
	assertEntryBytes(t, corrected, issuerPage, wantIssuerDir, "issuer directory")
	assertEntryBytes(t, corrected, mustIndex(t, escrowKey), wantEscrow, "escrow")
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

func TestReconstructFromMeta_LoanCreateDirectories(t *testing.T) {
	const borrower = "rEaWzpDUL2cBckwDJhRENZiKCbNKwG2cAZ"
	const brokerAccount = "rpRrVjCLggyjBaAYreukcyuWuzb23wuWrn"
	const brokerOwner = "rrrrrrrrrrrrrrrrrrrrBZbvji"
	const brokerIDHex = "33353F075781FD714AE43F61FC6B4A88BCB5CE3C348FE0FF40B1F6F803C7D86A"
	const loanKeyHex = "6BABEC1111496AD23DDFCD51DAF11AF98DC018F297A2EE82509D7881A2F50F9C"

	borrowerID, err := state.DecodeAccountID(borrower)
	if err != nil {
		t.Fatalf("decode borrower: %v", err)
	}
	brokerAccountID, err := state.DecodeAccountID(brokerAccount)
	if err != nil {
		t.Fatalf("decode broker account: %v", err)
	}
	brokerID := mustIndex(t, brokerIDHex)
	brokerKey := keylet.LoanBrokerByID(brokerID).Key
	loanKey := mustIndex(t, loanKeyHex)

	for _, tt := range []struct {
		name string
		page uint64
	}{
		{name: "page zero", page: 0},
		{name: "nonzero page", page: 7},
	} {
		t.Run(tt.name, func(t *testing.T) {
			borrowerDir := keylet.OwnerDirPage(borrowerID, tt.page).Key
			brokerDir := keylet.OwnerDirPage(brokerAccountID, tt.page).Key
			borrowerDirHex := protocol.Hash256Hex(borrowerDir)
			brokerDirHex := protocol.Hash256Hex(brokerDir)
			page := strconv.FormatUint(tt.page, 16)

			preState := putAll(t, map[[32]byte][]byte{
				brokerKey: loanBrokerSLE(t, brokerAccount, brokerOwner),
				borrowerDir: encodeSLE(t, map[string]any{
					"LedgerEntryType": "DirectoryNode", "Flags": 0,
					"RootIndex": protocol.Hash256Hex(keylet.OwnerDir(borrowerID).Key),
					"Owner":     borrower,
				}),
				brokerDir: encodeSLE(t, map[string]any{
					"LedgerEntryType": "DirectoryNode", "Flags": 0,
					"RootIndex": protocol.Hash256Hex(keylet.OwnerDir(brokerAccountID).Key),
					"Owner":     brokerAccount,
				}),
			})
			loanNewFields := map[string]any{
				"Borrower": borrower, "LoanBrokerID": brokerIDHex, "LoanSequence": uint32(1),
				"StartDate": uint32(836247371), "PaymentInterval": uint32(400),
				"PeriodicPayment": "10000", "PrincipalOutstanding": "10000",
				"TotalValueOutstanding": "10000",
			}
			if tt.page != 0 {
				loanNewFields["OwnerNode"] = page
				loanNewFields["LoanBrokerNode"] = page
			}
			meta := encodeMeta(t,
				map[string]any{"ModifiedNode": map[string]any{
					"LedgerEntryType": "DirectoryNode", "LedgerIndex": borrowerDirHex,
					"FinalFields": map[string]any{"Flags": 0, "Owner": borrower,
						"RootIndex": protocol.Hash256Hex(keylet.OwnerDir(borrowerID).Key)},
				}},
				map[string]any{"ModifiedNode": map[string]any{
					"LedgerEntryType": "DirectoryNode", "LedgerIndex": brokerDirHex,
					"FinalFields": map[string]any{"Flags": 0, "Owner": brokerAccount,
						"RootIndex": protocol.Hash256Hex(keylet.OwnerDir(brokerAccountID).Key)},
				}},
				map[string]any{"CreatedNode": map[string]any{
					"LedgerEntryType": "Loan", "LedgerIndex": loanKeyHex,
					"NewFields": loanNewFields,
				}},
			)

			corrected, err := reconstructFromMeta(preState, []metaTx{{Blob: meta, TxHash: mustIndex(t, testTxHashHex)}}, testLedgerSeq)
			if err != nil {
				t.Fatalf("reconstructFromMeta: %v", err)
			}
			wantLoan := encodeSLE(t, map[string]any{
				"LedgerEntryType": "Loan", "Flags": 0, "OwnerNode": page, "LoanBrokerNode": page,
				"Borrower": borrower, "LoanBrokerID": brokerIDHex, "LoanSequence": uint32(1),
				"StartDate": uint32(836247371), "PaymentInterval": uint32(400),
				"PeriodicPayment": "10000", "PrincipalOutstanding": "10000",
				"TotalValueOutstanding": "10000", "PreviousTxnID": testTxHashHex,
				"PreviousTxnLgrSeq": testLedgerSeq,
			})
			assertEntryBytes(t, corrected, loanKey, wantLoan, "loan")
			assertDirectoryMembers(t, corrected, borrowerDir, loanKey)
			assertDirectoryMembers(t, corrected, brokerDir, loanKey)
		})
	}
}

func TestReconstructFromMeta_LoanDeleteDirectories(t *testing.T) {
	const borrower = "rEaWzpDUL2cBckwDJhRENZiKCbNKwG2cAZ"
	const brokerAccount = "rpRrVjCLggyjBaAYreukcyuWuzb23wuWrn"
	const brokerOwner = "rrrrrrrrrrrrrrrrrrrrBZbvji"
	const brokerIDHex = "33353F075781FD714AE43F61FC6B4A88BCB5CE3C348FE0FF40B1F6F803C7D86A"
	const loanKeyHex = "6BABEC1111496AD23DDFCD51DAF11AF98DC018F297A2EE82509D7881A2F50F9C"

	borrowerID, _ := state.DecodeAccountID(borrower)
	brokerAccountID, _ := state.DecodeAccountID(brokerAccount)
	brokerID := mustIndex(t, brokerIDHex)
	brokerKey := keylet.LoanBrokerByID(brokerID).Key
	loanKey := mustIndex(t, loanKeyHex)
	borrowerDir := keylet.OwnerDirPage(borrowerID, 0).Key
	brokerDir := keylet.OwnerDirPage(brokerAccountID, 0).Key
	loanFields := map[string]any{
		"LedgerEntryType": "Loan", "Flags": 0, "OwnerNode": "0", "LoanBrokerNode": "0",
		"Borrower": borrower, "LoanBrokerID": brokerIDHex, "LoanSequence": uint32(1),
		"StartDate": uint32(836247371), "PaymentInterval": uint32(400), "PeriodicPayment": "10000",
		"PreviousTxnID": testTxHashHex, "PreviousTxnLgrSeq": testLedgerSeq - 1,
	}
	preState := putAll(t, map[[32]byte][]byte{
		brokerKey: loanBrokerSLE(t, brokerAccount, brokerOwner),
		loanKey:   encodeSLE(t, loanFields),
		borrowerDir: encodeSLE(t, map[string]any{
			"LedgerEntryType": "DirectoryNode", "Flags": 0, "Owner": borrower,
			"RootIndex": protocol.Hash256Hex(borrowerDir), "Indexes": []string{loanKeyHex},
		}),
		brokerDir: encodeSLE(t, map[string]any{
			"LedgerEntryType": "DirectoryNode", "Flags": 0, "Owner": brokerAccount,
			"RootIndex": protocol.Hash256Hex(brokerDir), "Indexes": []string{loanKeyHex},
		}),
	})
	finalFields := maps.Clone(loanFields)
	delete(finalFields, "LedgerEntryType")
	delete(finalFields, "PreviousTxnID")
	delete(finalFields, "PreviousTxnLgrSeq")
	meta := encodeMeta(t, map[string]any{"DeletedNode": map[string]any{
		"LedgerEntryType": "Loan", "LedgerIndex": loanKeyHex, "FinalFields": finalFields,
	}})

	corrected, err := reconstructFromMeta(preState, []metaTx{{Blob: meta, TxHash: mustIndex(t, testTxHashHex)}}, testLedgerSeq)
	if err != nil {
		t.Fatalf("reconstructFromMeta: %v", err)
	}
	if _, found, err := corrected.Get(loanKey); err != nil || found {
		t.Fatalf("deleted loan present (found=%v err=%v)", found, err)
	}
	assertDirectoryMembers(t, corrected, borrowerDir)
	assertDirectoryMembers(t, corrected, brokerDir)
}

func TestReconstructFromMeta_LoanBrokerDeleteDirectories(t *testing.T) {
	const (
		brokerAccount = "rEaWzpDUL2cBckwDJhRENZiKCbNKwG2cAZ"
		owner         = "rrrrrrrrrrrrrrrrrrrrBZbvji"
		vaultAccount  = "rpRrVjCLggyjBaAYreukcyuWuzb23wuWrn"
		brokerIDHex   = "33353F075781FD714AE43F61FC6B4A88BCB5CE3C348FE0FF40B1F6F803C7D86A"
		vaultIDHex    = "44453F075781FD714AE43F61FC6B4A88BCB5CE3C348FE0FF40B1F6F803C7D86A"
	)

	ownerID, _ := state.DecodeAccountID(owner)
	vaultAccountID, _ := state.DecodeAccountID(vaultAccount)
	brokerKey := mustIndex(t, brokerIDHex)
	vaultID := mustIndex(t, vaultIDHex)
	ownerDir := keylet.OwnerDirPage(ownerID, 2).Key
	vaultDir := keylet.OwnerDirPage(vaultAccountID, 3).Key
	brokerFields := map[string]any{
		"LedgerEntryType": "LoanBroker", "Flags": 0, "Sequence": uint32(1),
		"OwnerNode": "2", "VaultNode": "3", "VaultID": vaultIDHex,
		"Account": brokerAccount, "Owner": owner, "LoanSequence": uint32(1),
		"PreviousTxnID": testTxHashHex, "PreviousTxnLgrSeq": testLedgerSeq - 1,
	}
	preState := putAll(t, map[[32]byte][]byte{
		brokerKey:                     encodeSLE(t, brokerFields),
		keylet.VaultByID(vaultID).Key: vaultSLE(t, vaultAccount, owner),
		ownerDir: encodeSLE(t, map[string]any{
			"LedgerEntryType": "DirectoryNode", "Flags": 0, "Owner": owner,
			"RootIndex": protocol.Hash256Hex(keylet.OwnerDir(ownerID).Key), "Indexes": []string{brokerIDHex},
		}),
		vaultDir: encodeSLE(t, map[string]any{
			"LedgerEntryType": "DirectoryNode", "Flags": 0, "Owner": vaultAccount,
			"RootIndex": protocol.Hash256Hex(keylet.OwnerDir(vaultAccountID).Key), "Indexes": []string{brokerIDHex},
		}),
	})
	finalFields := maps.Clone(brokerFields)
	delete(finalFields, "LedgerEntryType")
	delete(finalFields, "PreviousTxnID")
	delete(finalFields, "PreviousTxnLgrSeq")
	meta := encodeMeta(t, map[string]any{"DeletedNode": map[string]any{
		"LedgerEntryType": "LoanBroker", "LedgerIndex": brokerIDHex, "FinalFields": finalFields,
	}})

	corrected, err := reconstructFromMeta(preState, []metaTx{{Blob: meta}}, testLedgerSeq)
	if err != nil {
		t.Fatalf("reconstructFromMeta: %v", err)
	}
	if _, found, err := corrected.Get(brokerKey); err != nil || found {
		t.Fatalf("deleted LoanBroker present (found=%v err=%v)", found, err)
	}
	assertDirectoryMembers(t, corrected, ownerDir)
	assertDirectoryMembers(t, corrected, vaultDir)
}

func TestReconstructFromMeta_LoanDeleteAfterLoanBrokerDelete(t *testing.T) {
	const borrower = "rEaWzpDUL2cBckwDJhRENZiKCbNKwG2cAZ"
	const brokerAccount = "rpRrVjCLggyjBaAYreukcyuWuzb23wuWrn"
	const brokerOwner = "rrrrrrrrrrrrrrrrrrrrBZbvji"
	const brokerIDHex = "33353F075781FD714AE43F61FC6B4A88BCB5CE3C348FE0FF40B1F6F803C7D86A"
	const loanKeyHex = "6BABEC1111496AD23DDFCD51DAF11AF98DC018F297A2EE82509D7881A2F50F9C"

	borrowerID, _ := state.DecodeAccountID(borrower)
	brokerAccountID, _ := state.DecodeAccountID(brokerAccount)
	brokerID := mustIndex(t, brokerIDHex)
	brokerKey := keylet.LoanBrokerByID(brokerID).Key
	loanKey := mustIndex(t, loanKeyHex)
	borrowerDir := keylet.OwnerDirPage(borrowerID, 0).Key
	brokerDir := keylet.OwnerDirPage(brokerAccountID, 0).Key
	loanFields := map[string]any{
		"LedgerEntryType": "Loan", "Flags": 0, "OwnerNode": "0", "LoanBrokerNode": "0",
		"Borrower": borrower, "LoanBrokerID": brokerIDHex, "LoanSequence": uint32(1),
		"StartDate": uint32(836247371), "PaymentInterval": uint32(400), "PeriodicPayment": "10000",
	}
	preState := putAll(t, map[[32]byte][]byte{
		brokerKey:                        loanBrokerSLE(t, brokerAccount, brokerOwner),
		loanKey:                          encodeSLE(t, loanFields),
		keylet.VaultByID([32]byte{}).Key: vaultSLE(t, brokerAccount, brokerOwner),
		borrowerDir: encodeSLE(t, map[string]any{
			"LedgerEntryType": "DirectoryNode", "Flags": 0, "Owner": borrower,
			"RootIndex": protocol.Hash256Hex(borrowerDir), "Indexes": []string{loanKeyHex},
		}),
		brokerDir: encodeSLE(t, map[string]any{
			"LedgerEntryType": "DirectoryNode", "Flags": 0, "Owner": brokerAccount,
			"RootIndex": protocol.Hash256Hex(brokerDir), "Indexes": []string{loanKeyHex},
		}),
	})
	finalFields := maps.Clone(loanFields)
	delete(finalFields, "LedgerEntryType")
	meta := encodeMeta(t,
		map[string]any{"DeletedNode": map[string]any{
			"LedgerEntryType": "LoanBroker", "LedgerIndex": brokerIDHex,
			"FinalFields": map[string]any{
				"Account": brokerAccount, "Owner": brokerOwner,
				"OwnerNode": "0", "VaultNode": "0", "VaultID": strings.Repeat("0", 64),
			},
		}},
		map[string]any{"DeletedNode": map[string]any{
			"LedgerEntryType": "Loan", "LedgerIndex": loanKeyHex,
			"FinalFields": finalFields,
		}},
	)

	corrected, err := reconstructFromMeta(preState, []metaTx{{Blob: meta}}, testLedgerSeq)
	if err != nil {
		t.Fatalf("reconstructFromMeta: %v", err)
	}
	for name, key := range map[string][32]byte{"loan": loanKey, "broker": brokerKey} {
		if _, found, err := corrected.Get(key); err != nil || found {
			t.Fatalf("deleted %s present (found=%v err=%v)", name, found, err)
		}
	}
	assertDirectoryMembers(t, corrected, borrowerDir)
	assertDirectoryMembers(t, corrected, brokerDir)
}

func TestReconstructFromMeta_LoanBrokerResolutionErrors(t *testing.T) {
	const brokerIDHex = "33353F075781FD714AE43F61FC6B4A88BCB5CE3C348FE0FF40B1F6F803C7D86A"
	brokerID := mustIndex(t, brokerIDHex)
	brokerKey := keylet.LoanBrokerByID(brokerID).Key
	meta := encodeMeta(t, map[string]any{"CreatedNode": map[string]any{
		"LedgerEntryType": "Loan",
		"LedgerIndex":     "6BABEC1111496AD23DDFCD51DAF11AF98DC018F297A2EE82509D7881A2F50F9C",
		"NewFields": map[string]any{
			"Borrower": "rEaWzpDUL2cBckwDJhRENZiKCbNKwG2cAZ", "LoanBrokerID": brokerIDHex,
			"LoanSequence": uint32(1), "StartDate": uint32(1), "PaymentInterval": uint32(1),
			"PeriodicPayment": "1",
		},
	}})

	for _, tt := range []struct {
		name    string
		entries map[[32]byte][]byte
		want    string
	}{
		{name: "missing broker", want: "not found"},
		{name: "corrupt broker", entries: map[[32]byte][]byte{brokerKey: bytes.Repeat([]byte{0xff}, 12)}, want: "decoding LoanBroker"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := reconstructFromMeta(putAll(t, tt.entries), []metaTx{{Blob: meta}}, testLedgerSeq)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestReconstructFromMeta_LoanBrokerVaultResolutionErrors(t *testing.T) {
	const vaultIDHex = "44453F075781FD714AE43F61FC6B4A88BCB5CE3C348FE0FF40B1F6F803C7D86A"
	vaultID := mustIndex(t, vaultIDHex)
	vaultKey := keylet.VaultByID(vaultID).Key
	meta := encodeMeta(t, map[string]any{"CreatedNode": map[string]any{
		"LedgerEntryType": "LoanBroker",
		"LedgerIndex":     "33353F075781FD714AE43F61FC6B4A88BCB5CE3C348FE0FF40B1F6F803C7D86A",
		"NewFields": map[string]any{
			"Account":      "rEaWzpDUL2cBckwDJhRENZiKCbNKwG2cAZ",
			"Owner":        "rrrrrrrrrrrrrrrrrrrrBZbvji",
			"Sequence":     uint32(1),
			"LoanSequence": uint32(1),
			"VaultID":      vaultIDHex,
		},
	}})

	for _, tt := range []struct {
		name    string
		entries map[[32]byte][]byte
		want    string
	}{
		{name: "missing vault", want: "not found"},
		{
			name:    "corrupt vault",
			entries: map[[32]byte][]byte{vaultKey: bytes.Repeat([]byte{0xff}, 12)},
			want:    "decoding Vault",
		},
		{
			name: "wrong entry type",
			entries: map[[32]byte][]byte{vaultKey: encodeSLE(t, map[string]any{
				"LedgerEntryType": "AccountRoot", "Account": testAccount,
				"Balance": "0", "Flags": 0, "OwnerCount": 0, "Sequence": 0,
			})},
			want: "resolved to AccountRoot",
		},
		{
			name: "missing account",
			entries: map[[32]byte][]byte{vaultKey: encodeSLE(t, map[string]any{
				"LedgerEntryType": "Vault", "Flags": 0, "Sequence": uint32(1),
				"OwnerNode": "0", "Owner": testAccount,
			})},
			want: "invalid Account",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := reconstructFromMeta(putAll(t, tt.entries), []metaTx{{Blob: meta}}, testLedgerSeq)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func loanBrokerSLE(t *testing.T, account, owner string) []byte {
	t.Helper()
	return encodeSLE(t, map[string]any{
		"LedgerEntryType": "LoanBroker", "Flags": 0, "Sequence": uint32(0),
		"OwnerNode": "0", "VaultNode": "0", "VaultID": strings.Repeat("0", 64),
		"Account": account, "Owner": owner, "LoanSequence": uint32(0),
		"PreviousTxnID": strings.Repeat("0", 64), "PreviousTxnLgrSeq": uint32(0),
	})
}

func vaultSLE(t *testing.T, account, owner string) []byte {
	t.Helper()
	return encodeSLE(t, map[string]any{
		"LedgerEntryType": "Vault", "Flags": 0, "Sequence": uint32(0),
		"OwnerNode": "0", "Owner": owner, "Account": account,
		"Asset":      map[string]any{"currency": "XRP"},
		"ShareMPTID": strings.Repeat("0", 48), "WithdrawalPolicy": 1,
		"PreviousTxnID": strings.Repeat("0", 64), "PreviousTxnLgrSeq": uint32(0),
	})
}

func assertDirectoryMembers(t *testing.T, m *shamap.SHAMap, key [32]byte, want ...[32]byte) {
	t.Helper()
	item, found, err := m.Get(key)
	if err != nil || !found || item == nil {
		t.Fatalf("directory missing (found=%v err=%v)", found, err)
	}
	obj, err := binarycodec.Decode(hex.EncodeToString(item.Data()))
	if err != nil {
		t.Fatalf("decode directory: %v", err)
	}
	if got := decodeIndexes(obj["Indexes"]); !slices.Equal(got, want) {
		t.Fatalf("directory members = %X, want %X", got, want)
	}
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
