package trustset

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/goXRPLd/amendment"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/tx"
)

func disableAllAmendments(env *jtx.TestEnv) {
	for _, f := range amendment.AllFeatures() {
		env.DisableFeature(f.Name)
	}
}

// TestReproByteDiff_DirPagination triggers the 33rd TrustSet from one source
// — when the owner directory's first page fills (32 entries) and a new page
// is allocated. This is the 6-AffectedNode pattern seen in iter7 / iter8 for
// each pagination event. If goxrpl's meta for the 33rd tx differs from
// rippled's, this is where to look.
func TestReproByteDiff_DirPagination(t *testing.T) {
	env := jtx.NewTestEnv(t)
	disableAllAmendments(env)

	// Seeds match rippled standalone $CLAUDE_JOB_DIR/rippled_pagination_output.json
	// to enable byte-by-byte meta_blob comparison.
	const srcSeed = "shSf2euXnYF8omhjcUR5UqBNBYvrN"
	issuerSeeds := []string{
		"shrUDYFNEUhBv2VUja7vAqcMHsn1L", "ssm6LttMh3KwwVH1cnhF8VBMPCfru",
		"ss42dxEMhUspfug5qJDnhFtQhzwq6", "sn92HpBQqjwtJtZDfkKyfBTWu4rDQ",
		"sspt9XnQYksLyrjAigzmf4eBqLKrB", "saaG3N4SuFdqgmNZhfgRfBa2Tu4qm",
		"snrFVFURexGSP9LbEdDQPuGt9yjGj", "shW6B2PJKGV5L4XaPur2zEwqQTf9y",
		"shhRKHRRXR3Dj8BhZbSKqnvmRa6cq", "shgmANCy2gBagP8LnzuDvZ5M3g9hA",
		"snWhzB7bbUPDVR1xZFQBid46iXavp", "snwJQZJsCprNt4PUB77E8cZqt6DL4",
		"ss1rGR2UDGAMDad4kLEZU3vA6bnoW", "snrK6Q3AL8wAqao5jMKTMDmU22fdk",
		"sniTzxfwQQtxfgprxSLpQGKz5yyM2", "sn3a6PJjnhi4wc7h89MsZQhmKTJ9U",
		"ssvH4piFAnV3ido2kearXC9M6gsoX", "ssXPhwXp8rXVC8AVd33VmRDgESGst",
		"snvjh4wsCHKACn7gbHjUzLFxyQj4j", "ssjCSxc4vRsge9pTSUTMkWXR9rsLG",
		"sa3dQUNBxNWAqeE5d5T8UmBzUvNez", "ss4ewQhybG3Nwor4fe1x5q8WgtwFP",
		"snrewJ34RqFnyYLvsLnuqpXRNF8UK", "sskrmdEhDSSALwiZk1ArahwXbpLay",
		"snnZdPcoazdaz9ChNNR3R8oH7aQEj", "sh1wqz6zqhume6ghotvjQgKZkYTgN",
		"snjXa5ZJR1TTK9yCXGNRQAA8cGgn9", "shp4fknAn7mHKwRuFMkjtehZZowVy",
		"ssvd8zvey57EuziZjWJVmikgFokRS", "ssYRpwJp2quGtAtZnCJZEEtH6yr5w",
		"sspgHz32gh6Atow1ztzYuS5DpKcSw", "spvpHuXjvtfQrqrtFoCYSpY5pncJM",
		"shVEEkq5RAcdZGH3Crm4rR7Gp58z6",
	}
	if len(issuerSeeds) != 33 {
		t.Fatalf("need exactly 33 seeds, got %d", len(issuerSeeds))
	}

	src := jtx.NewAccountFromSeed("src", srcSeed)
	issuers := make([]*jtx.Account, 33)
	all := []*jtx.Account{src}
	for i := 0; i < 33; i++ {
		issuers[i] = jtx.NewAccountFromSeed(fmt.Sprintf("iss%02d", i), issuerSeeds[i])
		all = append(all, issuers[i])
	}
	// Fund with 100 XRP to cover 33 trust line reserves (10 base + 33*2 = 76 XRP).
	for _, acc := range all {
		env.FundAmountNoRipple(acc, 100_000_000_000)
	}
	env.Close()

	// 32 TrustSets — fills the first owner dir page (32 entries max).
	for i := 0; i < 32; i++ {
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
	// Same seeds as rippled standalone produced.
	const REBSeed = "ssT3VWw382SXrJQ5N2ebAoucnTRSU"
	const R4Seed = "snku3scoC3i3DZWnZDwbMm7mMRLQP"

	env := jtx.NewTestEnv(t)

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
