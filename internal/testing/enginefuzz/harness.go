package enginefuzz

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

const profileV320Fingerprint = "0ea1cc59ad0073ad27127e5018e56476cdb2ddb29fe18ca6f4364eafd16593fa"

type executionRecord struct {
	Step   traceStep
	Result jtx.TxResult
}

type runReport struct {
	Records          []executionRecord
	Kinds            map[txKind]int
	Applied          map[txKind]int
	InvariantChecks  int
	TransactionCalls int
	Closes           int
}

type scenario struct {
	t              testing.TB
	env            *jtx.TestEnv
	gw             *jtx.Account
	users          []*jtx.Account
	currencies     []string
	profile        amendmentProfile
	expectedSupply uint64
}

func newScenario(t testing.TB, profile amendmentProfile) *scenario {
	t.Helper()
	env := jtx.NewTestEnv(t)
	applyAmendmentProfile(t, env, profile)

	gw := jtx.NewAccount("enginefuzz-gw")
	users := []*jtx.Account{
		jtx.NewAccount("enginefuzz-alice"),
		jtx.NewAccount("enginefuzz-bob"),
		jtx.NewAccount("enginefuzz-carol"),
		jtx.NewAccount("enginefuzz-dave"),
	}
	currencies := []string{"USD", "EUR"}

	env.FundAmount(gw, uint64(jtx.XRP(1_000_000)))
	for _, user := range users {
		env.FundAmount(user, uint64(jtx.XRP(1_000_000)))
	}
	env.Close()
	for _, user := range users {
		for _, currency := range currencies {
			env.Trust(user, tx.NewIssuedAmountFromFloat64(1_000_000_000, currency, gw.Address))
			env.PayIOU(gw, user, gw, currency, 1_000_000)
		}
	}
	env.Close()

	sc := &scenario{t: t, env: env, gw: gw, users: users, currencies: currencies, profile: profile}
	total, err := sc.totalXRP()
	if err != nil {
		t.Fatalf("profile=%s initial XRP total: %v", profile, err)
	}
	sc.expectedSupply = total
	if got := env.LastClosedLedger().TotalDrops(); got != total {
		t.Fatalf("profile=%s seeded supply mismatch: ledger=%d state=%d", profile, got, total)
	}
	return sc
}

func applyAmendmentProfile(t testing.TB, env *jtx.TestEnv, profile amendmentProfile) {
	t.Helper()
	if profile != profileV320 {
		t.Fatalf("unknown amendment profile %d", profile)
	}
	features := amendment.SupportedFeatures()
	names := make([]string, len(features))
	for i, feature := range features {
		names[i] = feature.Name
	}
	sort.Strings(names)
	digest := sha256.Sum256([]byte(strings.Join(names, "\n")))
	got := hex.EncodeToString(digest[:])
	if got != profileV320Fingerprint {
		t.Fatalf("amendment profile %s changed: got fingerprint %s", profile, got)
	}
	env.SetAmendments(names)
	env.Close()
}

func runTrace(t testing.TB, tr trace) runReport {
	t.Helper()
	_, report := executeTrace(t, tr)
	return report
}

func executeTrace(t testing.TB, tr trace) (*scenario, runReport) {
	t.Helper()
	sc := newScenario(t, tr.Profile)
	report := runReport{Kinds: make(map[txKind]int), Applied: make(map[txKind]int)}
	for i, step := range tr.Steps {
		record, err := sc.executeStep(i, step)
		if err != nil {
			t.Fatal(err)
		}
		report.Records = append(report.Records, record)
		report.Kinds[step.Kind]++
		if record.Result.Applied {
			report.Applied[step.Kind]++
		}
		if record.Result.ApplyInvoked {
			report.TransactionCalls++
		}
		if record.Result.InvariantsChecked {
			report.InvariantChecks++
		}
		if step.CloseAfter {
			sc.closeAndCheck(i)
			report.Closes++
		}
	}
	sc.closeAndCheck(len(tr.Steps))
	report.Closes++
	return sc, report
}

func (sc *scenario) executeStep(index int, step traceStep) (executionRecord, error) {
	built := sc.build(step)
	result, err := sc.submitAndCheck(index, step.String(), sc.accountFor(step.From), built, tx.TapNONE, func(result jtx.TxResult) error {
		return classifyOutcome(step, result, sc.profile, index)
	})
	if err != nil {
		return executionRecord{}, err
	}
	return executionRecord{Step: step, Result: result}, nil
}

func (sc *scenario) submitAndCheck(
	index int,
	description string,
	payer *jtx.Account,
	built tx.Transaction,
	flags tx.ApplyFlags,
	classify func(jtx.TxResult) error,
) (jtx.TxResult, error) {
	beforeHash, err := sc.env.Ledger().StateMapHash()
	if err != nil {
		return jtx.TxResult{}, fmt.Errorf("step %d %s profile=%s: state hash before submit: %w", index, description, sc.profile, err)
	}
	beforeTotal, err := sc.totalXRP()
	if err != nil {
		return jtx.TxResult{}, fmt.Errorf("step %d %s profile=%s: XRP total before submit: %w", index, description, sc.profile, err)
	}
	beforeInfo := sc.env.AccountInfo(payer)
	result := sc.env.SubmitWithFlags(built, flags)
	if err := classify(result); err != nil {
		return jtx.TxResult{}, err
	}
	afterHash, err := sc.env.Ledger().StateMapHash()
	if err != nil {
		return jtx.TxResult{}, fmt.Errorf("step %d %s profile=%s: state hash after submit: %w", index, description, sc.profile, err)
	}
	afterTotal, err := sc.totalXRP()
	if err != nil {
		return jtx.TxResult{}, fmt.Errorf("step %d %s profile=%s: XRP total after submit: %w", index, description, sc.profile, err)
	}
	afterInfo := sc.env.AccountInfo(payer)
	if result.Applied {
		if beforeTotal < result.Fee || afterTotal != beforeTotal-result.Fee {
			return jtx.TxResult{}, fmt.Errorf("step %d %s profile=%s result=%s: XRP total %d -> %d, charged fee %d", index, description, sc.profile, result.Code, beforeTotal, afterTotal, result.Fee)
		}
		if result.Metadata == nil || result.Metadata.TransactionResult != result.Result {
			return jtx.TxResult{}, fmt.Errorf("step %d %s profile=%s result=%s: applied result has missing or inconsistent metadata", index, description, sc.profile, result.Code)
		}
		if !result.InvariantsChecked {
			return jtx.TxResult{}, fmt.Errorf("step %d %s profile=%s result=%s: applied result skipped invariants", index, description, sc.profile, result.Code)
		}
		sc.expectedSupply -= result.Fee
		if beforeInfo != nil && afterInfo != nil && afterInfo.Sequence != beforeInfo.Sequence+1 {
			return jtx.TxResult{}, fmt.Errorf("step %d %s profile=%s result=%s: sequence %d -> %d", index, description, sc.profile, result.Code, beforeInfo.Sequence, afterInfo.Sequence)
		}
	} else {
		if beforeHash != afterHash || beforeTotal != afterTotal {
			return jtx.TxResult{}, fmt.Errorf("step %d %s profile=%s result=%s: rejected transaction mutated state or XRP", index, description, sc.profile, result.Code)
		}
		if result.Fee != 0 || result.Metadata != nil {
			return jtx.TxResult{}, fmt.Errorf("step %d %s profile=%s result=%s: rejected transaction reported fee=%d metadata=%t", index, description, sc.profile, result.Code, result.Fee, result.Metadata != nil)
		}
		if beforeInfo != nil && afterInfo != nil && afterInfo.Sequence != beforeInfo.Sequence {
			return jtx.TxResult{}, fmt.Errorf("step %d %s profile=%s result=%s: rejected transaction changed sequence %d -> %d", index, description, sc.profile, result.Code, beforeInfo.Sequence, afterInfo.Sequence)
		}
	}
	return result, nil
}

func (sc *scenario) closeAndCheck(index int) {
	sc.t.Helper()
	sc.env.Close()
	total, err := sc.totalXRP()
	if err != nil {
		sc.t.Fatalf("close after step %d profile=%s: XRP total: %v", index, sc.profile, err)
	}
	if total != sc.expectedSupply {
		sc.t.Fatalf("close after step %d profile=%s: state supply=%d expected=%d", index, sc.profile, total, sc.expectedSupply)
	}
	if got := sc.env.LastClosedLedger().TotalDrops(); got != sc.expectedSupply {
		sc.t.Fatalf("close after step %d profile=%s: ledger supply=%d expected=%d", index, sc.profile, got, sc.expectedSupply)
	}
	if _, err := sc.env.LastClosedLedger().StateMapHash(); err != nil {
		sc.t.Fatalf("close after step %d profile=%s: state hash: %v", index, sc.profile, err)
	}
}

func (sc *scenario) totalXRP() (uint64, error) {
	var total uint64
	var sumErr error
	err := sc.env.Ledger().ForEach(func(_ [32]byte, data []byte) bool {
		var amount uint64
		switch entry.Type(state.EntryTypeCode(data)) {
		case entry.TypeAccountRoot:
			account, err := state.ParseAccountRoot(data)
			if err != nil {
				sumErr = fmt.Errorf("parse AccountRoot: %w", err)
				return false
			}
			amount = account.Balance
		case entry.TypeEscrow:
			escrow, err := state.ParseEscrow(data)
			if err != nil {
				sumErr = fmt.Errorf("parse Escrow: %w", err)
				return false
			}
			if escrow.IsXRP {
				amount = escrow.Amount
			}
		case entry.TypePayChannel:
			channel, err := state.ParsePayChannel(data)
			if err != nil {
				sumErr = fmt.Errorf("parse PayChannel: %w", err)
				return false
			}
			if channel.Balance > channel.Amount {
				sumErr = fmt.Errorf("PayChannel balance %d exceeds amount %d", channel.Balance, channel.Amount)
				return false
			}
			amount = channel.Amount - channel.Balance
		}
		total, sumErr = addXRP(total, amount)
		return sumErr == nil
	})
	if err != nil {
		return 0, err
	}
	if sumErr != nil {
		return 0, sumErr
	}
	return total, nil
}

func (sc *scenario) build(step traceStep) tx.Transaction {
	from, to := sc.pair(step.From, step.To)
	switch step.Kind {
	case kindPaymentXRP:
		builder := payment.Pay(from, to, step.Amount)
		if step.Option&1 != 0 {
			builder = builder.DestTag(uint32(step.Option))
		}
		return builder.Build()
	case kindPaymentIOU:
		amount := tx.NewIssuedAmountFromFloat64(float64(step.Amount)/1e6, sc.currencies[int(step.Currency)%len(sc.currencies)], sc.gw.Address)
		builder := payment.PayIssued(from, to, amount)
		if step.Option&1 != 0 {
			builder = builder.PartialPayment()
		}
		return builder.Build()
	case kindAccountSet:
		builder := accountset.AccountSet(from)
		switch step.Option % 8 {
		case 0:
			builder = builder.RequireDest()
		case 1:
			builder = builder.ClearFlag(accounttx.AccountSetFlagRequireDest)
		case 2:
			builder = builder.DefaultRipple()
		case 3:
			builder = builder.DepositAuth()
		case 4:
			builder = builder.NoFreeze()
		case 5:
			builder = builder.GlobalFreeze()
		case 6:
			builder = builder.DisallowXRP()
		}
		return builder.Build()
	case kindTrustSet:
		builder := trustset.TrustLine(from, sc.currencies[int(step.Currency)%len(sc.currencies)], sc.gw, fmt.Sprintf("%d", step.Limit))
		switch step.Option % 4 {
		case 1:
			builder = builder.NoRipple()
		case 2:
			builder = builder.Freeze()
		case 3:
			builder = builder.ClearFreeze()
		}
		return builder.Build()
	case kindOfferCreate:
		amount := tx.NewIssuedAmountFromFloat64(float64(step.Limit)/1e3, sc.currencies[int(step.Currency)%len(sc.currencies)], sc.gw.Address)
		builder := offer.OfferCreateXRP(from, step.Amount, amount, step.Option&1 != 0)
		switch (step.Option >> 1) % 5 {
		case 1:
			builder = builder.Passive()
		case 2:
			builder = builder.ImmediateOrCancel()
		case 3:
			builder = builder.FillOrKill()
		case 4:
			builder = builder.Sell()
		}
		return builder.Build()
	case kindOfferCancel:
		offerSequence := step.Offer
		if offerSequence == 0 {
			info := sc.env.AccountInfo(from)
			if info != nil && info.Sequence > 1 {
				offerSequence = info.Sequence - 1
			}
		}
		return offer.OfferCancel(from, offerSequence).Build()
	default:
		panic(fmt.Sprintf("unsupported transaction kind %d", step.Kind))
	}
}

func (sc *scenario) accountFor(index uint8) *jtx.Account {
	return sc.users[int(index)%len(sc.users)]
}

func (sc *scenario) pair(fromIndex, toIndex uint8) (*jtx.Account, *jtx.Account) {
	from := int(fromIndex) % len(sc.users)
	to := int(toIndex) % len(sc.users)
	if from == to {
		to = (to + 1) % len(sc.users)
	}
	return sc.users[from], sc.users[to]
}
