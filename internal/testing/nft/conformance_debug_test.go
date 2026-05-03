package nft_test

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

// TestConformanceMintBurn mimics the conformance test flow:
// 1. Create env with all amendments (as default test env)
// 2. Submit NFTokenMint from master account (via pre-built tx_blob)
// 3. Close ledger
// 4. Submit NFTokenBurn (via pre-built tx_blob)
func TestConformanceMintBurn(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.Close()

	masterID, _ := state.DecodeAccountID("rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
	maxPageKL := keylet.NFTokenPageMax(masterID)

	// Step 1: Parse and submit NFTokenMint
	mintBlob, _ := hex.DecodeString("1200192400000001202A0000000068400000000000000A73210330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD02074473045022100858210F05EA58ACCAD60DA2EB7D505EF56016E0D323698D4819199358809120D022007B68AF69934E1E1DD12CC2A9E5D0F9696D5BDFA8CAA50CA29A36B9AA26C2F0A8114B5F762798A53D543A014CAF8B297CFF8F2F937E8")
	mintTx, err := tx.ParseFromBinary(mintBlob)
	if err != nil {
		t.Fatalf("Failed to parse mint blob: %v", err)
	}

	mintResult := env.Submit(mintTx)
	t.Logf("Mint result: %s", mintResult.Code)
	if mintResult.Code != "tesSUCCESS" {
		t.Fatalf("Expected tesSUCCESS for mint, got %s", mintResult.Code)
	}

	// Check the minted token
	if env.LedgerEntryExists(maxPageKL) {
		pageData, _ := env.LedgerEntry(maxPageKL)
		page, _ := state.ParseNFTokenPage(pageData)
		t.Logf("Minted token(s): %d", len(page.NFTokens))
		for i, tok := range page.NFTokens {
			t.Logf("  Token %d: %s", i, hex.EncodeToString(tok.NFTokenID[:]))
		}
	}

	env.Close()

	// Step 2: Parse and submit NFTokenBurn
	// The expected token ID is:
	// 00000000B5F762798A53D543A014CAF8B297CFF8F2F937E816E5DA9C00000001
	// (seq=1, taxon=cipheredTaxon(1,0) = 0x16E5DA9C)
	// Burn blob from conformance fixture (NFTokenAllFeatures/Enabled.json step 13)
	burnBlob, _ := hex.DecodeString("12001A24000000025A00000000B5F762798A53D543A014CAF8B297CFF8F2F937E816E5DA9C0000000168400000000000000A73210330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD02074473045022100B4F1A56F032D528FEA00C69DA36B9292B4FAD6A0535CF0481AA97F008E38ABB002207AD3450D5425E733CD79E9012C429A657A2FD02615070078F8085D904AC617938114B5F762798A53D543A014CAF8B297CFF8F2F937E8")
	burnTx, err := tx.ParseFromBinary(burnBlob)
	if err != nil {
		t.Fatalf("Failed to parse burn blob: %v", err)
	}

	burnResult := env.Submit(burnTx)
	t.Logf("Burn result: %s", burnResult.Code)
	if burnResult.Code != "tesSUCCESS" {
		t.Errorf("Expected tesSUCCESS for burn, got %s", burnResult.Code)
	}
}
