package enginefuzz

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

const (
	maxPhaseSteps = 24
	phaseStepSize = 4
)

type phaseKind uint8

const (
	phaseExplicitFee phaseKind = iota
	phaseSequence
	phaseTicketAndSequence
	phaseNetworkID
	phaseSigningKey
	phaseLastLedger
	phaseAccountTxnID
	phaseDelegate
	phaseApplyNone
	phaseApplyRetry
	phaseApplyFailHard
	numPhaseKinds
)

func (k phaseKind) String() string {
	switch k {
	case phaseExplicitFee:
		return "explicit-fee"
	case phaseSequence:
		return "sequence"
	case phaseTicketAndSequence:
		return "ticket-and-sequence"
	case phaseNetworkID:
		return "network-id"
	case phaseSigningKey:
		return "signing-key"
	case phaseLastLedger:
		return "last-ledger"
	case phaseAccountTxnID:
		return "account-txn-id"
	case phaseDelegate:
		return "delegate"
	case phaseApplyNone:
		return "apply-none"
	case phaseApplyRetry:
		return "apply-retry"
	case phaseApplyFailHard:
		return "apply-fail-hard"
	default:
		return fmt.Sprintf("unknown-%d", k)
	}
}

type phaseStep struct {
	Kind       phaseKind
	Actor      uint8
	Option     uint8
	CloseAfter bool
	InputStart int
	InputEnd   int
}

func (s phaseStep) String() string {
	return fmt.Sprintf("phase=%s actor=%d option=%d close=%t input=[%d,%d)", s.Kind, s.Actor, s.Option, s.CloseAfter, s.InputStart, s.InputEnd)
}

type phaseTrace struct {
	Profile amendmentProfile
	Steps   []phaseStep
}

func decodePhaseTrace(data []byte) phaseTrace {
	s := &stream{data: data}
	tr := phaseTrace{Profile: profileV320}
	if s.drained() {
		return tr
	}
	tr.Profile = amendmentProfile(s.index(1))
	for len(tr.Steps) < maxPhaseSteps && !s.drained() {
		start := s.offset()
		step := phaseStep{
			Kind:       phaseKind(s.index(int(numPhaseKinds))),
			Actor:      uint8(s.index(4)),
			Option:     s.u8(),
			CloseAfter: s.chance(32),
			InputStart: start,
			InputEnd:   s.offset(),
		}
		tr.Steps = append(tr.Steps, step)
	}
	return tr
}

func encodePhaseTrace(tr phaseTrace) []byte {
	data := []byte{byte(tr.Profile)}
	for _, step := range tr.Steps {
		buf := make([]byte, phaseStepSize)
		buf[0] = byte(step.Kind)
		buf[1] = step.Actor
		buf[2] = step.Option
		buf[3] = 255
		if step.CloseAfter {
			buf[3] = 0
		}
		data = append(data, buf...)
	}
	return data
}

type phaseReport struct {
	Kinds            map[phaseKind]int
	Applied          int
	Rejected         int
	TransactionCalls int
	InvariantChecks  int
	Closes           int
}

func runPhaseTrace(t testing.TB, tr phaseTrace) phaseReport {
	t.Helper()
	sc := newScenario(t, tr.Profile)
	report := phaseReport{Kinds: make(map[phaseKind]int)}
	for i, step := range tr.Steps {
		payer, transaction, flags := sc.buildPhase(step)
		result, err := sc.submitAndCheck(i, step.String(), payer, transaction, flags, func(result jtx.TxResult) error {
			return classifySafetyOutcome(step.String(), result, tr.Profile, i)
		})
		if err != nil {
			t.Fatal(err)
		}
		report.Kinds[step.Kind]++
		if result.Applied {
			report.Applied++
		} else {
			report.Rejected++
		}
		if result.ApplyInvoked {
			report.TransactionCalls++
		}
		if result.InvariantsChecked {
			report.InvariantChecks++
		}
		if step.CloseAfter {
			sc.closeAndCheck(i)
			report.Closes++
		}
	}
	sc.closeAndCheck(len(tr.Steps))
	report.Closes++
	return report
}

func (sc *scenario) buildPhase(step phaseStep) (*jtx.Account, tx.Transaction, tx.ApplyFlags) {
	payer := sc.accountFor(step.Actor)
	destination := sc.accountFor(step.Actor + 1)
	flags := tx.TapNONE
	var transaction tx.Transaction

	if step.Kind >= phaseApplyNone {
		ghost := jtx.NewAccount("enginefuzz-unfunded-destination")
		transaction = payment.Pay(payer, ghost, 1).Build()
		switch step.Kind {
		case phaseApplyRetry:
			flags = tx.TapRETRY
		case phaseApplyFailHard:
			flags = tx.TapFAIL_HARD
		}
		return payer, transaction, flags
	}

	transaction = payment.Pay(payer, destination, uint64(jtx.XRP(1))).Build()
	common := transaction.GetCommon()
	info := sc.env.AccountInfo(payer)
	switch step.Kind {
	case phaseExplicitFee:
		switch step.Option % 4 {
		case 0:
			common.Fee = "0"
		case 1:
			common.Fee = "10"
		case 2:
			common.Fee = "-1"
		case 3:
			common.Fee = "not-a-number"
		}
	case phaseSequence:
		sequence := info.Sequence
		switch step.Option % 3 {
		case 0:
			if sequence > 0 {
				sequence--
			}
		case 1:
			sequence++
		}
		common.Sequence = &sequence
	case phaseTicketAndSequence:
		sequence := info.Sequence
		ticket := sequence + 1
		common.Sequence = &sequence
		common.TicketSequence = &ticket
	case phaseNetworkID:
		networkID := uint32(step.Option)
		common.NetworkID = &networkID
	case phaseSigningKey:
		if step.Option&1 == 0 {
			common.SigningPubKey = "00"
		} else {
			common.SigningPubKey = "ABC"
		}
	case phaseLastLedger:
		ledgerSequence := sc.env.LedgerSeq()
		if step.Option&1 == 0 && ledgerSequence > 0 {
			ledgerSequence--
		} else {
			ledgerSequence++
		}
		common.LastLedgerSequence = &ledgerSequence
	case phaseAccountTxnID:
		if step.Option&1 == 0 {
			common.AccountTxnID = "00"
		} else {
			common.AccountTxnID = fmt.Sprintf("%064x", step.Option)
		}
	case phaseDelegate:
		if step.Option&1 == 0 {
			common.Delegate = payer.Address
		} else {
			common.Delegate = destination.Address
		}
	}
	return payer, transaction, flags
}

func phaseSeedCorpus() []struct {
	Name  string
	Trace phaseTrace
} {
	steps := make([]phaseStep, 0, int(numPhaseKinds)+5)
	for kind := phaseKind(0); kind < numPhaseKinds; kind++ {
		steps = append(steps, phaseStep{Kind: kind, Actor: uint8(kind % 4), Option: uint8(kind)})
	}
	steps = append(steps,
		phaseStep{Kind: phaseExplicitFee, Actor: 0, Option: 1},
		phaseStep{Kind: phaseSequence, Actor: 1, Option: 2},
		phaseStep{Kind: phaseLastLedger, Actor: 2, Option: 1, CloseAfter: true},
	)
	return []struct {
		Name  string
		Trace phaseTrace
	}{{Name: "common-fields-and-apply-flags", Trace: phaseTrace{Profile: profileV320, Steps: steps}}}
}
