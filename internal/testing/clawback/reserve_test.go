package clawback_test

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/clawback"
	metatesting "github.com/LeJamon/go-xrpl/internal/testing/metadata"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

const reserveTestCurrency = "USD"

func newIssuerHolder(t *testing.T, issuerLow bool) (*jtx.TestEnv, *jtx.Account, *jtx.Account) {
	t.Helper()
	env := jtx.NewTestEnv(t)
	issuer, holder := jtx.NewAccount("issuer"), jtx.NewAccount("holder")
	if keylet.IsLowAccount(issuer.ID, holder.ID) != issuerLow {
		issuer, holder = holder, issuer
	}
	require.Equal(t, issuerLow, keylet.IsLowAccount(issuer.ID, holder.ID))
	env.FundNoRipple(issuer)
	env.Fund(holder)
	env.Close()
	return env, issuer, holder
}

func submitReserveSuccess(t *testing.T, env *jtx.TestEnv, transaction tx.Transaction) jtx.TxResult {
	t.Helper()
	result := env.Submit(transaction)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	return result
}

func issueAmount(value int64, issuer *jtx.Account) tx.Amount {
	return tx.NewIssuedAmount(value, 0, reserveTestCurrency, issuer.Address)
}

func issuerNoRippleFlag(issuer, holder *jtx.Account) uint32 {
	if keylet.IsLowAccount(issuer.ID, holder.ID) {
		return state.LsfLowNoRipple
	}
	return state.LsfHighNoRipple
}

func holderReserveFlag(issuer, holder *jtx.Account) uint32 {
	if keylet.IsLowAccount(holder.ID, issuer.ID) {
		return state.LsfLowReserve
	}
	return state.LsfHighReserve
}

func issuerReserveFlag(issuer, holder *jtx.Account) uint32 {
	if keylet.IsLowAccount(issuer.ID, holder.ID) {
		return state.LsfLowReserve
	}
	return state.LsfHighReserve
}

func requireNoIssuerOwnerCountMutation(t *testing.T, result jtx.TxResult, issuer *jtx.Account) {
	t.Helper()
	require.NotNil(t, result.Metadata)
	for _, node := range result.Metadata.AffectedNodes {
		if node.LedgerEntryType != "AccountRoot" {
			continue
		}
		account, ok := node.FinalFields["Account"].(string)
		if ok && account == issuer.Address {
			require.NotContains(t, node.PreviousFields, "OwnerCount", "issuer OwnerCount must not change")
		}
	}
}

func requireNoIssuerReserveMutation(t *testing.T, result jtx.TxResult, issuer *jtx.Account) {
	t.Helper()
	requireNoIssuerOwnerCountMutation(t, result, issuer)
	for _, node := range result.Metadata.AffectedNodes {
		if node.LedgerEntryType == "RippleState" {
			require.NotContains(t, node.PreviousFields, "Flags", "clawback must only change the IOU balance")
		}
	}
}

func TestClawback_IOUReserveState(t *testing.T) {
	for _, issuerLow := range []bool{true, false} {
		t.Run(fmt.Sprintf("issuerLow=%t", issuerLow), func(t *testing.T) {
			env, issuer, holder := newIssuerHolder(t, issuerLow)

			submitReserveSuccess(t, env, accountset.AccountSet(issuer).AllowClawback().Build())
			submitReserveSuccess(t, env, trustset.TrustSet(holder, issueAmount(1000, issuer)).Build())
			submitReserveSuccess(t, env, payment.PayIssued(issuer, holder, issueAmount(100, issuer)).Build())
			submitReserveSuccess(t, env, accountset.AccountSet(issuer).DefaultRipple().Build())

			wantFlags := issuerNoRippleFlag(issuer, holder) | holderReserveFlag(issuer, holder)
			require.Equal(t, wantFlags, env.TrustLineFlags(issuer, holder, reserveTestCurrency))
			jtx.RequireOwnerCount(t, env, issuer, 0)
			jtx.RequireOwnerCount(t, env, holder, 1)

			remaining := int64(100)
			for _, amount := range []int64{10, 90} {
				result := submitReserveSuccess(t, env, clawback.Claw(issuer, holder, reserveTestCurrency, amount).Build())
				remaining -= amount
				jtx.RequireIOUBalance(t, env, holder, issuer, reserveTestCurrency, float64(remaining))
				jtx.RequireIOUBalance(t, env, issuer, holder, reserveTestCurrency, -float64(remaining))
				require.Equal(t, wantFlags, env.TrustLineFlags(issuer, holder, reserveTestCurrency))
				jtx.RequireOwnerCount(t, env, issuer, 0)
				jtx.RequireOwnerCount(t, env, holder, 1)
				requireNoIssuerReserveMutation(t, result, issuer)
			}
		})
	}
}

func TestClawback_IOUDefaultLineDeletionMetadata(t *testing.T) {
	for _, issuerLow := range []bool{true, false} {
		t.Run(fmt.Sprintf("issuerLow=%t", issuerLow), func(t *testing.T) {
			env, issuer, holder := newIssuerHolder(t, issuerLow)

			submitReserveSuccess(t, env, accountset.AccountSet(issuer).AllowClawback().Build())
			submitReserveSuccess(t, env, trustset.TrustSet(holder, issueAmount(1000, issuer)).Build())
			submitReserveSuccess(t, env, payment.PayIssued(issuer, holder, issueAmount(100, issuer)).Build())
			submitReserveSuccess(t, env, trustset.TrustSet(holder, issueAmount(0, issuer)).Build())
			submitReserveSuccess(t, env, accountset.AccountSet(issuer).DefaultRipple().Build())

			issuerFlag := issuerNoRippleFlag(issuer, holder)
			holderFlag := holderReserveFlag(issuer, holder)
			require.Equal(t, issuerFlag|holderFlag, env.TrustLineFlags(issuer, holder, reserveTestCurrency))
			jtx.RequireOwnerCount(t, env, issuer, 0)
			jtx.RequireOwnerCount(t, env, holder, 1)

			result := submitReserveSuccess(t, env, clawback.Claw(issuer, holder, reserveTestCurrency, 100).Build())
			jtx.RequireIOUBalance(t, env, holder, issuer, reserveTestCurrency, 0)
			jtx.RequireOwnerCount(t, env, issuer, 0)
			jtx.RequireOwnerCount(t, env, holder, 0)
			jtx.RequireLedgerEntryNotExists(t, env, keylet.Line(issuer.ID, holder.ID, reserveTestCurrency))
			jtx.RequireOwnerDirectoryContains(t, env, issuer, keylet.Line(issuer.ID, holder.ID, reserveTestCurrency).Key, false)
			jtx.RequireOwnerDirectoryContains(t, env, holder, keylet.Line(issuer.ID, holder.ID, reserveTestCurrency).Key, false)
			jtx.RequireLedgerEntryNotExists(t, env, keylet.OwnerDir(issuer.ID))
			jtx.RequireLedgerEntryNotExists(t, env, keylet.OwnerDir(holder.ID))

			line := metatesting.FindNode(result.Metadata, "DeletedNode", "RippleState")
			require.NotNil(t, line, "full clawback must report a DeletedNode for the trust line")
			balance, ok := line.FinalFields["Balance"].(map[string]any)
			require.True(t, ok, "deleted trust line must include final Balance: %#v", line.FinalFields["Balance"])
			require.Equal(t, "0", balance["value"])
			require.Equal(t, issuerFlag, metatesting.ToUint32(line.FinalFields["Flags"]))
			require.Zero(t, metatesting.ToUint32(line.FinalFields["Flags"])&holderFlag)
		})
	}
}

// TestClawback_IOURetainsStaleIssuerReserve verifies that a receiver reserve
// remains set when the issuer account later enables DefaultRipple. The account
// setting does not rewrite the existing trust-line reserve bit.
func TestClawback_IOURetainsStaleIssuerReserve(t *testing.T) {
	for _, issuerLow := range []bool{true, false} {
		t.Run(fmt.Sprintf("issuerLow=%t", issuerLow), func(t *testing.T) {
			env, issuer, holder := newIssuerHolder(t, issuerLow)

			submitReserveSuccess(t, env, accountset.AccountSet(issuer).AllowClawback().Build())
			submitReserveSuccess(t, env, trustset.TrustSet(issuer, issueAmount(1000, holder)).Build())
			submitReserveSuccess(t, env, trustset.TrustSet(holder, issueAmount(1000, issuer)).Build())
			submitReserveSuccess(t, env, payment.PayIssued(issuer, holder, issueAmount(100, issuer)).Build())
			submitReserveSuccess(t, env, trustset.TrustSet(holder, issueAmount(0, issuer)).Build())
			submitReserveSuccess(t, env, trustset.TrustSet(issuer, issueAmount(0, holder)).Build())
			submitReserveSuccess(t, env, accountset.AccountSet(issuer).DefaultRipple().Build())

			issuerFlag := issuerReserveFlag(issuer, holder)
			holderFlag := holderReserveFlag(issuer, holder)
			require.NotZero(t, env.AccountInfo(issuer).Flags&state.LsfDefaultRipple)
			require.Equal(t, float64(0), env.Limit(holder, issuer, reserveTestCurrency))
			require.Equal(t, float64(0), env.Limit(issuer, holder, reserveTestCurrency))
			require.Zero(t, env.TrustLineFlags(issuer, holder, reserveTestCurrency)&issuerNoRippleFlag(issuer, holder))
			require.Equal(t, issuerFlag|holderFlag, env.TrustLineFlags(issuer, holder, reserveTestCurrency))
			jtx.RequireOwnerCount(t, env, issuer, 1)
			jtx.RequireOwnerCount(t, env, holder, 1)

			result := submitReserveSuccess(t, env, clawback.Claw(issuer, holder, reserveTestCurrency, 100).Build())
			jtx.RequireIOUBalance(t, env, holder, issuer, reserveTestCurrency, 0)
			require.True(t, env.TrustLineExists(issuer, holder, reserveTestCurrency))
			require.Equal(t, issuerFlag, env.TrustLineFlags(issuer, holder, reserveTestCurrency))
			jtx.RequireOwnerCount(t, env, issuer, 1)
			jtx.RequireOwnerCount(t, env, holder, 0)
			requireNoIssuerOwnerCountMutation(t, result, issuer)
			line := metatesting.FindNode(result.Metadata, "ModifiedNode", "RippleState")
			require.NotNil(t, line)
			require.Equal(t, issuerFlag|holderFlag, metatesting.ToUint32(line.PreviousFields["Flags"]))
			require.Equal(t, issuerFlag, metatesting.ToUint32(line.FinalFields["Flags"]))
		})
	}
}

func TestClawback_IOUHolderReservePredicates(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, env *jtx.TestEnv, issuer, holder *jtx.Account)
		check func(t *testing.T, env *jtx.TestEnv, issuer, holder *jtx.Account)
	}{
		{
			name: "quality_in",
			setup: func(t *testing.T, env *jtx.TestEnv, issuer, holder *jtx.Account) {
				submitReserveSuccess(t, env, trustset.TrustSet(holder, issueAmount(1000, issuer)).QualityIn(2_000_000_000).Build())
			},
			check: func(t *testing.T, env *jtx.TestEnv, issuer, holder *jtx.Account) {
				requireHolderQuality(t, env, issuer, holder, 2_000_000_000, 0)
			},
		},
		{
			name: "quality_out",
			setup: func(t *testing.T, env *jtx.TestEnv, issuer, holder *jtx.Account) {
				submitReserveSuccess(t, env, trustset.TrustSet(holder, issueAmount(1000, issuer)).QualityOut(3_000_000_000).Build())
			},
			check: func(t *testing.T, env *jtx.TestEnv, issuer, holder *jtx.Account) {
				requireHolderQuality(t, env, issuer, holder, 0, 3_000_000_000)
			},
		},
		{
			name: "freeze",
			setup: func(t *testing.T, env *jtx.TestEnv, issuer, holder *jtx.Account) {
				submitReserveSuccess(t, env, trustset.TrustSet(holder, issueAmount(1000, issuer)).Build())
				submitReserveSuccess(t, env, trustset.TrustSet(holder, issueAmount(1000, issuer)).Freeze().Build())
			},
			check: func(t *testing.T, env *jtx.TestEnv, issuer, holder *jtx.Account) {
				require.NotZero(t, env.TrustLineFlags(issuer, holder, reserveTestCurrency)&holderFreezeFlag(issuer, holder))
			},
		},
		{
			name: "holder_no_ripple_mismatch",
			setup: func(t *testing.T, env *jtx.TestEnv, issuer, holder *jtx.Account) {
				submitReserveSuccess(t, env, trustset.TrustSet(holder, issueAmount(1000, issuer)).Build())
				submitReserveSuccess(t, env, trustset.TrustSet(holder, issueAmount(1000, issuer)).NoRipple().Build())
			},
			check: func(t *testing.T, env *jtx.TestEnv, issuer, holder *jtx.Account) {
				require.NotZero(t, env.TrustLineFlags(issuer, holder, reserveTestCurrency)&holderNoRippleFlag(issuer, holder))
			},
		},
	}

	for i, test := range tests {
		issuerLow := i%2 == 0
		t.Run(fmt.Sprintf("%s/issuerLow=%t", test.name, issuerLow), func(t *testing.T) {
			env, issuer, holder := newIssuerHolder(t, issuerLow)
			submitReserveSuccess(t, env, accountset.AccountSet(issuer).AllowClawback().Build())
			test.setup(t, env, issuer, holder)
			submitReserveSuccess(t, env, payment.PayIssued(issuer, holder, issueAmount(100, issuer)).Build())
			submitReserveSuccess(t, env, trustset.TrustSet(holder, issueAmount(0, issuer)).Build())
			submitReserveSuccess(t, env, accountset.AccountSet(issuer).DefaultRipple().Build())

			wantFlags := env.TrustLineFlags(issuer, holder, reserveTestCurrency)
			require.Equal(t, float64(0), env.Limit(holder, issuer, reserveTestCurrency))
			require.NotZero(t, wantFlags&holderReserveFlag(issuer, holder))
			if test.check != nil {
				test.check(t, env, issuer, holder)
			}
			result := submitReserveSuccess(t, env, clawback.Claw(issuer, holder, reserveTestCurrency, 100).Build())
			jtx.RequireIOUBalance(t, env, holder, issuer, reserveTestCurrency, 0)
			require.True(t, env.TrustLineExists(issuer, holder, reserveTestCurrency))
			require.Equal(t, wantFlags, env.TrustLineFlags(issuer, holder, reserveTestCurrency))
			jtx.RequireOwnerCount(t, env, issuer, 0)
			jtx.RequireOwnerCount(t, env, holder, 1)
			requireNoIssuerReserveMutation(t, result, issuer)
			if test.check != nil {
				test.check(t, env, issuer, holder)
			}
		})
	}
}

func requireHolderQuality(t *testing.T, env *jtx.TestEnv, issuer, holder *jtx.Account, qualityIn, qualityOut uint32) {
	t.Helper()
	raw, err := env.LedgerEntry(keylet.Line(issuer.ID, holder.ID, reserveTestCurrency))
	require.NoError(t, err)
	line, err := state.ParseRippleState(raw)
	require.NoError(t, err)
	if keylet.IsLowAccount(holder.ID, issuer.ID) {
		require.Equal(t, qualityIn, line.LowQualityIn)
		require.Equal(t, qualityOut, line.LowQualityOut)
	} else {
		require.Equal(t, qualityIn, line.HighQualityIn)
		require.Equal(t, qualityOut, line.HighQualityOut)
	}
}

func holderFreezeFlag(issuer, holder *jtx.Account) uint32 {
	if keylet.IsLowAccount(holder.ID, issuer.ID) {
		return state.LsfLowFreeze
	}
	return state.LsfHighFreeze
}

func holderNoRippleFlag(issuer, holder *jtx.Account) uint32 {
	if keylet.IsLowAccount(holder.ID, issuer.ID) {
		return state.LsfLowNoRipple
	}
	return state.LsfHighNoRipple
}
