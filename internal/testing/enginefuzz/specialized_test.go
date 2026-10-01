package enginefuzz

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func assertSpecializedReachability(t testing.TB, state *specializedState) {
	t.Helper()
	if state.report.Applied == 0 || state.report.Closes == 0 ||
		(state.report.PseudoApplied == 0 && (state.report.TransactionCalls == 0 || state.report.InvariantChecks == 0)) {
		t.Fatalf("specialized trace lacks applied/phase/close reachability: %+v", *state.report)
	}
}

func TestEngineSpecializedStatefulCorpus(t *testing.T) {
	for _, seed := range specializedSeedCorpus() {
		t.Run(seed.Name, func(t *testing.T) {
			input := decodeSpecializedInput(encodeSpecializedInput(seed.Input))
			state := runSpecializedVariant(t, input)
			assertSpecializedReachability(t, state)
			t.Logf("specialized profile=%s family=%d amount=%d steps=%d applied=%d feeClaims=%d calls=%d invariants=%d closes=%d pseudo=%d", state.profile, input.Family, input.Amount, state.report.Steps, state.report.Applied, state.report.FeeClaims, state.report.TransactionCalls, state.report.InvariantChecks, state.report.Closes, state.report.PseudoApplied)
		})
	}
}

func FuzzEngineSpecializedStateful(f *testing.F) {
	for _, seed := range specializedSeedCorpus() {
		f.Add(encodeSpecializedInput(seed.Input))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		state := runSpecializedVariant(t, decodeSpecializedInput(data))
		assertSpecializedReachability(t, state)
	})
}

func TestEngineSpecializedSigningBoundary(t *testing.T) {
	s, accounts := accountSetUp(t, profileV341, "signing-boundary", "signing-alice", "signing-bob")
	alice, bob := accounts[0], accounts[1]
	s.establishBaseline()

	wrongSigner := payment.Pay(alice, bob, uint64(jtx.XRP(1))).Build()
	wrongHash, err := s.env.Ledger().StateMapHash()
	if err != nil {
		t.Fatalf("wrong-signature state hash before: %v", err)
	}
	wrongAliceBalance, wrongBobBalance := s.env.Balance(alice), s.env.Balance(bob)
	wrongAliceSequence := s.env.Seq(alice)
	wrongResult := s.env.SubmitSignedWith(wrongSigner, bob)
	wrongAfterHash, err := s.env.Ledger().StateMapHash()
	if err != nil {
		t.Fatalf("wrong-signature state hash after: %v", err)
	}
	if wrongResult.Result != ter.TefBAD_AUTH || wrongResult.Applied || wrongResult.Fee != 0 || wrongResult.Metadata != nil ||
		wrongResult.TransactionCalls != 0 || wrongResult.InvariantChecks != 0 || wrongAfterHash != wrongHash ||
		s.env.Balance(alice) != wrongAliceBalance || s.env.Balance(bob) != wrongBobBalance || s.env.Seq(alice) != wrongAliceSequence {
		t.Fatalf("wrong-signature boundary result=%+v", wrongResult)
	}

	valid := payment.Pay(alice, bob, uint64(jtx.XRP(1))).Build()
	validAliceBalance, validBobBalance := s.env.Balance(alice), s.env.Balance(bob)
	validResult := s.env.SubmitSigned(valid)
	if validResult.Result != ter.TesSUCCESS || !validResult.Applied || validResult.Metadata == nil {
		t.Fatalf("valid-signature boundary result=%+v", validResult)
	}
	if validResult.TransactionCalls != 1 || validResult.InvariantChecks != 1 ||
		s.env.Balance(alice) != validAliceBalance-uint64(jtx.XRP(1))-validResult.Fee ||
		s.env.Balance(bob) != validBobBalance+uint64(jtx.XRP(1)) {
		t.Fatalf("valid-signature boundary state result=%+v alice=%d bob=%d", validResult, s.env.Balance(alice), s.env.Balance(bob))
	}
	s.expectedSupply -= validResult.Fee
	s.close()
	if s.env.Balance(alice) != validAliceBalance-uint64(jtx.XRP(1))-validResult.Fee ||
		s.env.Balance(bob) != validBobBalance+uint64(jtx.XRP(1)) {
		t.Fatalf("valid-signature boundary closed state alice=%d bob=%d", s.env.Balance(alice), s.env.Balance(bob))
	}
}

func TestEngineSpecializedTecRecoveryAndRetry(t *testing.T) {
	s, accounts := accountSetUp(t, profileV341, "tec-recovery-retry", "tec-source", "tec-funded")
	source := accounts[0]
	s.establishBaseline()
	ghost := jtx.NewAccount("tec-unfunded-destination")
	beforeInfo := s.env.AccountInfo(source)
	beforeTotal, err := totalLedgerXRP(s.env)
	if err != nil {
		t.Fatalf("tec recovery initial XRP total: %v", err)
	}
	claimedSourceBalance, claimedSequence := s.env.Balance(source), s.env.Seq(source)
	claimedHash, err := s.env.Ledger().StateMapHash()
	if err != nil {
		t.Fatalf("tec recovery state hash before: %v", err)
	}
	claimed := s.env.SubmitWithFlags(payment.Pay(source, ghost, 1).Build(), tx.TapNONE)
	claimedAfterHash, err := s.env.Ledger().StateMapHash()
	if err != nil {
		t.Fatalf("tec recovery state hash after: %v", err)
	}
	if claimed.Result != ter.TecNO_DST_INSUF_XRP || !claimed.Applied || claimed.Fee == 0 || claimed.ApplyInvoked || !claimed.InvariantsChecked ||
		claimed.Metadata == nil || claimed.TransactionCalls != 0 || claimed.InvariantChecks != 1 || claimedAfterHash == claimedHash {
		t.Fatalf("fee-claiming tec result=%+v", claimed)
	}
	afterInfo := s.env.AccountInfo(source)
	afterTotal, err := totalLedgerXRP(s.env)
	if err != nil {
		t.Fatalf("tec recovery final XRP total: %v", err)
	}
	if beforeInfo == nil || afterInfo == nil || afterInfo.Sequence != beforeInfo.Sequence+1 || claimedSequence+1 != afterInfo.Sequence ||
		s.env.Balance(source) != claimedSourceBalance-claimed.Fee || afterTotal != beforeTotal-claimed.Fee {
		t.Fatalf("fee-claiming tec state source=%+v -> %+v total=%d -> %d fee=%d", beforeInfo, afterInfo, beforeTotal, afterTotal, claimed.Fee)
	}
	if s.env.Exists(ghost) {
		t.Fatal("fee-claiming tec created an unfunded destination")
	}
	s.expectedSupply -= claimed.Fee
	s.close()

	retrySource := jtx.NewAccount("retry-source")
	retryDestination := jtx.NewAccount("retry-destination")
	s.env.FundAmount(retrySource, uint64(jtx.XRP(30_000)))
	s.env.FundAmount(retryDestination, uint64(jtx.XRP(30_000)))
	s.env.Close()
	s.establishBaseline()
	retry := payment.Pay(retrySource, retryDestination, uint64(jtx.XRP(30_000))).Build()
	firstHash, err := s.env.Ledger().StateMapHash()
	if err != nil {
		t.Fatalf("retry state hash before failed attempt: %v", err)
	}
	firstSourceBalance, firstDestinationBalance := s.env.Balance(retrySource), s.env.Balance(retryDestination)
	firstSequence := s.env.Seq(retrySource)
	first := s.env.SubmitWithFlags(retry, tx.TapRETRY)
	firstAfterHash, err := s.env.Ledger().StateMapHash()
	if err != nil {
		t.Fatalf("retry state hash after failed attempt: %v", err)
	}
	if first.Result != ter.TecUNFUNDED_PAYMENT || first.Applied || first.Fee != 0 || first.Metadata != nil ||
		!first.ApplyInvoked || first.InvariantsChecked || first.TransactionCalls != 1 || first.InvariantChecks != 0 || firstAfterHash != firstHash ||
		s.env.Balance(retrySource) != firstSourceBalance || s.env.Balance(retryDestination) != firstDestinationBalance || s.env.Seq(retrySource) != firstSequence {
		t.Fatalf("retryable failed attempt=%+v", first)
	}
	s.env.Pay(retrySource, uint64(jtx.XRP(10_000)))
	s.expectedSupply -= s.env.BaseFee()
	secondSourceBalance, secondDestinationBalance := s.env.Balance(retrySource), s.env.Balance(retryDestination)
	second := s.env.SubmitWithFlags(retry, tx.TapNONE)
	if second.Result != ter.TesSUCCESS || !second.Applied || second.Fee == 0 || second.Metadata == nil ||
		second.TransactionCalls != 1 || second.InvariantChecks != 1 ||
		s.env.Balance(retrySource) != secondSourceBalance-uint64(jtx.XRP(30_000))-second.Fee ||
		s.env.Balance(retryDestination) != secondDestinationBalance+uint64(jtx.XRP(30_000)) {
		t.Fatalf("retry successful attempt=%+v", second)
	}
	s.expectedSupply -= second.Fee
	s.close()
	if s.env.Balance(retrySource) != secondSourceBalance-uint64(jtx.XRP(30_000))-second.Fee ||
		s.env.Balance(retryDestination) != secondDestinationBalance+uint64(jtx.XRP(30_000)) {
		t.Fatalf("retry closed state source=%d destination=%d", s.env.Balance(retrySource), s.env.Balance(retryDestination))
	}
}
