package tx_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/trustset"
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

func TestRegisterRejectsIncompleteDescriptor(t *testing.T) {
	all.RegisterAll()
	newPayment := func() tx.Transaction { return payment.NewPayment("", "", tx.Amount{}) }
	newTrustSet := func() tx.Transaction { return &trustset.TrustSet{BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "")} }
	complete := tx.Descriptor{Type: tx.TypePayment, New: newPayment, Delegation: tx.Delegable, Consequences: tx.CustomConsequences}
	cases := []struct {
		name   string
		mutate func(*tx.Descriptor)
		want   string
	}{
		{"unnamed type", func(d *tx.Descriptor) { d.Type = tx.TypeNickNameSet }, "no canonical name"},
		{"no constructor", func(d *tx.Descriptor) { d.New = nil }, "missing constructor"},
		{"wrong constructor", func(d *tx.Descriptor) { d.New = newTrustSet }, "constructor builds"},
		{"no delegation", func(d *tx.Descriptor) { d.Delegation = 0 }, "delegation not declared"},
		{"no consequences", func(d *tx.Descriptor) { d.Consequences = 0 }, "consequences not declared"},
		{"custom not declared", func(d *tx.Descriptor) { d.Consequences = tx.NormalConsequences }, "ConsequencesCalculator"},
		{"custom not implemented", func(d *tx.Descriptor) {
			d.Type, d.New = tx.TypeTrustSet, newTrustSet
		}, "ConsequencesCalculator"},
		{"duplicate", func(*tx.Descriptor) {}, "already registered"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := complete
			c.mutate(&d)
			defer func() {
				msg, _ := recover().(string)
				if !strings.Contains(msg, c.want) {
					t.Errorf("panic = %q, want it to mention %q", msg, c.want)
				}
			}()
			tx.Register(d)
		})
	}
}

// SponsorHelpers.cpp isReserveSponsorAllowed.
func TestDescriptorReserveSponsorable(t *testing.T) {
	assertTypeSet(t, "ReserveSponsorable",
		registeredWith(func(d tx.Descriptor) bool { return d.ReserveSponsorable }),
		[]tx.Type{
			tx.TypeDelegateSet, tx.TypeDepositPreauth, tx.TypePayment,
			tx.TypeSignerListSet, tx.TypeCheckCancel, tx.TypeCheckCash,
			tx.TypeCheckCreate, tx.TypeEscrowCancel, tx.TypeEscrowCreate,
			tx.TypeEscrowFinish, tx.TypePaymentChannelClaim,
			tx.TypePaymentChannelCreate, tx.TypePaymentChannelFund,
			tx.TypeClawback, tx.TypeMPTokenAuthorize,
			tx.TypeMPTokenIssuanceCreate, tx.TypeMPTokenIssuanceDestroy,
			tx.TypeMPTokenIssuanceSet, tx.TypeTrustSet,
			tx.TypeCredentialAccept, tx.TypeCredentialCreate,
			tx.TypeCredentialDelete, tx.TypeAccountSet, tx.TypeRegularKeySet,
			tx.TypeSponsorshipTransfer,
		})
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
