package engine

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

type rulesAwareValidationTx struct {
	*txcore.BaseTx
	validateCalled        bool
	validateRulesCalled   bool
	preflightRulesCalled  bool
	validateRulesExpected *amendment.Rules
}

func (t *rulesAwareValidationTx) Validate() error {
	t.validateCalled = true
	return ter.Errorf(ter.TemMALFORMED, "rules-free validator must be bypassed")
}

func (t *rulesAwareValidationTx) ValidateRules(rules *amendment.Rules) error {
	t.validateRulesCalled = true
	t.validateRulesExpected = rules
	return nil
}

func (t *rulesAwareValidationTx) PreflightRules(*amendment.Rules) error {
	t.preflightRulesCalled = true
	return ter.Errorf(ter.TemMALFORMED, "split rules validator must be bypassed")
}

func TestRulesAwareValidatorReplacesSplitPreflightBody(t *testing.T) {
	rules := amendment.AllSupportedRules()
	txn := &rulesAwareValidationTx{BaseTx: txcore.NewBaseTx(txcore.TypeAccountSet, precedenceSourceAddr)}

	require.Equal(t, ter.TesSUCCESS, validatePreflightBody(txn, rules))
	require.True(t, txn.validateRulesCalled)
	require.Same(t, rules, txn.validateRulesExpected)
	require.False(t, txn.validateCalled)
	require.False(t, txn.preflightRulesCalled)
}

func TestRulesAwareValidatorRunsForInnerTransaction(t *testing.T) {
	rules := amendment.AllSupportedRules()
	e := preflightEngine(rules)
	txn := &rulesAwareValidationTx{BaseTx: txcore.NewBaseTx(txcore.TypeAccountSet, precedenceSourceAddr)}

	require.Equal(t, ter.TesSUCCESS, e.preflightInner(txn))
	require.True(t, txn.validateRulesCalled)
	require.Same(t, rules, txn.validateRulesExpected)
	require.False(t, txn.validateCalled)
	require.False(t, txn.preflightRulesCalled)
}
