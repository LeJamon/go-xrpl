package lending_test

import (
	"bytes"
	"encoding/hex"
	"sort"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// TestLoanSetCounterSignerAuthorisation checks that CounterpartySignature is
// authorised against the counter-signing account (Counterparty, or the
// LoanBroker owner when absent) with the same rules as a transaction source.
func TestLoanSetCounterSignerAuthorisation(t *testing.T) {
	type fixture struct {
		*loanSetAssetFixture
		rogue, regular, signer1, signer2 *jtx.Account
	}
	signerList := func(f *fixture) {
		f.env.SetSignerList(f.owner, 2, []jtx.TestSigner{{Account: f.signer1, Weight: 1}, {Account: f.signer2, Weight: 1}})
		f.env.Close()
	}
	regularKey := func(f *fixture) {
		f.env.SetRegularKey(f.owner, f.regular)
		f.env.Close()
	}

	tests := []struct {
		name string
		// omitCounterparty leaves Counterparty unset so the broker owner is the
		// counter-signer.
		omitCounterparty bool
		setup            func(*fixture)
		mutate           func(*testing.T, *fixture, *lending.LoanSet)
		single           func(*fixture) *jtx.Account
		multi            func(*fixture) []*jtx.Account
		// corrupt changes the transaction after counter-signing.
		corrupt bool
		want    string
	}{
		{name: "owner master key", single: func(f *fixture) *jtx.Account { return f.owner }, want: jtx.TesSUCCESS},
		{name: "owner master key without counterparty", omitCounterparty: true, single: func(f *fixture) *jtx.Account { return f.owner }, want: jtx.TesSUCCESS},
		{name: "owner regular key", setup: regularKey, single: func(f *fixture) *jtx.Account { return f.regular }, want: jtx.TesSUCCESS},
		{
			name: "owner regular key with master disabled",
			setup: func(f *fixture) {
				regularKey(f)
				f.env.DisableMasterKey(f.owner)
				f.env.Close()
			},
			single: func(f *fixture) *jtx.Account { return f.regular },
			want:   jtx.TesSUCCESS,
		},
		{name: "listed signers", setup: signerList, multi: func(f *fixture) []*jtx.Account { return []*jtx.Account{f.signer1, f.signer2} }, want: jtx.TesSUCCESS},
		{name: "unfunded key", single: func(f *fixture) *jtx.Account { return f.rogue }, want: "tefBAD_AUTH"},
		{name: "unfunded key without counterparty", omitCounterparty: true, single: func(f *fixture) *jtx.Account { return f.rogue }, want: "tefBAD_AUTH"},
		{name: "borrower self-countersign", omitCounterparty: true, single: func(f *fixture) *jtx.Account { return f.borrower }, want: "tefBAD_AUTH"},
		{name: "unrelated key while regular key set", setup: regularKey, single: func(f *fixture) *jtx.Account { return f.rogue }, want: "tefBAD_AUTH"},
		{
			name: "owner master key while disabled",
			setup: func(f *fixture) {
				regularKey(f)
				f.env.DisableMasterKey(f.owner)
				f.env.Close()
			},
			single: func(f *fixture) *jtx.Account { return f.owner },
			want:   "tefMASTER_DISABLED",
		},
		{name: "multi-signed without signer list", multi: func(f *fixture) []*jtx.Account { return []*jtx.Account{f.signer1, f.signer2} }, want: "tefNOT_MULTI_SIGNING"},
		{name: "unlisted signers", setup: signerList, multi: func(f *fixture) []*jtx.Account { return []*jtx.Account{f.rogue, f.regular} }, want: "tefBAD_SIGNATURE"},
		{name: "below quorum", setup: signerList, multi: func(f *fixture) []*jtx.Account { return []*jtx.Account{f.signer1} }, want: "tefBAD_QUORUM"},
		{
			name:             "missing broker without counterparty",
			omitCounterparty: true,
			mutate: func(_ *testing.T, _ *fixture, set *lending.LoanSet) {
				set.LoanBrokerID = strings.Repeat("AB", 32)
			},
			single: func(f *fixture) *jtx.Account { return f.owner },
			want:   "temBAD_SIGNER",
		},
		{
			name: "pseudo-account counterparty",
			mutate: func(t *testing.T, f *fixture, set *lending.LoanSet) {
				brokerKey, err := hex.DecodeString(f.brokerID)
				require.NoError(t, err)
				pseudo, err := state.EncodeAccountID(loanBrokerPseudoID(t, f.env, keylet.LoanBrokerByID([32]byte(brokerKey))))
				require.NoError(t, err)
				set.Counterparty = pseudo
			},
			single: func(f *fixture) *jtx.Account { return f.owner },
			want:   "tefBAD_AUTH",
		},
		{name: "corrupted signature", corrupt: true, single: func(f *fixture) *jtx.Account { return f.owner }, want: "temINVALID"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fixture{
				loanSetAssetFixture: newLoanValueRangeFixture(t, "XRP"),
				rogue:               jtx.NewAccount("rogue"),
				regular:             jtx.NewAccount("regular"),
				signer1:             jtx.NewAccount("signer1"),
				signer2:             jtx.NewAccount("signer2"),
			}
			if tc.setup != nil {
				tc.setup(f)
			}

			set := lending.NewLoanSet(f.borrower.Address, f.brokerID, "1000")
			if !tc.omitCounterparty {
				set.Counterparty = f.owner.Address
			}
			if tc.mutate != nil {
				tc.mutate(t, f, set)
			}
			seq := f.env.Seq(f.borrower)
			set.GetCommon().Sequence = &seq
			set.GetCommon().Fee = "100"
			set.GetCommon().SigningPubKey = strings.ToUpper(f.borrower.PublicKeyHex())
			if tc.multi != nil {
				set.GetCommon().CounterpartySignature = counterMultiSign(t, f.env, set, tc.multi(f))
			} else {
				signer := tc.single(f)
				sig, err := txsign.SignCounterpartyWithRules(set,
					strings.ToUpper(signer.PublicKeyHex()),
					"00"+strings.ToUpper(signer.PrivateKeyHex()), f.env.Rules())
				require.NoError(t, err)
				set.GetCommon().CounterpartySignature = sig
			}
			if tc.corrupt {
				set.PrincipalRequested = "1001"
			}

			before := loanSetLedgerState(t, f.env)
			f.env.SetVerifySignatures(true)
			result := f.env.Submit(set)
			f.env.SetVerifySignatures(false)

			require.Equal(t, tc.want, result.Code)
			if tc.want == jtx.TesSUCCESS {
				return
			}
			require.False(t, result.Applied)
			require.Equal(t, before, loanSetLedgerState(t, f.env), "rejected LoanSet must not change ledger state")
		})
	}
}

func counterMultiSign(t *testing.T, env *jtx.TestEnv, set *lending.LoanSet, signers []*jtx.Account) *tx.CounterpartySignature {
	t.Helper()
	sorted := append([]*jtx.Account(nil), signers...)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i].AccountID(), sorted[j].AccountID()
		return bytes.Compare(a[:], b[:]) < 0
	})
	cp := &tx.CounterpartySignature{}
	for _, signer := range sorted {
		sig, err := txsign.SignTransactionForMultiSignRole(set, signer.Address,
			"00"+strings.ToUpper(signer.PrivateKeyHex()), binarycodec.CounterpartyRole, env.Rules())
		require.NoError(t, err)
		cp.Signers = append(cp.Signers, tx.SignerWrapper{Signer: tx.Signer{
			Account: signer.Address, SigningPubKey: strings.ToUpper(signer.PublicKeyHex()), TxnSignature: sig,
		}})
	}
	return cp
}
