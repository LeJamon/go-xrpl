package trustset

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

func disableAllAmendmentsBD(env *jtx.TestEnv) {
	for _, f := range amendment.AllFeatures() {
		env.DisableFeature(f.Name)
	}
}

// TestReproByteDiff_DirPagination triggers the 33rd TrustSet from one source
// — when the owner directory's first page fills (32 entries) and a new page
// is allocated. This is the 6-AffectedNode pattern seen in iter7 / iter8 for
// each pagination event. Seeds match rippled v2.6.2 standalone (see
// $CLAUDE_JOB_DIR/rippled_pagination_output.json) for byte-by-byte comparison.
// Amendments are disabled to mirror the soak network's pre-vote state.
func TestReproByteDiff_DirPagination(t *testing.T) {
	env := jtx.NewTestEnv(t)
	disableAllAmendmentsBD(env)

	const srcSeed = "shuajNRGVnV937mqdsg8SpQ8pDcpR"
	issuerSeeds := []string{
		"shqmBSEtTXCgXSJUCgzzZCxizEKba", "snt8WdQ7qV7TGF1P7wKZJ4t4ohqAY", "saE9bmU83Fic8ezu1YmEpNQmghUpe", "shQg1CHVzXR7P8xCTpVLPbWfNiANU",
		"sptfdGCf4U923qSGepp8y1BbYBFtj", "shiZsWog5hKe1YLVG8wwvSWFZgkUE", "saNK1bZrZjrznovQaf5fuFKYMZ8W4", "sn6yT1Qz1Fzzq99R2SbcqgZaHRdDR",
		"snwqZHQW8R5xtFrBeGqet8vEvr1Zz", "spvWy8K2hpDKFoh1CmCzi3d15Fowb", "shqGTh8YEJccp4qPK2A8L8wYWnuiF", "ssNb6yQnATWyxdLRkUqT15kPYUxxG",
		"snuUG5ckBrCJ7pGMYRzfD4P77kymz", "ssrsug1WbH3zsAUcrWRCaxf2VWKWf", "shHV2kjeZap3waTLwpsf6Y6VbW5vs", "snDns7K7BvLRovUkoL4K6N6CEWMLQ",
		"snATjs4BzXcMMGVgHWxbbKMPogzDh", "ssibApc3TojkpQKSnqGj1XQT1C1RM", "snhUhBucDsJF11AGMDSGxQJfQV9rY", "sh2TPjrLvqGDqu9D8KyQPdrg9DxeN",
		"shPmzRSQ45aDNFHy8eDjXtqY5E9Yn", "snovpMmMhF9yogZZUkj7TPWc2RC1H", "sneovhyEFg8n5qfB5YXjkoiJPSJR2", "snosobo179rjjTRQaaXqKofUnf2u8",
		"shgH4r7TeaE3AcxcLaPNLgBTJCmTG", "ssgvSyDJi6u47SBpP3cBp8kGdDZgs", "shujkQX3jFBFBzcHFNRhfrPX1xX9A", "spq4d1t6zj2BHJGUAPQ6YZCbxbCSz",
		"sap2PqeZ7rLjaacXawQBh45Vevbq1", "sahTkDsg4DUMoect4uYmvecrhiW5Z", "snDzo2TqiFLzusnVsDgu28Wws3gAR", "ssjzh5CpnHiPFG8rCraZxaMa8GH8M",
		"ssL8WivwzdMWK2CUhoe8mLBfqKLWS",
	}
	if len(issuerSeeds) != 33 {
		t.Fatalf("need exactly 33 seeds, got %d", len(issuerSeeds))
	}

	src := jtx.NewAccountFromSeed("src", srcSeed)
	issuers := make([]*jtx.Account, 33)
	all := make([]*jtx.Account, 0, 34)
	all = append(all, src)
	for i := range 33 {
		issuers[i] = jtx.NewAccountFromSeed(fmt.Sprintf("iss%02d", i), issuerSeeds[i])
		all = append(all, issuers[i])
	}
	// Fund with 100 XRP to cover 33 trust line reserves (10 base + 33*2 = 76 XRP).
	for _, acc := range all {
		env.FundAmountNoRipple(acc, 100_000_000_000)
	}
	env.Close()

	// 32 TrustSets — fills the first owner dir page (32 entries max).
	for i := range 32 {
		limit := tx.NewIssuedAmountFromFloat64(1000, "USD", issuers[i].Address)
		r := env.Submit(TrustSet(src, limit).Build())
		jtx.RequireTxSuccess(t, r)
	}
	env.Close()
	t.Logf("After 32 TrustSets: OwnerCount=32, 1 dir page")

	// 33rd TrustSet — this should trigger a NEW PAGE creation.
	limit := tx.NewIssuedAmountFromFloat64(1000, "USD", issuers[32].Address)
	pag := env.Submit(TrustSet(src, limit).Build())
	jtx.RequireTxSuccess(t, pag)
	t.Logf("33rd TrustSet (pagination) code=%s, meta nodes=%d", pag.Code, len(pag.Metadata.AffectedNodes))
	pb, err := tx.SerializeMetadata(pag.Metadata)
	if err != nil {
		t.Fatalf("SerializeMetadata pagination: %v", err)
	}
	fmt.Printf("\nGOXRPL_PAGINATION_META_HEX (%d bytes): %s\n", len(pb), strings.ToUpper(hex.EncodeToString(pb)))
	metaMap := tx.MetadataToMap(pag.Metadata)
	jsonBytes, _ := json.MarshalIndent(metaMap, "", "  ")
	fmt.Printf("\n=== GOXRPL META JSON (33rd TS — pagination) ===\n%s\n", string(jsonBytes))
	env.Close()
}

// TestReproByteDiff_ModifyExistingTrustLine reproduces the exact scenario from
// rippled standalone in $CLAUDE_JOB_DIR/rippled_repro.py and dumps the
// resulting tx + meta blob hex for byte-by-byte comparison.
//
// Setup mirrors the rippled run:
//   - REB (high addr) seeded with the same secp256k1 key as rippled's.
//   - R4 (low addr) seeded with the same.
//   - REB TrustSet R4 USD 1000000 first (creates trust line, REB sets high limit).
//   - R4 TrustSet REB USD 1000000 second (modifies existing trust line, adds
//     low reserve).
//
// Print the second tx's meta_blob hex; the rippled hex from the standalone
// run is in $CLAUDE_JOB_DIR/rippled_repro_output.json. The first byte
// where they differ identifies the metadata-serialization bug.
func TestReproByteDiff_ModifyExistingTrustLine(t *testing.T) {
	// Same seeds as the rippled v2.6.2 standalone produced — see
	// $CLAUDE_JOB_DIR/rippled_modify_meta.json. Pre-vote (no amendments)
	// to mirror the soak network's state.
	const REBSeed = "shutW9X6jm9Uo3eTPkhweAcv8cYeP"
	const R4Seed = "sa38ZRR4x9dX64iQQh7mcfVn66Ba5"

	env := jtx.NewTestEnv(t)
	disableAllAmendmentsBD(env)

	reb := jtx.NewAccountFromSeed("reb", REBSeed)
	r4 := jtx.NewAccountFromSeed("r4", R4Seed)
	t.Logf("REB addr: %s", reb.Address)
	t.Logf("R4  addr: %s", r4.Address)

	// Fund both with 10 XRP, no DefaultRipple (match rippled standalone setup)
	env.FundAmountNoRipple(reb, 10000000000)
	env.FundAmountNoRipple(r4, 10000000000)
	env.Close()

	// 1st TrustSet: REB → R4 (REB is source) — THE 5-NODE CREATE-NEW-TRUST-LINE TARGET
	limit1 := tx.NewIssuedAmountFromFloat64(1000000, "USD", r4.Address)
	first := env.Submit(TrustSet(reb, limit1).Build())
	jtx.RequireTxSuccess(t, first)
	if first.Metadata != nil {
		fb, err := tx.SerializeMetadata(first.Metadata)
		if err != nil {
			t.Fatalf("SerializeMetadata first: %v", err)
		}
		fmt.Printf("\nGOXRPL FIRST_META_BLOB_HEX (%d bytes): %s\n", len(fb), strings.ToUpper(hex.EncodeToString(fb)))
		metaMap := tx.MetadataToMap(first.Metadata)
		jsonBytes, _ := json.MarshalIndent(metaMap, "", "  ")
		fmt.Printf("\n=== GOXRPL META JSON (1st tx — 5-node CREATE) ===\n%s\n", string(jsonBytes))
	}
	env.Close()

	// 2nd TrustSet: R4 → REB (R4 is source) — THE TARGET (modifies existing)
	limit2 := tx.NewIssuedAmountFromFloat64(1000000, "USD", reb.Address)
	targetTxn := TrustSet(r4, limit2).Build()
	target := env.Submit(targetTxn)
	jtx.RequireTxSuccess(t, target)

	t.Logf("Target tx code: %s", target.Code)
	if target.Metadata != nil {
		metaBlob, err := tx.SerializeMetadata(target.Metadata)
		if err != nil {
			t.Fatalf("SerializeMetadata: %v", err)
		}
		t.Logf("META_BLOB hex (%d bytes): %s", len(metaBlob), strings.ToUpper(hex.EncodeToString(metaBlob)))
		// Dump as JSON for structural compare with rippled JSON
		metaMap := tx.MetadataToMap(target.Metadata)
		jsonBytes, _ := json.MarshalIndent(metaMap, "", "  ")
		fmt.Printf("\n=== GOXRPL META JSON ===\n%s\n", string(jsonBytes))
	}
	env.Close()

	closed := env.LastClosedLedger()
	if closed == nil {
		t.Fatal("no closed ledger")
	}
	t.Logf("Closed ledger seq=%d hash=%x", closed.Sequence(), closed.Hash())

	// Walk tx tree and find the target tx
	t.Logf("Ledger TxCount() = %d", closed.TxCount())
	err := closed.ForEachTransaction(func(txHash [32]byte, blob []byte) bool {
		txData, metaData, err := tx.SplitTxWithMetaBlob(blob)
		if err != nil {
			t.Logf("  tx %x split err: %v", txHash[:6], err)
			return true
		}
		t.Logf("=== GOXRPL TX ENTRY ===")
		t.Logf("hash: %x", txHash)
		t.Logf("tx_blob_len: %d, meta_blob_len: %d", len(txData), len(metaData))
		t.Logf("TX_BLOB_HEX: %s", strings.ToUpper(hex.EncodeToString(txData)))
		t.Logf("META_BLOB_HEX: %s", strings.ToUpper(hex.EncodeToString(metaData)))
		// Also print via fmt for capture
		fmt.Printf("\n--- hash %x ---\nTX_BLOB: %s\nMETA_BLOB: %s\n",
			txHash, strings.ToUpper(hex.EncodeToString(txData)), strings.ToUpper(hex.EncodeToString(metaData)))
		return true
	})
	if err != nil {
		t.Logf("ForEachTransaction err: %v", err)
	}
}

// TestReproByteDiff_MultiTrustSetThreading reproduces 3 sequential TrustSets
// from the same source in the same ledger. Mirrors
// $CLAUDE_JOB_DIR/rippled_multi_ts.json (rippled v2.6.2 standalone).
// Verifies per-tx PreviousTxnID threading on the source AccountRoot.
func TestReproByteDiff_MultiTrustSetThreading(t *testing.T) {
	const srcSeed = "shihoZLAj68B7wLvXcnQehHGEuFpH"
	issuerSeeds := []string{
		"spuUhpWSH9PQwY52W4m7Gn3YKwVcv",
		"snFN35cU8EKc6oy6Vc6B7KkfvkmSd",
		"snSYRBE4UTbmYqXCVgk7YS7PrhvsM",
	}

	env := jtx.NewTestEnv(t)
	disableAllAmendmentsBD(env)

	src := jtx.NewAccountFromSeed("src", srcSeed)
	issuers := make([]*jtx.Account, 3)
	for i := range 3 {
		issuers[i] = jtx.NewAccountFromSeed(fmt.Sprintf("iss%d", i), issuerSeeds[i])
	}

	// Fund all with 50 XRP (matches rippled standalone setup).
	for _, acc := range append([]*jtx.Account{src}, issuers...) {
		env.FundAmountNoRipple(acc, 50_000_000_000)
	}
	env.Close()

	// 3 sequential TrustSets from src in the same ledger (no env.Close between)
	for i := range 3 {
		limit := tx.NewIssuedAmountFromFloat64(1000, "USD", issuers[i].Address)
		r := env.Submit(TrustSet(src, limit).Build())
		jtx.RequireTxSuccess(t, r)
		if r.Metadata != nil {
			mb, err := tx.SerializeMetadata(r.Metadata)
			if err != nil {
				t.Fatalf("SerializeMetadata TS%d: %v", i, err)
			}
			fmt.Printf("\nGOXRPL_MULTI_TS_%d_META_HEX (%d bytes): %s\n", i, len(mb), strings.ToUpper(hex.EncodeToString(mb)))
		}
	}
	env.Close()
}
