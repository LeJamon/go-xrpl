// Tests for DepositPreauth transaction behaviour.
// Reference: rippled/src/test/app/DepositAuth_test.cpp – struct DepositPreauth_test
package depositpreauth_test

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"sort"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/credential"
	dp "github.com/LeJamon/go-xrpl/internal/testing/depositpreauth"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/depositpreauth"
	paymentPkg "github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/keylet"
	ledgerentry "github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

// xrpAccount is the XRPL zero account address (20 bytes of zero).
const xrpAccount = "rrrrrrrrrrrrrrrrrrrrrhoLvTp"

// --------------------------------------------------------------------------
// testEnable
// Reference: rippled DepositPreauth_test::testEnable (lines 413-493)
// --------------------------------------------------------------------------

func TestDepositPreauth_Enable(t *testing.T) {
	alice := jtx.NewAccount("alice")
	becky := jtx.NewAccount("becky")

	// featureDepositPreauth enabled.
	t.Run("Enabled", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(becky, uint64(jtx.XRP(10000)))
		env.Close()

		// Add DepositPreauth for becky.
		result := env.Submit(dp.Auth(alice, becky).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 1)
		jtx.RequireOwnerCount(t, env, becky, 0)

		// Remove DepositPreauth for becky.
		result = env.Submit(dp.Unauth(alice, becky).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 0)
		jtx.RequireOwnerCount(t, env, becky, 0)
	})

	// Verify that tickets can be used for preauthorization.
	t.Run("Tickets", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(becky, uint64(jtx.XRP(10000)))
		env.Close()

		// Create 2 tickets.
		firstTicket := env.CreateTickets(alice, 2)
		aliceSeq := env.Seq(alice)
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 2) // 2 tickets

		// Consume tickets from biggest to smallest.
		aliceTicketSeq := aliceSeq

		// Add DepositPreauth using a ticket.
		aliceTicketSeq--
		result := env.Submit(dp.Auth(alice, becky).TicketSeq(aliceTicketSeq).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		// Used one ticket, gained one preauth entry: 2 - 1 + 1 = 2
		jtx.RequireOwnerCount(t, env, alice, 2)
		require.Equal(t, aliceSeq, env.Seq(alice), "sequence should not advance when using tickets")
		jtx.RequireOwnerCount(t, env, becky, 0)

		// Remove DepositPreauth using a ticket.
		aliceTicketSeq--
		require.Equal(t, firstTicket, aliceTicketSeq) // sanity check: we're at the first ticket
		result = env.Submit(dp.Unauth(alice, becky).TicketSeq(aliceTicketSeq).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 0)
		require.Equal(t, aliceSeq, env.Seq(alice))
		jtx.RequireOwnerCount(t, env, becky, 0)
	})
}

// --------------------------------------------------------------------------
// testInvalid
// Reference: rippled DepositPreauth_test::testInvalid (lines 495-611)
// --------------------------------------------------------------------------

func TestDepositPreauth_Invalid(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	becky := jtx.NewAccount("becky")
	carol := jtx.NewAccount("carol")

	// Add DepositPreauth to an unfunded account.
	t.Run("UnfundedAccount", func(t *testing.T) {
		result := env.Submit(dp.Auth(alice, becky).Sequence(1).Build())
		require.Equal(t, "terNO_ACCOUNT", result.Code)
	})

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(becky, uint64(jtx.XRP(10000)))
	env.Close()

	// Bad fee.
	t.Run("BadFee", func(t *testing.T) {
		raw := dp.Raw(alice.Address).
			Authorize(becky.Address).
			Fee("-10").
			Sequence(env.Seq(alice))
		result := env.Submit(raw.Build())
		require.Equal(t, "temBAD_FEE", result.Code)
		env.Close()
	})

	// Bad flags.
	t.Run("BadFlags", func(t *testing.T) {
		// tfSell = 0x00080000 is an offer-specific flag, invalid for DepositPreauth.
		result := env.Submit(dp.Auth(alice, becky).Flags(0x00080000).Build())
		require.Equal(t, "temINVALID_FLAG", result.Code)
		env.Close()
	})

	// Neither Authorize nor Unauthorize.
	t.Run("NeitherAuthNorUnauth", func(t *testing.T) {
		raw := dp.Raw(alice.Address).
			Fee("10").
			Sequence(env.Seq(alice))
		result := env.Submit(raw.Build())
		require.Equal(t, "temMALFORMED", result.Code)
		env.Close()
	})

	// Both Authorize and Unauthorize.
	t.Run("BothAuthAndUnauth", func(t *testing.T) {
		raw := dp.Raw(alice.Address).
			Authorize(becky.Address).
			Unauthorize(becky.Address).
			Fee("10").
			Sequence(env.Seq(alice))
		result := env.Submit(raw.Build())
		require.Equal(t, "temMALFORMED", result.Code)
		env.Close()
	})

	// Authorize zero account.
	t.Run("AuthorizeZeroAccount", func(t *testing.T) {
		raw := dp.Raw(alice.Address).
			Authorize(xrpAccount).
			Fee("10").
			Sequence(env.Seq(alice))
		result := env.Submit(raw.Build())
		require.Equal(t, "temINVALID_ACCOUNT_ID", result.Code)
		env.Close()
	})

	// Self-authorization.
	t.Run("SelfAuth", func(t *testing.T) {
		result := env.Submit(dp.Auth(alice, alice).Build())
		require.Equal(t, "temCANNOT_PREAUTH_SELF", result.Code)
		env.Close()
	})

	// Authorize unfunded account.
	t.Run("AuthUnfundedTarget", func(t *testing.T) {
		result := env.Submit(dp.Auth(alice, carol).Build())
		require.Equal(t, "tecNO_TARGET", result.Code)
		env.Close()
	})

	t.Run("AuthorizationLifecycle", func(t *testing.T) {
		lifecycleEnv := jtx.NewTestEnv(t)
		lifecycleEnv.FundAmount(alice, uint64(jtx.XRP(10000)))
		lifecycleEnv.FundAmount(becky, uint64(jtx.XRP(10000)))
		lifecycleEnv.Close()
		preauthKey := keylet.DepositPreauth(alice.ID, becky.ID)
		initialBalance := lifecycleEnv.Balance(alice)

		jtx.RequireOwnerCount(t, lifecycleEnv, alice, 0)
		result := lifecycleEnv.Submit(dp.Auth(alice, becky).Build())
		jtx.RequireTxSuccess(t, result)
		lifecycleEnv.Close()
		jtx.RequireLedgerEntryExists(t, lifecycleEnv, preauthKey)
		jtx.RequireOwnerDirectoryContains(t, lifecycleEnv, alice, preauthKey.Key, true)
		jtx.RequireOwnerCount(t, lifecycleEnv, alice, 1)
		data, err := lifecycleEnv.LedgerEntry(preauthKey)
		require.NoError(t, err)
		stored := &ledgerentry.DepositPreauth{}
		require.NoError(t, stored.Decode(data))
		require.Equal(t, alice.Address, stored.Account)
		require.Equal(t, becky.Address, stored.Authorize)
		require.Equal(t, "0", stored.OwnerNode)
		require.Equal(t, initialBalance-lifecycleEnv.BaseFee(), lifecycleEnv.Balance(alice))
		availableAfterCreate := lifecycleEnv.Balance(alice) - reserve(lifecycleEnv, 1)

		result = lifecycleEnv.Submit(dp.Auth(alice, becky).Build())
		require.Equal(t, "tecDUPLICATE", result.Code)
		lifecycleEnv.Close()
		jtx.RequireLedgerEntryExists(t, lifecycleEnv, preauthKey)
		jtx.RequireOwnerCount(t, lifecycleEnv, alice, 1)

		result = lifecycleEnv.Submit(dp.Unauth(alice, becky).Build())
		jtx.RequireTxSuccess(t, result)
		lifecycleEnv.Close()
		jtx.RequireLedgerEntryNotExists(t, lifecycleEnv, preauthKey)
		jtx.RequireOwnerDirectoryContains(t, lifecycleEnv, alice, preauthKey.Key, false)
		jtx.RequireOwnerCount(t, lifecycleEnv, alice, 0)
		require.Equal(t, initialBalance-3*lifecycleEnv.BaseFee(), lifecycleEnv.Balance(alice))
		require.Equal(t,
			availableAfterCreate+lifecycleEnv.ReserveIncrement()-2*lifecycleEnv.BaseFee(),
			lifecycleEnv.Balance(alice)-reserve(lifecycleEnv, 0),
		)

		result = lifecycleEnv.Submit(dp.Unauth(alice, becky).Build())
		require.Equal(t, "tecNO_ENTRY", result.Code)
		lifecycleEnv.Close()
		jtx.RequireOwnerCount(t, lifecycleEnv, alice, 0)
	})

	// Insufficient reserve.
	t.Run("InsufficientReserve", func(t *testing.T) {
		// Fund carol with just below what's needed for one owner object.
		// accountReserve(1) = reserveBase + reserveIncrement = 250,000,000
		// priorBalance = funded amount (fee is added back), so fund < 250,000,000.
		env.FundAmount(carol, 249_999_999)
		env.Close()

		result := env.Submit(dp.Auth(carol, becky).Build())
		require.Equal(t, "tecINSUFFICIENT_RESERVE", result.Code)
		env.Close()
		jtx.RequireOwnerCount(t, env, carol, 0)
		jtx.RequireOwnerCount(t, env, becky, 0)

		// Give carol enough to barely meet the reserve.
		result = env.Submit(payment.Pay(alice, carol, env.BaseFee()+1).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(dp.Auth(carol, becky).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireOwnerCount(t, env, carol, 1)
		jtx.RequireOwnerCount(t, env, becky, 0)

		// But carol can't afford another preauthorization.
		result = env.Submit(dp.Auth(carol, alice).Build())
		require.Equal(t, "tecINSUFFICIENT_RESERVE", result.Code)
		env.Close()
		jtx.RequireOwnerCount(t, env, carol, 1)
		jtx.RequireOwnerCount(t, env, becky, 0)
		jtx.RequireOwnerCount(t, env, alice, 0)
	})

	// Remove non-existent authorization.
	t.Run("RemoveNonExistent", func(t *testing.T) {
		result := env.Submit(dp.Unauth(alice, carol).Build())
		require.Equal(t, "tecNO_ENTRY", result.Code)
		env.Close()
		jtx.RequireOwnerCount(t, env, alice, 0)
		jtx.RequireOwnerCount(t, env, becky, 0)
	})
}

// --------------------------------------------------------------------------
// testPayment
// Reference: rippled DepositPreauth_test::testPayment (lines 613-816)
// Called 4 times with different feature combinations in rippled's run().
// --------------------------------------------------------------------------

func TestDepositPreauth_Payment(t *testing.T) {
	type featureSet struct {
		name                string
		supportsCredentials bool
	}

	featureSets := []featureSet{
		{"NoCredentials", false},
		{"WithCredentials", true},
	}

	for _, fs := range featureSets {
		t.Run(fs.name, func(t *testing.T) {
			testPayment(t, fs.supportsCredentials)
		})
	}
}

func testPayment(t *testing.T, supportsCredentials bool) {
	t.Helper()

	alice := jtx.NewAccount("alice")
	becky := jtx.NewAccount("becky")
	gw := jtx.NewAccount("gw")

	// Self-payment bug fix section
	t.Run("SelfPayment", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		if !supportsCredentials {
			env.DisableFeature("Credentials")
		}

		env.FundAmount(alice, uint64(jtx.XRP(5000)))
		env.FundAmount(becky, uint64(jtx.XRP(5000)))
		env.FundAmount(gw, uint64(jtx.XRP(5000)))
		env.Close()

		result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(trustset.TrustLine(becky, "USD", gw, "1000").Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		usd500 := tx.NewIssuedAmountFromFloat64(500, "USD", gw.Address)
		result = env.Submit(payment.PayIssued(gw, alice, usd500).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// alice creates passive offer: TakerPays=XRP(100), TakerGets=USD(100)
		// In rippled: offer(alice, XRP(100), USD(100), tfPassive)
		// Note: rippled's offer(account, takerPays, takerGets) vs Go's CreatePassiveOffer(account, takerGets, takerPays)
		usd100 := tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)
		xrp100 := tx.NewXRPAmount(jtx.XRP(100))
		env.CreatePassiveOffer(alice, usd100, xrp100)
		env.Close()

		// becky pays herself USD(10) by consuming part of alice's offer.
		// Reference: rippled uses path(~USD) which includes currency AND issuer.
		// ~USD = BookSpec(gw, USD) → {typeCurrency|typeIssuer, currency=USD, issuer=gw}
		usd10 := tx.NewIssuedAmountFromFloat64(10, "USD", gw.Address)
		xrp10 := tx.NewXRPAmount(jtx.XRP(10))
		usdPath := [][]paymentPkg.PathStep{{{Currency: "USD", Issuer: gw.Address}}}
		result = env.Submit(
			payment.PayIssued(becky, becky, usd10).
				SendMax(xrp10).
				Paths(usdPath).
				Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// becky enables DepositAuth.
		env.EnableDepositAuth(becky)
		env.Close()

		// becky pays herself again; DepositPreauth fixed the old self-payment bug.
		result = env.Submit(
			payment.PayIssued(becky, becky, usd10).
				SendMax(xrp10).
				Paths(usdPath).
				Build(),
		)
		require.Equal(t, "tesSUCCESS", result.Code)
		env.Close()
	})

	// Credential-based payment section
	t.Run("CredentialPayment", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		if !supportsCredentials {
			env.DisableFeature("Credentials")
		}

		env.FundAmount(alice, uint64(jtx.XRP(5000)))
		env.FundAmount(becky, uint64(jtx.XRP(5000)))
		env.FundAmount(gw, uint64(jtx.XRP(5000)))
		env.Close()

		// Set up trust line from becky to gw for IOU payment.
		result := env.Submit(trustset.TrustLine(becky, "USD", gw, "1000").Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		credType := "abcde"
		carol := jtx.NewAccount("carol")
		env.FundAmount(carol, uint64(jtx.XRP(5000)))
		env.Close()

		// Enable DepositAuth on becky.
		env.EnableDepositAuth(becky)
		env.Close()

		// Expected results based on feature flags.
		expectCredentials := "tesSUCCESS"
		if !supportsCredentials {
			expectCredentials = "temDISABLED"
		}
		expectDP := "tesSUCCESS"
		if !supportsCredentials {
			expectDP = "temDISABLED"
		}
		expectPayment := "tesSUCCESS"
		if !supportsCredentials {
			expectPayment = "temDISABLED"
		}

		// becky sets up credential-based preauth.
		result = env.Submit(dp.AuthCredentials(becky, []dp.AuthorizeCredentials{
			{Issuer: carol, CredTypeText: credType},
		}).Build())
		require.Equal(t, expectDP, result.Code)
		env.Close()

		// carol creates credential for gw (subject=gw, issuer=carol).
		result = env.Submit(credential.CredentialCreateText(carol, gw, credType).Build())
		require.Equal(t, expectCredentials, result.Code)
		env.Close()
		// gw accepts the credential from carol.
		result = env.Submit(credential.CredentialAcceptText(gw, carol, credType).Build())
		require.Equal(t, expectCredentials, result.Code)
		env.Close()

		// Compute credential index (subject=gw, issuer=carol).
		var credIdx string
		if supportsCredentials {
			credIdx = dp.CredentialIndexHex(gw, carol, credType)
		} else {
			credIdx = "48004829F915654A81B11C4AB8218D96FED67F209B58328A72314FB6EA288BE4"
		}

		// gw pays becky using credentials.
		usd100 := tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)
		result = env.Submit(
			payment.PayIssued(gw, becky, usd100).
				CredentialIDs([]string{credIdx}).
				Build(),
		)
		require.Equal(t, expectPayment, result.Code)
		env.Close()
	})

	// Preauthorization payment section.
	t.Run("PreauthPayments", func(t *testing.T) {
		carol := jtx.NewAccount("carol2")

		env := jtx.NewTestEnv(t)
		if !supportsCredentials {
			env.DisableFeature("Credentials")
		}

		env.FundAmount(alice, uint64(jtx.XRP(5000)))
		env.FundAmount(becky, uint64(jtx.XRP(5000)))
		env.FundAmount(carol, uint64(jtx.XRP(5000)))
		env.FundAmount(gw, uint64(jtx.XRP(5000)))
		env.Close()

		result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(trustset.TrustLine(becky, "USD", gw, "1000").Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(trustset.TrustLine(carol, "USD", gw, "1000").Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		usd1000 := tx.NewIssuedAmountFromFloat64(1000, "USD", gw.Address)
		result = env.Submit(payment.PayIssued(gw, alice, usd1000).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Make XRP and IOU payments from alice to becky. Should be fine.
		xrp100 := uint64(jtx.XRP(100))
		usd100 := tx.NewIssuedAmountFromFloat64(100, "USD", gw.Address)

		result = env.Submit(payment.Pay(alice, becky, xrp100).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(payment.PayIssued(alice, becky, usd100).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// becky enables DepositAuth.
		env.EnableDepositAuth(becky)
		env.Close()

		// alice can no longer pay becky.
		result = env.Submit(payment.Pay(alice, becky, xrp100).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(payment.PayIssued(alice, becky, usd100).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		env.Close()

		// becky preauthorizes carol (not alice).
		result = env.Submit(dp.Auth(becky, carol).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// alice still can't pay becky.
		result = env.Submit(payment.Pay(alice, becky, xrp100).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(payment.PayIssued(alice, becky, usd100).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		env.Close()

		// becky preauthorizes alice.
		result = env.Submit(dp.Auth(becky, alice).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// alice can now pay becky.
		result = env.Submit(payment.Pay(alice, becky, xrp100).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(payment.PayIssued(alice, becky, usd100).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// alice enables DepositAuth. becky is not authorized to pay alice.
		env.EnableDepositAuth(alice)
		env.Close()

		result = env.Submit(payment.Pay(becky, alice, xrp100).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(payment.PayIssued(becky, alice, usd100).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		env.Close()

		// becky removes carol's preauth. Should have no impact on alice.
		result = env.Submit(dp.Unauth(becky, carol).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(payment.Pay(alice, becky, xrp100).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(payment.PayIssued(alice, becky, usd100).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// becky removes alice's preauth. alice now can't pay.
		result = env.Submit(dp.Unauth(becky, alice).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(payment.Pay(alice, becky, xrp100).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		result = env.Submit(payment.PayIssued(alice, becky, usd100).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		env.Close()

		// becky clears DepositAuth. alice can pay again.
		env.DisableDepositAuth(becky)
		env.Close()

		result = env.Submit(payment.Pay(alice, becky, xrp100).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(payment.PayIssued(alice, becky, usd100).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})
}

// --------------------------------------------------------------------------
// testCredentialsPayment
// Reference: rippled DepositPreauth_test::testCredentialsPayment (lines 818-1021)
// --------------------------------------------------------------------------

func TestDepositPreauth_CredentialsPayment(t *testing.T) {
	credType := "abcde"

	issuer := jtx.NewAccount("issuer")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	maria := jtx.NewAccount("maria")
	john := jtx.NewAccount("john")

	// ---- Payment failure with disabled credentials rule ----
	t.Run("DisabledCredentials", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("Credentials")

		env.FundAmount(issuer, uint64(jtx.XRP(5000)))
		env.FundAmount(bob, uint64(jtx.XRP(5000)))
		env.FundAmount(alice, uint64(jtx.XRP(5000)))
		env.Close()

		// Bob requires preauthorization.
		env.EnableDepositAuth(bob)
		env.Close()

		// Setup credential-based DepositPreauth fails — amendment not supported.
		result := env.Submit(dp.AuthCredentials(bob, []dp.AuthorizeCredentials{
			{Issuer: issuer, CredTypeText: credType},
		}).Build())
		require.Equal(t, "temDISABLED", result.Code)
		env.Close()

		// But can create old (account-based) DepositPreauth.
		result = env.Submit(dp.Auth(bob, alice).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Alice can't pay with credentials — amendment not enabled.
		invalidIdx := "0E0B04ED60588A758B67E21FBBE95AC5A63598BA951761DC0EC9C08D7E01E034"
		result = env.Submit(
			payment.Pay(alice, bob, uint64(jtx.XRP(10))).
				CredentialIDs([]string{invalidIdx}).
				Build(),
		)
		require.Equal(t, "temDISABLED", result.Code)
		env.Close()
	})

	// ---- Payment with credentials ----
	t.Run("PaymentWithCredentials", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		env.FundAmount(issuer, uint64(jtx.XRP(5000)))
		env.FundAmount(alice, uint64(jtx.XRP(5000)))
		env.FundAmount(bob, uint64(jtx.XRP(5000)))
		env.FundAmount(john, uint64(jtx.XRP(5000)))
		env.Close()

		// Issuer creates credential for Alice, Alice hasn't accepted yet.
		result := env.Submit(credential.CredentialCreateText(issuer, alice, credType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Get the credential index.
		credIdx := dp.CredentialIndexHex(alice, issuer, credType)

		// Bob requires preauthorization.
		env.EnableDepositAuth(bob)
		env.Close()

		// Bob accepts payments from accounts with credentials signed by 'issuer'.
		result = env.Submit(dp.AuthCredentials(bob, []dp.AuthorizeCredentials{
			{Issuer: issuer, CredTypeText: credType},
		}).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Alice can't pay — empty credentials array.
		result = env.Submit(
			payment.Pay(alice, bob, uint64(jtx.XRP(100))).
				CredentialIDs([]string{}).
				Build(),
		)
		require.Equal(t, "temMALFORMED", result.Code)
		env.Close()

		// Alice can't pay — not accepted credentials.
		result = env.Submit(
			payment.Pay(alice, bob, uint64(jtx.XRP(100))).
				CredentialIDs([]string{credIdx}).
				Build(),
		)
		require.Equal(t, "tecBAD_CREDENTIALS", result.Code)
		env.Close()

		// Alice accepts the credentials.
		result = env.Submit(credential.CredentialAcceptText(alice, issuer, credType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Now alice can pay.
		result = env.Submit(
			payment.Pay(alice, bob, uint64(jtx.XRP(100))).
				CredentialIDs([]string{credIdx}).
				Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Alice can pay maria without depositPreauth enabled (credentials are optional).
		result = env.Submit(
			payment.Pay(alice, maria, uint64(jtx.XRP(250))).
				CredentialIDs([]string{credIdx}).
				Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// john can accept payment with old (account-based) DepositPreauth and valid credentials.
		env.EnableDepositAuth(john)
		result = env.Submit(dp.Auth(john, alice).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(
			payment.Pay(alice, john, uint64(jtx.XRP(100))).
				CredentialIDs([]string{credIdx}).
				Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// ---- Payment failure with invalid credentials ----
	t.Run("InvalidCredentials", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		env.FundAmount(issuer, uint64(jtx.XRP(10000)))
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.FundAmount(maria, uint64(jtx.XRP(10000)))
		env.Close()

		// Issuer creates credential for alice, then alice accepts.
		result := env.Submit(credential.CredentialCreateText(issuer, alice, credType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(credential.CredentialAcceptText(alice, issuer, credType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		credIdx := dp.CredentialIndexHex(alice, issuer, credType)

		// Success: destination didn't enable preauthorization, so valid credentials won't fail.
		result = env.Submit(
			payment.Pay(alice, bob, uint64(jtx.XRP(100))).
				CredentialIDs([]string{credIdx}).
				Build(),
		)
		jtx.RequireTxSuccess(t, result)

		// Bob requires preauthorization.
		env.EnableDepositAuth(bob)
		env.Close()

		// Fail: destination didn't setup DepositPreauth object for these credentials.
		result = env.Submit(
			payment.Pay(alice, bob, uint64(jtx.XRP(100))).
				CredentialIDs([]string{credIdx}).
				Build(),
		)
		require.Equal(t, "tecNO_PERMISSION", result.Code)

		// Bob tries to setup DepositPreauth with duplicates — not allowed.
		result = env.Submit(dp.AuthCredentials(bob, []dp.AuthorizeCredentials{
			{Issuer: issuer, CredTypeText: credType},
			{Issuer: issuer, CredTypeText: credType},
		}).Build())
		require.Equal(t, "temMALFORMED", result.Code)

		// Bob sets up DepositPreauth correctly.
		result = env.Submit(dp.AuthCredentials(bob, []dp.AuthorizeCredentials{
			{Issuer: issuer, CredTypeText: credType},
		}).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Alice can't pay with non-existing credentials.
		invalidIdx := "0E0B04ED60588A758B67E21FBBE95AC5A63598BA951761DC0EC9C08D7E01E034"
		result = env.Submit(
			payment.Pay(alice, bob, uint64(jtx.XRP(100))).
				CredentialIDs([]string{invalidIdx}).
				Build(),
		)
		require.Equal(t, "tecBAD_CREDENTIALS", result.Code)

		// maria can't pay using alice's credentials.
		result = env.Submit(
			payment.Pay(maria, bob, uint64(jtx.XRP(100))).
				CredentialIDs([]string{credIdx}).
				Build(),
		)
		require.Equal(t, "tecBAD_CREDENTIALS", result.Code)

		// Create another valid credential for alice with different type.
		credType2 := "fghij"
		result = env.Submit(credential.CredentialCreateText(issuer, alice, credType2).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(credential.CredentialAcceptText(alice, issuer, credType2).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		credIdx2 := dp.CredentialIndexHex(alice, issuer, credType2)

		// Alice can't pay with invalid set of valid credentials (wrong combination).
		result = env.Submit(
			payment.Pay(alice, bob, uint64(jtx.XRP(100))).
				CredentialIDs([]string{credIdx, credIdx2}).
				Build(),
		)
		require.Equal(t, "tecNO_PERMISSION", result.Code)

		// Error: duplicate credentials.
		result = env.Submit(
			payment.Pay(alice, bob, uint64(jtx.XRP(100))).
				CredentialIDs([]string{credIdx, credIdx}).
				Build(),
		)
		require.Equal(t, "temMALFORMED", result.Code)

		// Alice can pay with the correct single credential.
		result = env.Submit(
			payment.Pay(alice, bob, uint64(jtx.XRP(100))).
				CredentialIDs([]string{credIdx}).
				Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})
}

// --------------------------------------------------------------------------
// testCredentialsCreation
// Reference: rippled DepositPreauth_test::testCredentialsCreation (lines 1023-1193)
// --------------------------------------------------------------------------

func TestDepositPreauth_CredentialsCreation(t *testing.T) {
	credType := "abcde"
	credTypeHex := hex.EncodeToString([]byte(credType))

	issuer := jtx.NewAccount("issuer")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env := jtx.NewTestEnv(t)

	env.FundAmount(issuer, uint64(jtx.XRP(5000)))
	env.FundAmount(alice, uint64(jtx.XRP(5000)))
	env.FundAmount(bob, uint64(jtx.XRP(5000)))
	env.Close()

	// Both AuthorizeCredentials and UnauthorizeCredentials.
	t.Run("BothAuthAndUnauthCredentials", func(t *testing.T) {
		raw := dp.Raw(bob.Address).Fee("10").Sequence(env.Seq(bob))
		raw.AuthorizeCredentials([]depositpreauth.CredentialWrapper{{
			Credential: depositpreauth.CredentialSpec{
				Issuer: issuer.Address, CredentialType: credTypeHex,
			},
		}})
		raw.UnauthorizeCredentials([]depositpreauth.CredentialWrapper{})
		result := env.Submit(raw.Build())
		require.Equal(t, "temMALFORMED", result.Code)
	})

	// Both Unauthorize and AuthorizeCredentials.
	t.Run("UnauthAndAuthCreds", func(t *testing.T) {
		raw := dp.Raw(bob.Address).Fee("10").Sequence(env.Seq(bob))
		raw.Unauthorize(issuer.Address)
		raw.AuthorizeCredentials([]depositpreauth.CredentialWrapper{{
			Credential: depositpreauth.CredentialSpec{
				Issuer: issuer.Address, CredentialType: credTypeHex,
			},
		}})
		result := env.Submit(raw.Build())
		require.Equal(t, "temMALFORMED", result.Code)
	})

	// Both Authorize and AuthorizeCredentials.
	t.Run("AuthAndAuthCreds", func(t *testing.T) {
		raw := dp.Raw(bob.Address).Fee("10").Sequence(env.Seq(bob))
		raw.Authorize(issuer.Address)
		raw.AuthorizeCredentials([]depositpreauth.CredentialWrapper{{
			Credential: depositpreauth.CredentialSpec{
				Issuer: issuer.Address, CredentialType: credTypeHex,
			},
		}})
		result := env.Submit(raw.Build())
		require.Equal(t, "temMALFORMED", result.Code)
	})

	// Both Unauthorize and UnauthorizeCredentials.
	t.Run("UnauthAndUnauthCreds", func(t *testing.T) {
		raw := dp.Raw(bob.Address).Fee("10").Sequence(env.Seq(bob))
		raw.Unauthorize(issuer.Address)
		raw.UnauthorizeCredentials([]depositpreauth.CredentialWrapper{{
			Credential: depositpreauth.CredentialSpec{
				Issuer: issuer.Address, CredentialType: credTypeHex,
			},
		}})
		result := env.Submit(raw.Build())
		require.Equal(t, "temMALFORMED", result.Code)
	})

	// Both Authorize and UnauthorizeCredentials.
	t.Run("AuthAndUnauthCreds", func(t *testing.T) {
		raw := dp.Raw(bob.Address).Fee("10").Sequence(env.Seq(bob))
		raw.Authorize(issuer.Address)
		raw.UnauthorizeCredentials([]depositpreauth.CredentialWrapper{{
			Credential: depositpreauth.CredentialSpec{
				Issuer: issuer.Address, CredentialType: credTypeHex,
			},
		}})
		result := env.Submit(raw.Build())
		require.Equal(t, "temMALFORMED", result.Code)
	})

	// AuthorizeCredentials is empty.
	t.Run("EmptyAuthCreds", func(t *testing.T) {
		result := env.Submit(dp.AuthCredentials(bob, []dp.AuthorizeCredentials{}).Build())
		require.Equal(t, "temARRAY_EMPTY", result.Code)
	})

	// Invalid issuer (zero account).
	t.Run("InvalidIssuer", func(t *testing.T) {
		raw := dp.Raw(bob.Address).Fee("10").Sequence(env.Seq(bob))
		raw.AuthorizeCredentials([]depositpreauth.CredentialWrapper{{
			Credential: depositpreauth.CredentialSpec{
				Issuer:         xrpAccount,
				CredentialType: credTypeHex,
			},
		}})
		result := env.Submit(raw.Build())
		require.Equal(t, "temINVALID_ACCOUNT_ID", result.Code)
	})

	// Empty credential type.
	t.Run("EmptyCredType", func(t *testing.T) {
		result := env.Submit(dp.AuthCredentials(bob, []dp.AuthorizeCredentials{
			{Issuer: issuer, CredTypeText: ""},
		}).Build())
		require.Equal(t, "temMALFORMED", result.Code)
	})

	// More than 8 credentials.
	t.Run("TooManyCredentials", func(t *testing.T) {
		accounts := make([]*jtx.Account, 9)
		for i := range accounts {
			accounts[i] = jtx.NewAccount(fmt.Sprintf("cred%d", i))
			env.FundAmount(accounts[i], uint64(jtx.XRP(5000)))
		}
		env.Close()

		creds := make([]dp.AuthorizeCredentials, 9)
		for i, acc := range accounts {
			creds[i] = dp.AuthorizeCredentials{Issuer: acc, CredTypeText: credType}
		}
		result := env.Submit(dp.AuthCredentials(bob, creds).Build())
		require.Equal(t, "temARRAY_TOO_LARGE", result.Code)
	})

	// Non-existing issuer.
	t.Run("NonExistingIssuer", func(t *testing.T) {
		rick := jtx.NewAccount("rick")
		result := env.Submit(dp.AuthCredentials(bob, []dp.AuthorizeCredentials{
			{Issuer: rick, CredTypeText: credType},
		}).Build())
		require.Equal(t, "tecNO_ISSUER", result.Code)
		env.Close()
	})

	// Insufficient reserve.
	t.Run("InsufficientReserve", func(t *testing.T) {
		john := jtx.NewAccount("john")
		env.FundAmount(john, env.ReserveBase())
		env.Close()

		result := env.Submit(dp.AuthCredentials(john, []dp.AuthorizeCredentials{
			{Issuer: issuer, CredTypeText: credType},
		}).Build())
		require.Equal(t, "tecINSUFFICIENT_RESERVE", result.Code)
	})

	// No deposit object exists for unauthorize.
	t.Run("NoEntryForUnauth", func(t *testing.T) {
		result := env.Submit(dp.UnauthCredentials(bob, []dp.AuthorizeCredentials{
			{Issuer: issuer, CredTypeText: credType},
		}).Build())
		require.Equal(t, "tecNO_ENTRY", result.Code)
	})

	t.Run("CredentialAuthorizationLifecycle", func(t *testing.T) {
		preauthKey := keylet.DepositPreauthCredentials(bob.ID, []keylet.CredentialPair{{
			Issuer: issuer.ID, CredentialType: []byte(credType),
		}})
		jtx.RequireOwnerCount(t, env, bob, 0)
		result := env.Submit(dp.AuthCredentials(bob, []dp.AuthorizeCredentials{
			{Issuer: issuer, CredTypeText: credType},
		}).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireLedgerEntryExists(t, env, preauthKey)
		jtx.RequireOwnerDirectoryContains(t, env, bob, preauthKey.Key, true)
		jtx.RequireOwnerCount(t, env, bob, 1)
		data, err := env.LedgerEntry(preauthKey)
		require.NoError(t, err)
		stored := &ledgerentry.DepositPreauth{}
		require.NoError(t, stored.Decode(data))
		require.Equal(t, bob.Address, stored.Account)
		require.Equal(t, "0", stored.OwnerNode)
		require.Equal(t, []any{map[string]any{
			"Credential": map[string]any{
				"Issuer": issuer.Address, "CredentialType": hex.EncodeToString([]byte(credType)),
			},
		}}, stored.AuthorizeCredentials)

		result = env.Submit(dp.AuthCredentials(bob, []dp.AuthorizeCredentials{
			{Issuer: issuer, CredTypeText: credType},
		}).Build())
		require.Equal(t, "tecDUPLICATE", result.Code)
		env.Close()
		jtx.RequireLedgerEntryExists(t, env, preauthKey)
		jtx.RequireOwnerCount(t, env, bob, 1)

		result = env.Submit(dp.UnauthCredentials(bob, []dp.AuthorizeCredentials{
			{Issuer: issuer, CredTypeText: credType},
		}).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireLedgerEntryNotExists(t, env, preauthKey)
		jtx.RequireOwnerDirectoryContains(t, env, bob, preauthKey.Key, false)
		jtx.RequireOwnerCount(t, env, bob, 0)
	})
}

// --------------------------------------------------------------------------
// testExpiredCreds
// Reference: rippled DepositPreauth_test::testExpiredCreds (lines 1195-1430)
// --------------------------------------------------------------------------

func TestDepositPreauth_ExpiredCreds(t *testing.T) {
	credType := "abcde"
	credType2 := "fghijkl"

	issuer := jtx.NewAccount("issuer")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	gw := jtx.NewAccount("gw")

	// ---- Payment failure with expired credentials ----
	t.Run("ExpiredPayment", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		env.FundAmount(issuer, uint64(jtx.XRP(10000)))
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.FundAmount(gw, uint64(jtx.XRP(10000)))
		env.Close()

		// Issuer creates credential for alice with expiration (current time + 60s).
		now := env.NowRipple()
		expiration := now + 60
		result := env.Submit(
			credential.CredentialCreateText(issuer, alice, credType).
				Expiration(expiration).
				Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Alice accepts credentials.
		result = env.Submit(credential.CredentialAcceptText(alice, issuer, credType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Issuer creates non-expiring credential for alice (expiration far in the future).
		now = env.NowRipple()
		result = env.Submit(
			credential.CredentialCreateText(issuer, alice, credType2).
				Expiration(now + 1000).
				Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(credential.CredentialAcceptText(alice, issuer, credType2).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		jtx.RequireOwnerCount(t, env, issuer, 0)
		jtx.RequireOwnerCount(t, env, alice, 2)

		credIdx := dp.CredentialIndexHex(alice, issuer, credType)
		credIdx2 := dp.CredentialIndexHex(alice, issuer, credType2)

		// Bob requires preauthorization.
		env.EnableDepositAuth(bob)
		env.Close()

		// Bob sets up credential-based preauth for both credential types.
		result = env.Submit(dp.AuthCredentials(bob, []dp.AuthorizeCredentials{
			{Issuer: issuer, CredTypeText: credType},
			{Issuer: issuer, CredTypeText: credType2},
		}).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Alice can pay (credentials not yet expired).
		result = env.Submit(
			payment.Pay(alice, bob, uint64(jtx.XRP(100))).
				CredentialIDs([]string{credIdx, credIdx2}).
				Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		env.Close() // Extra close to advance time

		// Credentials have now expired. Alice can't pay.
		result = env.Submit(
			payment.Pay(alice, bob, uint64(jtx.XRP(100))).
				CredentialIDs([]string{credIdx, credIdx2}).
				Build(),
		)
		require.Equal(t, "tecEXPIRED", result.Code)
		env.Close()

		// Expired credential should be deleted.
		credKey := jtx.CredentialKeylet(alice, issuer, credType)
		require.False(t, env.LedgerEntryExists(credKey),
			"expired credential should be deleted from ledger")

		// Non-expired credential should still be present.
		credKey2 := jtx.CredentialKeylet(alice, issuer, credType2)
		require.True(t, env.LedgerEntryExists(credKey2),
			"non-expired credential should still exist")

		jtx.RequireOwnerCount(t, env, issuer, 0)
		jtx.RequireOwnerCount(t, env, alice, 1) // only credType2 remains

		// Additional test: issuer creates credential for gw with short expiration.
		now = env.NowRipple()
		result = env.Submit(
			credential.CredentialCreateText(issuer, gw, credType).
				Expiration(now + 40).
				Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(credential.CredentialAcceptText(gw, issuer, credType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		gwCredIdx := dp.CredentialIndexHex(gw, issuer, credType)

		jtx.RequireOwnerCount(t, env, issuer, 0)
		jtx.RequireOwnerCount(t, env, gw, 1)

		// Advance time past expiration.
		env.Close()
		env.Close()
		env.Close()

		// Payment with expired credentials fails.
		usd150 := tx.NewIssuedAmountFromFloat64(150, "USD", gw.Address)
		result = env.Submit(
			payment.PayIssued(gw, bob, usd150).
				CredentialIDs([]string{gwCredIdx}).
				Build(),
		)
		require.Equal(t, "tecEXPIRED", result.Code)
		env.Close()

		// Expired credential should be deleted.
		gwCredKey := jtx.CredentialKeylet(gw, issuer, credType)
		require.False(t, env.LedgerEntryExists(gwCredKey))
		jtx.RequireOwnerCount(t, env, issuer, 0)
		jtx.RequireOwnerCount(t, env, gw, 0)
	})

	// ---- Escrow failure with expired credentials ----
	t.Run("ExpiredEscrow", func(t *testing.T) {
		zelda := jtx.NewAccount("zelda")

		env := jtx.NewTestEnv(t)

		env.FundAmount(issuer, uint64(jtx.XRP(5000)))
		env.FundAmount(alice, uint64(jtx.XRP(5000)))
		env.FundAmount(bob, uint64(jtx.XRP(5000)))
		env.FundAmount(zelda, uint64(jtx.XRP(5000)))
		env.Close()

		// Issuer creates credential for zelda with short expiration.
		now := env.NowRipple()
		result := env.Submit(
			credential.CredentialCreateText(issuer, zelda, credType).
				Expiration(now + 50).
				Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Zelda accepts credentials.
		result = env.Submit(credential.CredentialAcceptText(zelda, issuer, credType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		credIdx := dp.CredentialIndexHex(zelda, issuer, credType)

		// Bob requires preauthorization.
		env.EnableDepositAuth(bob)
		env.Close()

		// Bob sets up credential-based preauth.
		result = env.Submit(dp.AuthCredentials(bob, []dp.AuthorizeCredentials{
			{Issuer: issuer, CredTypeText: credType},
		}).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		aliceSeq := env.Seq(alice)
		escrowKey := keylet.Escrow(alice.ID, aliceSeq)
		bobBalance := env.Balance(bob)
		result = env.Submit(
			escrow.EscrowCreate(alice, bob, jtx.XRP(1000)).
				FinishAfter(env.NowRipple() + 1).
				Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		require.True(t, env.LedgerEntryExists(escrowKey))
		jtx.RequireOwnerCount(t, env, alice, 1)
		require.Equal(t, bobBalance, env.Balance(bob))

		result = env.Submit(
			escrow.EscrowFinish(zelda, alice, aliceSeq).
				CredentialIDs([]string{}).
				Build(),
		)
		require.Equal(t, "temMALFORMED", result.Code)
		env.Close()
		require.True(t, env.LedgerEntryExists(escrowKey))

		invalidIdx := "0E0B04ED60588A758B67E21FBBE95AC5A63598BA951761DC0EC9C08D7E01E034"
		result = env.Submit(
			escrow.EscrowFinish(zelda, alice, aliceSeq).
				CredentialIDs([]string{invalidIdx}).
				Build(),
		)
		require.Equal(t, "tecBAD_CREDENTIALS", result.Code)
		env.Close()
		require.True(t, env.LedgerEntryExists(escrowKey))

		result = env.Submit(
			escrow.EscrowFinish(zelda, alice, aliceSeq).
				CredentialIDs([]string{credIdx}).
				Fee(1500).
				Build(),
		)
		require.Equal(t, "tecEXPIRED", result.Code)
		env.Close()

		zeldaCredKey := jtx.CredentialKeylet(zelda, issuer, credType)
		require.False(t, env.LedgerEntryExists(zeldaCredKey))
		require.True(t, env.LedgerEntryExists(escrowKey))
		jtx.RequireOwnerCount(t, env, zelda, 0)
		jtx.RequireOwnerCount(t, env, alice, 1)
		require.Equal(t, bobBalance, env.Balance(bob))
	})
}

// --------------------------------------------------------------------------
// testSortingCredentials
// Reference: rippled DepositPreauth_test::testSortingCredentials (lines 1432-1559)
// --------------------------------------------------------------------------

func TestDepositPreauth_SortingCredentials(t *testing.T) {
	stock := jtx.NewAccount("stock")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env := jtx.NewTestEnv(t)

	env.FundAmount(stock, uint64(jtx.XRP(5000)))
	env.FundAmount(alice, uint64(jtx.XRP(5000)))
	env.FundAmount(bob, uint64(jtx.XRP(5000)))

	// Create 8 issuers (a-h) with matching credential types.
	issuers := make([]*jtx.Account, 8)
	credTypes := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	for i := range issuers {
		issuers[i] = jtx.NewAccount(credTypes[i])
		env.FundAmount(issuers[i], uint64(jtx.XRP(5000)))
	}
	env.Close()

	// Build credentials list.
	credentials := make([]dp.AuthorizeCredentials, 8)
	for i := range credentials {
		credentials[i] = dp.AuthorizeCredentials{
			Issuer:       issuers[i],
			CredTypeText: credTypes[i],
		}
	}
	credentials[1].Issuer = credentials[0].Issuer
	expectedCredentials := append([]dp.AuthorizeCredentials(nil), credentials...)
	sort.Slice(expectedCredentials, func(i, j int) bool {
		if cmp := bytes.Compare(expectedCredentials[i].Issuer.ID[:], expectedCredentials[j].Issuer.ID[:]); cmp != 0 {
			return cmp < 0
		}
		return expectedCredentials[i].CredTypeText < expectedCredentials[j].CredTypeText
	})
	credentialPairs := make([]keylet.CredentialPair, len(credentials))
	for i, credential := range credentials {
		credentialPairs[i] = keylet.CredentialPair{
			Issuer:         credential.Issuer.ID,
			CredentialType: []byte(credential.CredTypeText),
		}
	}
	preauthKey := keylet.DepositPreauthCredentials(stock.ID, credentialPairs)

	// Sorting in ledger object: credentials should be sorted regardless of input order.
	t.Run("SortingInObject", func(t *testing.T) {
		for i := range 10 {
			// Rotate the credentials array to get different orderings.
			rotated := make([]dp.AuthorizeCredentials, len(credentials))
			copy(rotated, credentials)
			// Simple rotation by i positions.
			for j := range rotated {
				rotated[j] = credentials[(j+i)%len(credentials)]
			}

			result := env.Submit(dp.AuthCredentials(stock, rotated).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			data, err := env.LedgerEntry(preauthKey)
			require.NoError(t, err)
			stored := &ledgerentry.DepositPreauth{}
			require.NoError(t, stored.Decode(data))
			require.Len(t, stored.AuthorizeCredentials, len(expectedCredentials))
			for index, value := range stored.AuthorizeCredentials {
				wrapper, ok := value.(map[string]any)
				require.True(t, ok)
				credential, ok := wrapper["Credential"].(map[string]any)
				require.True(t, ok)
				require.Equal(t, expectedCredentials[index].Issuer.Address, credential["Issuer"])
				require.Equal(t,
					hex.EncodeToString([]byte(expectedCredentials[index].CredTypeText)),
					credential["CredentialType"],
				)
			}

			deleteOrder := make([]dp.AuthorizeCredentials, len(credentials))
			for j := range deleteOrder {
				deleteOrder[j] = credentials[(len(credentials)-1-j+i)%len(credentials)]
			}
			result = env.Submit(dp.UnauthCredentials(stock, deleteOrder).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
			require.False(t, env.LedgerEntryExists(preauthKey))
		}
	})

	// Duplicate detection in DepositPreauth params.
	t.Run("DuplicateInParams", func(t *testing.T) {
		// Create once.
		result := env.Submit(dp.AuthCredentials(stock, credentials).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Re-create with any shuffled order — should get tecDUPLICATE.
		for i := range 10 {
			rotated := make([]dp.AuthorizeCredentials, len(credentials))
			copy(rotated, credentials)
			for j := range rotated {
				rotated[j] = credentials[(j+i+1)%len(credentials)]
			}

			result := env.Submit(dp.AuthCredentials(stock, rotated).Build())
			require.Equal(t, "tecDUPLICATE", result.Code)
		}

		result = env.Submit(dp.UnauthCredentials(stock, credentials).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		require.False(t, env.LedgerEntryExists(preauthKey))
	})

	// Duplicate credentials in DepositPreauth params.
	t.Run("DuplicateCredentials", func(t *testing.T) {
		// Take 7 credentials and append a duplicate.
		copyCredentials := credentials[:7]

		for _, c := range copyCredentials {
			withDup := make([]dp.AuthorizeCredentials, len(copyCredentials)+1)
			copy(withDup, copyCredentials)
			withDup[len(copyCredentials)] = c

			result := env.Submit(dp.AuthCredentials(stock, withDup).Build())
			require.Equal(t, "temMALFORMED", result.Code)
		}
	})

	// Duplicate credentials in payment params.
	t.Run("DuplicateCredentialInPayment", func(t *testing.T) {
		// Create credentials for alice and save their hashes.
		credentialIDs := make([]string, len(credentials))
		for i, c := range credentials {
			result := env.Submit(credential.CredentialCreateText(c.Issuer, alice, c.CredTypeText).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
			result = env.Submit(credential.CredentialAcceptText(alice, c.Issuer, c.CredTypeText).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			credentialIDs[i] = dp.CredentialIndexHex(alice, c.Issuer, c.CredTypeText)
		}

		// Check duplicates in payment params.
		for _, h := range credentialIDs {
			withDup := make([]string, len(credentialIDs)+1)
			copy(withDup, credentialIDs)
			withDup[len(credentialIDs)] = h

			result := env.Submit(
				payment.Pay(alice, bob, uint64(jtx.XRP(100))).
					CredentialIDs(withDup).
					Build(),
			)
			require.Equal(t, "temMALFORMED", result.Code)
		}
	})
}
