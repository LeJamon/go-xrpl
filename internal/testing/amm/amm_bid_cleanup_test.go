package amm_test

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/testing/metadata"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	coreamm "github.com/LeJamon/go-xrpl/internal/tx/amm"
	sponsortx "github.com/LeJamon/go-xrpl/internal/tx/sponsor"
	"github.com/LeJamon/go-xrpl/keylet"
)

type bidCleanupFixture struct {
	env        *amm.AMMTestEnv
	ammAccount *jtx.Account
	lineKey    keylet.Keylet
	amount     tx.Amount
}

func newBidCleanupFixture(t *testing.T) bidCleanupFixture {
	t.Helper()

	env := setupAMM(t)
	requested := env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 100)
	result := env.Submit(amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
		LPTokenOut(requested).
		LPToken().
		Build())
	if !result.Success {
		t.Fatalf("AMMDeposit: %s - %s", result.Code, result.Message)
	}
	env.Close()

	ammAccount := env.ReadAMMAccount(amm.XRP(), env.USD)
	if ammAccount == nil {
		t.Fatal("AMM account not found")
	}
	currency := coreamm.GenerateAMMLPTCurrency("XRP", "USD")
	held := env.IOUBalance(env.Carol, ammAccount, currency)
	if held == nil || held.IsZero() {
		t.Fatal("Carol has no LP tokens")
	}

	return bidCleanupFixture{
		env:        env,
		ammAccount: ammAccount,
		lineKey:    keylet.Line(env.Carol.ID, ammAccount.ID, currency),
		amount:     *held,
	}
}

func (f bidCleanupFixture) bidAll(t *testing.T) jtx.TxResult {
	t.Helper()
	result := f.env.Submit(amm.AMMBid(f.env.Carol, amm.XRP(), f.env.USD).
		BidMin(f.amount).
		Build())
	if !result.Success {
		t.Fatalf("AMMBid: %s - %s", result.Code, result.Message)
	}
	return result
}

func readBidCleanupAccount(t *testing.T, env *amm.AMMTestEnv, account *jtx.Account) *state.AccountRoot {
	t.Helper()
	data, err := env.LedgerEntry(keylet.Account(account.ID))
	if err != nil {
		t.Fatalf("read account %s: %v", account.Name, err)
	}
	root, err := state.ParseAccountRoot(data)
	if err != nil {
		t.Fatalf("parse account %s: %v", account.Name, err)
	}
	return root
}

func bidCleanupDirectoryContains(t *testing.T, env *amm.AMMTestEnv, account *jtx.Account, item [32]byte) bool {
	t.Helper()
	found := false
	if err := state.DirForEach(env.Ledger(), keylet.OwnerDir(account.ID), func(key [32]byte) error {
		found = found || key == item
		return nil
	}); err != nil {
		t.Fatalf("read owner directory for %s: %v", account.Name, err)
	}
	return found
}

func bidCleanupMetadataNode(result jtx.TxResult, nodeType string, entryKey keylet.Keylet) *tx.AffectedNode {
	if result.Metadata == nil {
		return nil
	}
	want := strings.ToUpper(hex.EncodeToString(entryKey.Key[:]))
	for i := range result.Metadata.AffectedNodes {
		node := &result.Metadata.AffectedNodes[i]
		if node.NodeType == nodeType && node.LedgerIndex == want {
			return node
		}
	}
	return nil
}

func requireBidCleanupReserveMetadata(t *testing.T, node *tx.AffectedNode, holderIsLow bool) {
	t.Helper()
	reserveFlag := state.LsfHighReserve
	if holderIsLow {
		reserveFlag = state.LsfLowReserve
	}
	if flags := metadata.ToUint32(node.PreviousFields["Flags"]); flags&reserveFlag == 0 {
		t.Fatalf("PreviousFields.Flags = 0x%X, want holder reserve 0x%X", flags, reserveFlag)
	}
	if flags := metadata.ToUint32(node.FinalFields["Flags"]); flags&reserveFlag != 0 {
		t.Fatalf("FinalFields.Flags = 0x%X, holder reserve 0x%X was not cleared", flags, reserveFlag)
	}
}

func TestAMMBidDeletesZeroLPTokenTrustLine(t *testing.T) {
	f := newBidCleanupFixture(t)
	ownerCountBefore := f.env.OwnerCount(f.env.Carol)

	if !bidCleanupDirectoryContains(t, f.env, f.env.Carol, f.lineKey.Key) ||
		!bidCleanupDirectoryContains(t, f.env, f.ammAccount, f.lineKey.Key) {
		t.Fatal("LP token line missing from an owner directory before AMMBid")
	}

	result := f.bidAll(t)

	if f.env.LedgerEntryExists(f.lineKey) {
		t.Fatal("zero LP token trust line still exists after AMMBid")
	}
	if got := f.env.OwnerCount(f.env.Carol); got != ownerCountBefore-1 {
		t.Fatalf("Carol OwnerCount = %d, want %d", got, ownerCountBefore-1)
	}
	if bidCleanupDirectoryContains(t, f.env, f.env.Carol, f.lineKey.Key) ||
		bidCleanupDirectoryContains(t, f.env, f.ammAccount, f.lineKey.Key) {
		t.Fatal("deleted LP token line remains in an owner directory")
	}

	lineNode := bidCleanupMetadataNode(result, "DeletedNode", f.lineKey)
	if lineNode == nil {
		t.Fatal("AMMBid metadata has no DeletedNode for the LP token line")
	}
	balance, ok := lineNode.FinalFields["Balance"].(map[string]any)
	if !ok || balance["value"] != "0" {
		t.Fatalf("deleted LP token balance = %#v, want 0", lineNode.FinalFields["Balance"])
	}
	requireBidCleanupReserveMetadata(t, lineNode,
		state.CompareAccountIDs(f.env.Carol.ID, f.ammAccount.ID) < 0)
	accountNode := bidCleanupMetadataNode(result, "ModifiedNode", keylet.Account(f.env.Carol.ID))
	if accountNode == nil || metadata.ToUint32(accountNode.FinalFields["OwnerCount"]) != ownerCountBefore-1 {
		t.Fatalf("Carol metadata does not record OwnerCount %d: %#v", ownerCountBefore-1, accountNode)
	}
}

func TestAMMBidDeletesSponsoredZeroLPTokenTrustLine(t *testing.T) {
	f := newBidCleanupFixture(t)
	f.env.EnableFeature("Sponsor")
	f.env.FundAmount(f.env.Bob, uint64(jtx.XRP(2_000)))
	f.env.Close()

	remaining := int32(1)
	set := sponsortx.NewSponsorshipSet(f.env.Bob.Address)
	set.Sponsee = f.env.Carol.Address
	set.RemainingOwnerCountDelta = &remaining
	if result := f.env.Submit(set); !result.Success {
		t.Fatalf("SponsorshipSet: %s - %s", result.Code, result.Message)
	}

	transfer := sponsortx.NewSponsorshipTransfer(f.env.Carol.Address)
	transfer.ObjectID = hex.EncodeToString(f.lineKey.Key[:])
	transfer.Sponsor = f.env.Bob.Address
	reserve := tx.SpfSponsorReserve
	transfer.SponsorFlags = &reserve
	transfer.SetFlags(sponsortx.SponsorshipTransferFlagCreate)
	if result := f.env.Submit(transfer); !result.Success {
		t.Fatalf("SponsorshipTransfer: %s - %s", result.Code, result.Message)
	}

	carolBefore := readBidCleanupAccount(t, f.env, f.env.Carol)
	bobBefore := readBidCleanupAccount(t, f.env, f.env.Bob)
	if carolBefore.SponsoredOwnerCount != 1 || bobBefore.SponsoringOwnerCount != 1 {
		t.Fatalf("sponsorship counters before bid = Carol %d, Bob %d, want 1/1",
			carolBefore.SponsoredOwnerCount, bobBefore.SponsoringOwnerCount)
	}

	result := f.bidAll(t)
	carolAfter := readBidCleanupAccount(t, f.env, f.env.Carol)
	bobAfter := readBidCleanupAccount(t, f.env, f.env.Bob)

	if f.env.LedgerEntryExists(f.lineKey) {
		t.Fatal("sponsored zero LP token trust line still exists after AMMBid")
	}
	if carolAfter.OwnerCount != carolBefore.OwnerCount-1 || carolAfter.SponsoredOwnerCount != 0 {
		t.Fatalf("Carol counts after bid = OwnerCount %d, SponsoredOwnerCount %d",
			carolAfter.OwnerCount, carolAfter.SponsoredOwnerCount)
	}
	if bobAfter.SponsoringOwnerCount != 0 {
		t.Fatalf("Bob SponsoringOwnerCount = %d, want 0", bobAfter.SponsoringOwnerCount)
	}

	carolNode := bidCleanupMetadataNode(result, "ModifiedNode", keylet.Account(f.env.Carol.ID))
	if carolNode == nil || metadata.ToUint32(carolNode.FinalFields["SponsoredOwnerCount"]) != 0 {
		t.Fatalf("Carol metadata does not clear SponsoredOwnerCount: %#v", carolNode)
	}
	bobNode := bidCleanupMetadataNode(result, "ModifiedNode", keylet.Account(f.env.Bob.ID))
	if bobNode == nil || metadata.ToUint32(bobNode.FinalFields["SponsoringOwnerCount"]) != 0 {
		t.Fatalf("Bob metadata does not clear SponsoringOwnerCount: %#v", bobNode)
	}
	lineNode := bidCleanupMetadataNode(result, "DeletedNode", f.lineKey)
	if lineNode == nil {
		t.Fatal("AMMBid metadata has no DeletedNode for the sponsored LP token line")
	}
	holderIsLow := state.CompareAccountIDs(f.env.Carol.ID, f.ammAccount.ID) < 0
	requireBidCleanupReserveMetadata(t, lineNode, holderIsLow)
	sponsorField := "HighSponsor"
	if holderIsLow {
		sponsorField = "LowSponsor"
	}
	if sponsor, present := lineNode.FinalFields[sponsorField]; present && sponsor != "" {
		t.Fatalf("FinalFields.%s = %v, want absent", sponsorField, sponsor)
	}
	if sponsor := lineNode.PreviousFields[sponsorField]; sponsor != f.env.Bob.Address {
		t.Fatalf("PreviousFields.%s = %v, want %s", sponsorField, sponsor, f.env.Bob.Address)
	}

	sponsorshipData, err := f.env.LedgerEntry(keylet.Sponsorship(f.env.Bob.ID, f.env.Carol.ID))
	if err != nil {
		t.Fatalf("read Sponsorship: %v", err)
	}
	sponsorship, err := state.ParseSponsorship(sponsorshipData)
	if err != nil {
		t.Fatalf("parse Sponsorship: %v", err)
	}
	if sponsorship.RemainingOwnerCount != 0 {
		t.Fatalf("RemainingOwnerCount = %d, want 0", sponsorship.RemainingOwnerCount)
	}
}

func TestAMMBidOutbidRecreatesRefundedLPTokenTrustLine(t *testing.T) {
	f := newBidCleanupFixture(t)
	ownerCountBefore := f.env.OwnerCount(f.env.Carol)
	f.bidAll(t)
	if f.env.LedgerEntryExists(f.lineKey) {
		t.Fatal("first bid did not delete Carol's zero LP token line")
	}

	refundBid := f.env.LPTokenAmountFromLedger(amm.XRP(), f.env.USD, 200)
	result := f.env.Submit(amm.AMMBid(f.env.Alice, amm.XRP(), f.env.USD).
		BidMin(refundBid).
		Build())
	if !result.Success {
		t.Fatalf("outbid: %s - %s", result.Code, result.Message)
	}

	if !f.env.LedgerEntryExists(f.lineKey) {
		t.Fatal("outbid refund did not recreate Carol's LP token line")
	}
	if got := f.env.OwnerCount(f.env.Carol); got != ownerCountBefore {
		t.Fatalf("Carol OwnerCount after refund = %d, want %d", got, ownerCountBefore)
	}
	if !bidCleanupDirectoryContains(t, f.env, f.env.Carol, f.lineKey.Key) ||
		!bidCleanupDirectoryContains(t, f.env, f.ammAccount, f.lineKey.Key) {
		t.Fatal("recreated LP token line missing from an owner directory")
	}
	balance := f.env.IOUBalance(f.env.Carol, f.ammAccount, f.amount.Currency)
	if balance == nil || balance.Signum() <= 0 {
		t.Fatalf("Carol refund balance = %v, want positive", balance)
	}
	if bidCleanupMetadataNode(result, "CreatedNode", f.lineKey) == nil {
		t.Fatal("outbid metadata has no CreatedNode for Carol's LP token line")
	}
}

func TestAMMBidPartialBurnPreservesLPTokenTrustLine(t *testing.T) {
	f := newBidCleanupFixture(t)
	ownerCountBefore := f.env.OwnerCount(f.env.Carol)
	partial := f.env.LPTokenAmountFromLedger(amm.XRP(), f.env.USD, 50)
	result := f.env.Submit(amm.AMMBid(f.env.Carol, amm.XRP(), f.env.USD).
		BidMin(partial).
		Build())
	if !result.Success {
		t.Fatalf("AMMBid: %s - %s", result.Code, result.Message)
	}

	if !f.env.LedgerEntryExists(f.lineKey) {
		t.Fatal("partial burn deleted Carol's LP token line")
	}
	if got := f.env.OwnerCount(f.env.Carol); got != ownerCountBefore {
		t.Fatalf("Carol OwnerCount = %d, want %d", got, ownerCountBefore)
	}
	balance := f.env.IOUBalance(f.env.Carol, f.ammAccount, f.amount.Currency)
	if balance == nil || balance.Signum() <= 0 {
		t.Fatalf("Carol LP balance = %v, want positive", balance)
	}
	if bidCleanupMetadataNode(result, "ModifiedNode", f.lineKey) == nil {
		t.Fatal("partial burn metadata has no ModifiedNode for the LP token line")
	}
}

func TestAMMBidDirectoryFailureRollsBackCleanup(t *testing.T) {
	f := newBidCleanupFixture(t)
	lineData, err := f.env.LedgerEntry(f.lineKey)
	if err != nil {
		t.Fatalf("read LP token line: %v", err)
	}
	line, err := state.ParseRippleState(lineData)
	if err != nil {
		t.Fatalf("parse LP token line: %v", err)
	}
	line.HighNode = 1
	line.HasHighNode = true
	corruptLine, err := state.SerializeRippleState(line)
	if err != nil {
		t.Fatalf("serialize corrupt LP token line: %v", err)
	}
	if err := f.env.Ledger().Update(f.lineKey, corruptLine); err != nil {
		t.Fatalf("write corrupt LP token line: %v", err)
	}

	lowID := f.env.Carol.ID
	if state.CompareAccountIDs(f.env.Carol.ID, f.ammAccount.ID) > 0 {
		lowID = f.ammAccount.ID
	}
	lowDirKey := keylet.OwnerDir(lowID)
	lowDirBefore, err := f.env.LedgerEntry(lowDirKey)
	if err != nil {
		t.Fatalf("read low owner directory: %v", err)
	}
	carolBefore := readBidCleanupAccount(t, f.env, f.env.Carol)

	result := f.env.Submit(amm.AMMBid(f.env.Carol, amm.XRP(), f.env.USD).
		BidMin(f.amount).
		Build())
	if result.Code != "tefBAD_LEDGER" || result.Applied {
		t.Fatalf("AMMBid result = %s, applied=%v, want tefBAD_LEDGER/false", result.Code, result.Applied)
	}

	lineAfter, err := f.env.LedgerEntry(f.lineKey)
	if err != nil {
		t.Fatalf("read LP token line after failure: %v", err)
	}
	if !bytes.Equal(lineAfter, corruptLine) {
		t.Fatal("failed AMMBid changed the LP token line")
	}
	lowDirAfter, err := f.env.LedgerEntry(lowDirKey)
	if err != nil {
		t.Fatalf("read low owner directory after failure: %v", err)
	}
	if !bytes.Equal(lowDirAfter, lowDirBefore) {
		t.Fatal("failed AMMBid changed the low owner directory")
	}
	carolAfter := readBidCleanupAccount(t, f.env, f.env.Carol)
	if carolAfter.OwnerCount != carolBefore.OwnerCount || carolAfter.SponsoredOwnerCount != carolBefore.SponsoredOwnerCount {
		t.Fatalf("failed AMMBid changed Carol counts from %d/%d to %d/%d",
			carolBefore.OwnerCount, carolBefore.SponsoredOwnerCount,
			carolAfter.OwnerCount, carolAfter.SponsoredOwnerCount)
	}
}

func TestAMMBidPreservesZeroLPTokenTrustLineWithLimit(t *testing.T) {
	f := newBidCleanupFixture(t)
	limit := tx.NewIssuedAmountFromFloat64(1, f.amount.Currency, f.ammAccount.Address)
	if result := f.env.Submit(trustset.TrustSet(f.env.Carol, limit).Build()); !result.Success {
		t.Fatalf("TrustSet LP limit: %s - %s", result.Code, result.Message)
	}
	f.env.Close()
	ownerCountBefore := f.env.OwnerCount(f.env.Carol)

	result := f.bidAll(t)

	if !f.env.LedgerEntryExists(f.lineKey) {
		t.Fatal("AMMBid deleted an LP token trust line with a non-zero holder limit")
	}
	if got := f.env.OwnerCount(f.env.Carol); got != ownerCountBefore {
		t.Fatalf("Carol OwnerCount = %d, want %d", got, ownerCountBefore)
	}
	if !bidCleanupDirectoryContains(t, f.env, f.env.Carol, f.lineKey.Key) ||
		!bidCleanupDirectoryContains(t, f.env, f.ammAccount, f.lineKey.Key) {
		t.Fatal("preserved LP token line missing from an owner directory")
	}

	lineData, err := f.env.LedgerEntry(f.lineKey)
	if err != nil {
		t.Fatalf("read LP token line: %v", err)
	}
	line, err := state.ParseRippleState(lineData)
	if err != nil {
		t.Fatalf("parse LP token line: %v", err)
	}
	if !line.Balance.IsZero() {
		t.Fatalf("LP token balance = %s, want 0", line.Balance.Value())
	}
	holderIsLow := state.CompareAccountIDs(f.env.Carol.ID, f.ammAccount.ID) < 0
	if holderIsLow && line.LowLimit.IsZero() || !holderIsLow && line.HighLimit.IsZero() {
		t.Fatal("holder limit was not preserved")
	}
	if bidCleanupMetadataNode(result, "DeletedNode", f.lineKey) != nil ||
		bidCleanupMetadataNode(result, "ModifiedNode", f.lineKey) == nil {
		t.Fatal("LP token line metadata must be modified, not deleted")
	}
}
