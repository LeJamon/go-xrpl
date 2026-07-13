package engine

import (
	"reflect"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

type rulesAwareDispatchProbe struct {
	*txcore.BaseTx
	calls *[]string
}

func (p *rulesAwareDispatchProbe) Validate() error {
	*p.calls = append(*p.calls, "Validate")
	return ter.Errorf(ter.TemMALFORMED, "legacy Validate called")
}

func (p *rulesAwareDispatchProbe) PreflightRules(*amendment.Rules) error {
	*p.calls = append(*p.calls, "PreflightRules")
	return ter.Errorf(ter.TemMALFORMED, "legacy PreflightRules called")
}

func (p *rulesAwareDispatchProbe) PreflightWithRules(*amendment.Rules) error {
	*p.calls = append(*p.calls, "PreflightWithRules")
	return nil
}

type legacyDispatchProbe struct {
	*txcore.BaseTx
	calls *[]string
}

func (p *legacyDispatchProbe) Validate() error {
	*p.calls = append(*p.calls, "Validate")
	return nil
}

func (p *legacyDispatchProbe) PreflightRules(*amendment.Rules) error {
	*p.calls = append(*p.calls, "PreflightRules")
	return nil
}

func dispatchProbeBase(outer bool) *txcore.BaseTx {
	base := txcore.NewBaseTx(txcore.TypeAccountSet, precedenceSourceAddr)
	if outer {
		base.Fee = "10"
		base.Sequence = u32(5)
	}
	return base
}

func TestRulesAwarePreflightDispatch(t *testing.T) {
	e := preflightEngine(allRules())
	tests := []struct {
		name string
		run  func(txcore.Transaction) ter.Result
	}{
		{name: "outer", run: e.preflight},
		{name: "batch inner", run: e.preflightInner},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := []string{}
			probe := &rulesAwareDispatchProbe{
				BaseTx: dispatchProbeBase(tc.name == "outer"),
				calls:  &calls,
			}
			if got := tc.run(probe); got != ter.TesSUCCESS {
				t.Fatalf("preflight = %v, want TesSUCCESS", got)
			}
			want := []string{"PreflightWithRules"}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls = %v, want %v", calls, want)
			}
		})
	}
}

func TestLegacyPreflightDispatchOrdering(t *testing.T) {
	e := preflightEngine(allRules())
	tests := []struct {
		name string
		run  func(txcore.Transaction) ter.Result
	}{
		{name: "outer", run: e.preflight},
		{name: "batch inner", run: e.preflightInner},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := []string{}
			probe := &legacyDispatchProbe{
				BaseTx: dispatchProbeBase(tc.name == "outer"),
				calls:  &calls,
			}
			if got := tc.run(probe); got != ter.TesSUCCESS {
				t.Fatalf("preflight = %v, want TesSUCCESS", got)
			}
			want := []string{"Validate", "PreflightRules"}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls = %v, want %v", calls, want)
			}
		})
	}
}
