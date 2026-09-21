package check_test

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	checkbuilder "github.com/LeJamon/go-xrpl/internal/testing/check"
	"github.com/LeJamon/go-xrpl/internal/testing/metadata"
	"github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

const (
	fixCleanup340     = "fixCleanup3_4_0"
	maxTrustLineLimit = "9999999999999999e80"
)

func TestCheckCashIssuerDestinationCleanup(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, sourceLow := range []bool{true, false} {
			for _, cashKind := range []string{"Amount", "DeliverMin"} {
				name := fmt.Sprintf("cleanup-%v/source-low-%v/%s", cleanup, sourceLow, cashKind)
				t.Run(name, func(t *testing.T) {
					testCheckCashIssuerDestinationCleanup(t, cleanup, sourceLow, cashKind)
				})
			}
		}
	}
}

func testCheckCashIssuerDestinationCleanup(t *testing.T, cleanup, sourceLow bool, cashKind string) {
	t.Helper()
	env, source, issuer, amount, checkID, checkKey, lineKey := newIssuerCheckFixture(t, cleanup, sourceLow)

	issuerSequence := env.Seq(issuer)
	sourceSequence := env.Seq(source)
	issuerBalance := env.Balance(issuer)
	result := env.Submit(issuerCheckCash(issuer, checkID, amount, cashKind).Build())
	jtx.RequireTxSuccess(t, result)
	requireDeliveredAmount(t, result, amount)

	jtx.RequireSequence(t, env, issuer, issuerSequence+1)
	require.Equal(t, issuerBalance-env.BaseFee(), env.Balance(issuer))
	jtx.RequireSequence(t, env, source, sourceSequence)

	checkNode := metadata.FindNode(result.Metadata, "DeletedNode", "Check")
	require.NotNil(t, checkNode)
	require.Equal(t, strings.ToUpper(hex.EncodeToString(checkKey.Key[:])), checkNode.LedgerIndex)
	jtx.RequireLedgerEntryNotExists(t, env, checkKey)
	jtx.RequireOwnerDirectoryContains(t, env, source, checkKey.Key, false)
	jtx.RequireOwnerDirectoryContains(t, env, issuer, checkKey.Key, false)

	// The legacy HighLimit waiver prevents deletion when the source is high.
	wantLine := !cleanup && !sourceLow
	require.Equal(t, wantLine, env.TrustLineExists(source, issuer, "USD"))
	lineDeleted := metadata.FindNode(result.Metadata, "DeletedNode", "RippleState")
	lineModified := metadata.FindNode(result.Metadata, "ModifiedNode", "RippleState")
	if wantLine {
		require.Nil(t, lineDeleted)
		require.NotNil(t, lineModified)
		requireRippleStateLimits(t, lineModified.FinalFields, source, issuer, "0", "0")
		jtx.RequireOwnerDirectoryContains(t, env, source, lineKey.Key, true)
		jtx.RequireOwnerDirectoryContains(t, env, issuer, lineKey.Key, true)
		jtx.RequireOwnerCount(t, env, source, 1)
	} else {
		require.NotNil(t, lineDeleted)
		require.Nil(t, lineModified)
		require.Equal(t, strings.ToUpper(hex.EncodeToString(lineKey.Key[:])), lineDeleted.LedgerIndex)
		highValue := "0"
		if !cleanup && sourceLow {
			highValue = maxTrustLineLimit
		}
		requireRippleStateLimits(t, lineDeleted.FinalFields, source, issuer, "0", highValue)
		jtx.RequireOwnerDirectoryContains(t, env, source, lineKey.Key, false)
		jtx.RequireOwnerDirectoryContains(t, env, issuer, lineKey.Key, false)
		jtx.RequireOwnerCount(t, env, source, 0)
	}
	jtx.RequireOwnerCount(t, env, issuer, 0)
	jtx.RequireIOUBalance(t, env, source, issuer, "USD", 0)
}

func TestCheckCashIssuerDestinationInsufficientFunds(t *testing.T) {
	env, source, issuer, _, checkID, checkKey, lineKey := newIssuerCheckFixture(t, true, false)
	partial := tx.NewIssuedAmountFromFloat64(500, "USD", issuer.Address)
	jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(source, issuer, partial).Build()))
	env.Close()
	jtx.RequireIOUBalance(t, env, source, issuer, "USD", 400)

	sourceSequence := env.Seq(source)
	issuerSequence := env.Seq(issuer)
	issuerBalance := env.Balance(issuer)
	result := env.Submit(checkbuilder.CheckCashDeliverMin(issuer, checkID,
		tx.NewIssuedAmountFromFloat64(450, "USD", issuer.Address)).Build())
	jtx.RequireTxClaimed(t, result, "tecPATH_PARTIAL")
	jtx.RequireSequence(t, env, source, sourceSequence)
	jtx.RequireSequence(t, env, issuer, issuerSequence+1)
	require.Equal(t, issuerBalance-env.BaseFee(), env.Balance(issuer))
	require.NotNil(t, result.Metadata)
	require.Nil(t, result.Metadata.DeliveredAmount)
	require.Len(t, result.Metadata.AffectedNodes, 1)
	require.NotNil(t, metadata.FindNode(result.Metadata, "ModifiedNode", "AccountRoot"))
	jtx.RequireLedgerEntryExists(t, env, checkKey)
	jtx.RequireOwnerDirectoryContains(t, env, source, checkKey.Key, true)
	jtx.RequireOwnerDirectoryContains(t, env, issuer, checkKey.Key, true)
	jtx.RequireLedgerEntryExists(t, env, lineKey)
	jtx.RequireOwnerDirectoryContains(t, env, source, lineKey.Key, true)
	jtx.RequireOwnerDirectoryContains(t, env, issuer, lineKey.Key, true)
	jtx.RequireOwnerCount(t, env, source, 2)
	jtx.RequireOwnerCount(t, env, issuer, 0)
	jtx.RequireIOUBalance(t, env, source, issuer, "USD", 400)
}

func newIssuerCheckFixture(t *testing.T, cleanup, sourceLow bool) (*jtx.TestEnv, *jtx.Account, *jtx.Account, tx.Amount, string, keylet.Keylet, keylet.Keylet) {
	t.Helper()
	env := jtx.NewTestEnv(t)
	a := jtx.NewAccount("check-cash-issuer-a")
	b := jtx.NewAccount("check-cash-issuer-b")
	var source, issuer *jtx.Account
	if (state.CompareAccountIDs(a.ID, b.ID) < 0) == sourceLow {
		source, issuer = a, b
	} else {
		source, issuer = b, a
	}
	env.Fund(source, issuer)
	env.Close()

	amount := tx.NewIssuedAmountFromFloat64(900, "USD", issuer.Address)
	xrp := tx.NewXRPAmount(10_000)
	// Crossing offers creates a source-side RippleState with a zero trust
	// limit and a nonzero balance, matching the cleanup regression state.
	jtx.RequireTxSuccess(t, env.Submit(offer.OfferCreate(source, amount, xrp).Build()))
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(offer.OfferCreate(issuer, xrp, amount).Build()))
	env.Close()

	lineKey := keylet.Line(source.ID, issuer.ID, "USD")
	lineData, err := env.LedgerEntry(lineKey)
	require.NoError(t, err)
	line, err := state.ParseRippleState(lineData)
	require.NoError(t, err)
	sourceLimit := line.LowLimit
	if !sourceLow {
		sourceLimit = line.HighLimit
	}
	require.Equal(t, source.Address, sourceLimit.Issuer)
	require.True(t, sourceLimit.IsZero())
	jtx.RequireIOUBalance(t, env, source, issuer, "USD", 900)
	jtx.RequireOwnerDirectoryContains(t, env, source, lineKey.Key, true)
	jtx.RequireOwnerDirectoryContains(t, env, issuer, lineKey.Key, true)
	jtx.RequireOwnerCount(t, env, source, 1)
	jtx.RequireOwnerCount(t, env, issuer, 0)

	if cleanup {
		env.EnableFeature(fixCleanup340)
	} else {
		env.DisableFeature(fixCleanup340)
	}
	env.Close()
	require.Equal(t, cleanup, env.Rules().FixCleanup3_4_0Enabled(), "cleanup amendment state")

	checkSequence := env.Seq(source)
	checkKey := keylet.Check(source.ID, checkSequence)
	checkID := strings.ToUpper(hex.EncodeToString(checkKey.Key[:]))
	jtx.RequireTxSuccess(t, env.Submit(checkbuilder.CheckCreate(source, issuer, amount).Build()))
	env.Close()
	jtx.RequireOwnerCount(t, env, source, 2)
	return env, source, issuer, amount, checkID, checkKey, lineKey
}

func issuerCheckCash(account *jtx.Account, checkID string, amount tx.Amount, cashKind string) *checkbuilder.CheckCashBuilder {
	if cashKind == "DeliverMin" {
		return checkbuilder.CheckCashDeliverMin(account, checkID, amount)
	}
	return checkbuilder.CheckCashAmount(account, checkID, amount)
}

func requireRippleStateLimits(t *testing.T, fields map[string]any, source, issuer *jtx.Account, lowValue, highValue string) {
	t.Helper()
	low, high := source, issuer
	if state.CompareAccountIDs(source.ID, issuer.ID) > 0 {
		low, high = issuer, source
	}
	requireRippleStateLimit(t, fields, "LowLimit", low, lowValue)
	requireRippleStateLimit(t, fields, "HighLimit", high, highValue)
}

func requireRippleStateLimit(t *testing.T, fields map[string]any, field string, issuer *jtx.Account, value string) {
	t.Helper()
	limit, ok := fields[field].(map[string]any)
	require.True(t, ok, "%s must be an issued amount", field)
	require.Equal(t, issuer.Address, limit["issuer"])
	require.Equal(t, "USD", limit["currency"])
	require.Equal(t, value, limit["value"])
}

func TestCheckCashNonIssuerAutoTrustLineCleanup(t *testing.T) {
	t.Run("success-created-line", func(t *testing.T) {
		env, issuer, source, holder, amount, checkID, checkKey := newNonIssuerCashFixture(t, true, true, 25)
		holderSequence := env.Seq(holder)
		holderBalance := env.Balance(holder)
		sourceSequence := env.Seq(source)
		lineKey := keylet.Line(holder.ID, issuer.ID, "USD")

		result := env.Submit(checkbuilder.CheckCashAmount(holder, checkID, amount).Build())
		jtx.RequireTxSuccess(t, result)
		requireDeliveredAmount(t, result, amount)

		jtx.RequireSequence(t, env, holder, holderSequence+1)
		require.Equal(t, holderBalance-env.BaseFee(), env.Balance(holder))
		jtx.RequireSequence(t, env, source, sourceSequence)
		jtx.RequireIOUBalance(t, env, source, issuer, "USD", 25)
		jtx.RequireIOUBalance(t, env, holder, issuer, "USD", 25)
		jtx.RequireOwnerCount(t, env, source, 1)
		jtx.RequireOwnerCount(t, env, holder, 1)
		jtx.RequireOwnerCount(t, env, issuer, 0)
		jtx.RequireLedgerEntryNotExists(t, env, checkKey)
		jtx.RequireOwnerDirectoryContains(t, env, source, checkKey.Key, false)
		jtx.RequireOwnerDirectoryContains(t, env, holder, checkKey.Key, false)
		jtx.RequireOwnerDirectoryContains(t, env, holder, lineKey.Key, true)
		jtx.RequireOwnerDirectoryContains(t, env, issuer, lineKey.Key, true)

		createdLine := metadata.FindNode(result.Metadata, "CreatedNode", "RippleState")
		require.NotNil(t, createdLine)
		require.Equal(t, strings.ToUpper(hex.EncodeToString(lineKey.Key[:])), createdLine.LedgerIndex)
		require.NotNil(t, createdLine.NewFields)
		requireRippleStateLimits(t, createdLine.NewFields, holder, issuer, "0", "0")
	})

	t.Run("insufficient-reserve-keeps-check", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		lowReserve := env.ReserveBase() + env.ReserveIncrement() - 1
		issuer := jtx.NewAccount("check-cash-low-reserve-issuer")
		source := jtx.NewAccount("check-cash-low-reserve-source")
		holder := jtx.NewAccount("check-cash-low-reserve-destination")
		env.FundAmount(issuer, uint64(jtx.XRP(1000)))
		env.FundAmount(source, uint64(jtx.XRP(1000)))
		env.FundAmountNoRipple(holder, lowReserve)
		env.Close()
		usd := func(value float64) tx.Amount {
			return tx.NewIssuedAmountFromFloat64(value, "USD", issuer.Address)
		}
		jtx.RequireTxSuccess(t, env.Submit(trustset.TrustSet(source, usd(100)).Build()))
		env.Close()
		jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(issuer, source, usd(25)).Build()))
		env.Close()
		env.EnableFeature(fixCleanup340)
		env.Close()
		require.True(t, env.Rules().FixCleanup3_4_0Enabled())

		amount := usd(25)
		checkSequence := env.Seq(source)
		checkKey := keylet.Check(source.ID, checkSequence)
		checkID := strings.ToUpper(hex.EncodeToString(checkKey.Key[:]))
		jtx.RequireTxSuccess(t, env.Submit(checkbuilder.CheckCreate(source, holder, amount).Build()))
		env.Close()

		holderSequence := env.Seq(holder)
		holderBalance := env.Balance(holder)
		result := env.Submit(checkbuilder.CheckCashAmount(holder, checkID, amount).Build())
		jtx.RequireTxClaimed(t, result, "tecNO_LINE_INSUF_RESERVE")
		jtx.RequireSequence(t, env, holder, holderSequence+1)
		require.Equal(t, holderBalance-env.BaseFee(), env.Balance(holder))
		require.NotNil(t, result.Metadata)
		require.Nil(t, result.Metadata.DeliveredAmount)
		require.Len(t, result.Metadata.AffectedNodes, 1)
		require.NotNil(t, metadata.FindNode(result.Metadata, "ModifiedNode", "AccountRoot"))
		lineKey := keylet.Line(holder.ID, issuer.ID, "USD")
		jtx.RequireLedgerEntryExists(t, env, checkKey)
		jtx.RequireLedgerEntryNotExists(t, env, lineKey)
		jtx.RequireOwnerDirectoryContains(t, env, source, checkKey.Key, true)
		jtx.RequireOwnerDirectoryContains(t, env, holder, checkKey.Key, true)
		jtx.RequireOwnerDirectoryContains(t, env, holder, lineKey.Key, false)
		jtx.RequireOwnerDirectoryContains(t, env, issuer, lineKey.Key, false)
		jtx.RequireOwnerCount(t, env, source, 2)
		jtx.RequireOwnerCount(t, env, holder, 0)
		jtx.RequireIOUBalance(t, env, source, issuer, "USD", 25)
		jtx.RequireIOUBalance(t, env, holder, issuer, "USD", 0)
		require.Nil(t, metadata.FindNode(result.Metadata, "CreatedNode", "RippleState"))
	})

	t.Run("flow-failure-rolls-back-created-line", func(t *testing.T) {
		env, issuer, source, holder, amount, checkID, checkKey := newNonIssuerCashFixture(t, true, false, 25)
		lineKey := keylet.Line(holder.ID, issuer.ID, "USD")
		holderSequence := env.Seq(holder)
		holderBalance := env.Balance(holder)
		result := env.Submit(checkbuilder.CheckCashAmount(holder, checkID, amount).Build())
		require.Equal(t, "terNO_RIPPLE", result.Code)
		require.False(t, result.Success)
		require.Equal(t, holderSequence, env.Seq(holder))
		require.Equal(t, holderBalance, env.Balance(holder))
		require.Nil(t, result.Metadata)
		jtx.RequireLedgerEntryNotExists(t, env, lineKey)
		jtx.RequireLedgerEntryExists(t, env, checkKey)
		jtx.RequireOwnerDirectoryContains(t, env, source, checkKey.Key, true)
		jtx.RequireOwnerDirectoryContains(t, env, holder, checkKey.Key, true)
		jtx.RequireOwnerDirectoryContains(t, env, holder, lineKey.Key, false)
		jtx.RequireOwnerDirectoryContains(t, env, issuer, lineKey.Key, false)
		jtx.RequireOwnerCount(t, env, source, 2)
		jtx.RequireOwnerCount(t, env, holder, 0)
		jtx.RequireIOUBalance(t, env, source, issuer, "USD", 50)
		jtx.RequireIOUBalance(t, env, holder, issuer, "USD", 0)
	})
}

func newNonIssuerCashFixture(t *testing.T, cleanup, defaultRipple bool, sendMax float64) (*jtx.TestEnv, *jtx.Account, *jtx.Account, *jtx.Account, tx.Amount, string, keylet.Keylet) {
	t.Helper()
	env := jtx.NewTestEnv(t)
	issuer := jtx.NewAccount("check-cash-holder-issuer")
	source := jtx.NewAccount("check-cash-holder-source")
	holder := jtx.NewAccount("check-cash-holder-destination")
	if defaultRipple {
		env.Fund(issuer, source, holder)
	} else {
		env.FundNoRipple(issuer, source, holder)
	}
	env.Close()

	usd := func(value float64) tx.Amount {
		return tx.NewIssuedAmountFromFloat64(value, "USD", issuer.Address)
	}
	jtx.RequireTxSuccess(t, env.Submit(trustset.TrustSet(source, usd(100)).Build()))
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(issuer, source, usd(sendMax*2)).Build()))
	env.Close()

	if cleanup {
		env.EnableFeature(fixCleanup340)
	} else {
		env.DisableFeature(fixCleanup340)
	}
	env.Close()
	require.Equal(t, cleanup, env.Rules().FixCleanup3_4_0Enabled(), "cleanup amendment state")

	amount := usd(sendMax)
	checkSequence := env.Seq(source)
	checkKey := keylet.Check(source.ID, checkSequence)
	checkID := strings.ToUpper(hex.EncodeToString(checkKey.Key[:]))
	jtx.RequireTxSuccess(t, env.Submit(checkbuilder.CheckCreate(source, holder, amount).Build()))
	env.Close()
	return env, issuer, source, holder, amount, checkID, checkKey
}
