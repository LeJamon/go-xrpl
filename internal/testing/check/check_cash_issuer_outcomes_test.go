package check_test

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/metadata"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestCheckCashIssuerAmountsCleanup(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, tc := range []struct {
			name      string
			kind      string
			requested float64
			delivered float64
		}{
			{"partial-redemption", "Amount", 450, 450},
			{"above-minimum-delivery", "DeliverMin", 450, 900},
			{"amount-exceeds-check", "Amount", 901, 0},
			{"minimum-exceeds-check", "DeliverMin", 901, 0},
		} {
			t.Run(fmt.Sprintf("cleanup-%v/%s", cleanup, tc.name), func(t *testing.T) {
				env, source, issuer, _, checkID, checkKey, lineKey := newIssuerCheckFixture(t, cleanup, false)
				reserve := env.ReserveBase() + env.ReserveIncrement()
				spend := env.Balance(issuer) - (reserve - 1) - env.BaseFee()
				jtx.RequireTxSuccess(t, env.Submit(payment.Pay(issuer, env.MasterAccount(), spend).Build()))
				env.Close()
				require.Less(t, env.Balance(issuer), reserve)

				keys := []keylet.Keylet{checkKey, lineKey, keylet.Account(source.ID), keylet.OwnerDir(source.ID), keylet.OwnerDir(issuer.ID)}
				before := make([][]byte, len(keys))
				for i, key := range keys {
					data, err := env.LedgerEntry(key)
					require.NoError(t, err)
					require.NotEmpty(t, data)
					before[i] = append([]byte(nil), data...)
				}
				issuerSequence, sourceSequence := env.Seq(issuer), env.Seq(source)
				issuerBalance, sourceBalance := env.Balance(issuer), env.Balance(source)
				requested := tx.NewIssuedAmountFromFloat64(tc.requested, "USD", issuer.Address)
				result := env.Submit(issuerCheckCash(issuer, checkID, requested, tc.kind).Build())

				jtx.RequireSequence(t, env, issuer, issuerSequence+1)
				jtx.RequireSequence(t, env, source, sourceSequence)
				require.Equal(t, issuerBalance-env.BaseFee(), env.Balance(issuer))
				require.Equal(t, sourceBalance, env.Balance(source))
				jtx.RequireOwnerCount(t, env, issuer, 0)
				require.NotNil(t, result.Metadata)

				if tc.delivered == 0 {
					jtx.RequireTxClaimed(t, result, "tecPATH_PARTIAL")
					require.Nil(t, result.Metadata.DeliveredAmount)
					for i, key := range keys {
						data, err := env.LedgerEntry(key)
						require.NoError(t, err)
						require.Equal(t, before[i], data, "ledger entry %X changed on rejected cash", key.Key)
					}
					require.Len(t, result.Metadata.AffectedNodes, 1)
					require.NotNil(t, metadata.FindNode(result.Metadata, "ModifiedNode", "AccountRoot"))
					jtx.RequireOwnerCount(t, env, source, 2)
					return
				}

				jtx.RequireTxSuccess(t, result)
				delivered := tx.NewIssuedAmountFromFloat64(tc.delivered, "USD", issuer.Address)
				requireDeliveredAmount(t, result, delivered)
				jtx.RequireIOUBalance(t, env, source, issuer, "USD", 900-tc.delivered)
				jtx.RequireLedgerEntryNotExists(t, env, checkKey)
				jtx.RequireOwnerDirectoryContains(t, env, source, checkKey.Key, false)
				jtx.RequireOwnerDirectoryContains(t, env, issuer, checkKey.Key, false)
				require.NotNil(t, metadata.FindNode(result.Metadata, "DeletedNode", "Check"))
				require.Nil(t, metadata.FindNode(result.Metadata, "CreatedNode", "RippleState"))

				lineExists := tc.delivered < 900 || !cleanup
				require.Equal(t, lineExists, env.TrustLineExists(source, issuer, "USD"))
				jtx.RequireOwnerDirectoryContains(t, env, source, lineKey.Key, lineExists)
				jtx.RequireOwnerDirectoryContains(t, env, issuer, lineKey.Key, lineExists)
				if lineExists {
					jtx.RequireOwnerCount(t, env, source, 1)
					data, err := env.LedgerEntry(lineKey)
					require.NoError(t, err)
					line, err := state.ParseRippleState(data)
					require.NoError(t, err)
					previous, err := state.ParseRippleState(before[1])
					require.NoError(t, err)
					require.Equal(t, previous.LowLimit, line.LowLimit)
					require.Equal(t, previous.HighLimit, line.HighLimit)
				} else {
					jtx.RequireOwnerCount(t, env, source, 0)
					require.NotNil(t, metadata.FindNode(result.Metadata, "DeletedNode", "RippleState"))
				}
			})
		}
	}
}
