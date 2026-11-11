package tx_test

import (
	"slices"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
)

// registeredWith returns the registered types whose descriptor satisfies pred,
// sorted by code.
func registeredWith(pred func(tx.Descriptor) bool) []tx.Type {
	all.RegisterAll()
	var out []tx.Type
	for _, t := range tx.SupportedTypes() {
		if d, _ := tx.Lookup(t); pred(d) {
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return out
}

func assertTypeSet(t *testing.T, attribute string, got, want []tx.Type) {
	t.Helper()
	want = slices.Clone(want)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("%s types = %v, want %v", attribute, got, want)
	}
}

// Batch.h kDisabledTxTypes.
func TestDescriptorDisallowedInBatch(t *testing.T) {
	assertTypeSet(t, "DisallowedInBatch",
		registeredWith(func(d tx.Descriptor) bool { return d.DisallowedInBatch }),
		[]tx.Type{
			tx.TypeVaultCreate, tx.TypeVaultSet, tx.TypeVaultDelete,
			tx.TypeVaultDeposit, tx.TypeVaultWithdraw, tx.TypeVaultClawback,
			tx.TypeLoanBrokerSet, tx.TypeLoanBrokerDelete,
			tx.TypeLoanBrokerCoverDeposit, tx.TypeLoanBrokerCoverWithdraw,
			tx.TypeLoanBrokerCoverClawback, tx.TypeLoanSet, tx.TypeLoanDelete,
			tx.TypeLoanManage, tx.TypeLoanPay,
		})
}
