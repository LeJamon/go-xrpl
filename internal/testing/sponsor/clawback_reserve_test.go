package sponsor_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/clawback"
	"github.com/LeJamon/go-xrpl/internal/tx"
	trustsettx "github.com/LeJamon/go-xrpl/internal/tx/trustset"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type clawbackSponsorFixture struct {
	env            *jtx.TestEnv
	issuer         *jtx.Account
	holder         *jtx.Account
	reserveSponsor *jtx.Account
	lineKey        keylet.Keylet
}

type clawbackLineSides struct {
	issuerReserveFlag  uint32
	holderReserveFlag  uint32
	issuerNoRippleFlag uint32
	holderNoRippleFlag uint32
	issuerSponsor      string
	holderSponsor      string
	issuerQualityIn    uint32
	holderQualityIn    uint32
}

func newClawbackSponsorFixture(
	t *testing.T,
	issuerLow, sponsorIsIssuer bool,
) *clawbackSponsorFixture {
	t.Helper()

	env := jtx.NewTestEnv(t)
	env.EnableFeature("Sponsor")
	issuer := jtx.NewAccount("clawback-sponsor-issuer")
	holder := jtx.NewAccount("clawback-sponsor-holder")
	if keylet.IsLowAccount(issuer.ID, holder.ID) != issuerLow {
		issuer, holder = holder, issuer
	}
	require.Equal(t, issuerLow, keylet.IsLowAccount(issuer.ID, holder.ID))

	reserveSponsor := issuer
	if !sponsorIsIssuer {
		reserveSponsor = jtx.NewAccount("clawback-sponsor-third-party")
	}
	env.FundNoRipple(issuer)
	env.Fund(holder)
	if reserveSponsor != issuer {
		env.Fund(reserveSponsor)
	}
	env.Close()

	require.Equal(t, "tesSUCCESS", env.Submit(
		accountset.AccountSet(issuer).AllowClawback().Build(),
	).Code)
	env.Close()

	remaining := int32(1)
	require.Equal(t, "tesSUCCESS", setSponsorship(env, reserveSponsor, holder, 0, &remaining).Code)
	env.Close()

	create := trustsettx.NewTrustSet(
		holder.Address,
		tx.NewIssuedAmount(1_000, 0, "USD", issuer.Address),
	)
	create.Sponsor = reserveSponsor.Address
	reserveFlags := tx.SpfSponsorReserve
	create.SponsorFlags = &reserveFlags
	require.Equal(t, "tesSUCCESS", env.Submit(create).Code)
	env.Close()

	env.PayIOU(issuer, holder, issuer, "USD", 100)
	env.Close()

	clearLimit := trustsettx.NewTrustSet(
		holder.Address,
		tx.NewIssuedAmount(0, 0, "USD", issuer.Address),
	)
	require.Equal(t, "tesSUCCESS", env.Submit(clearLimit).Code)
	env.Close()

	// Change the issuer's account setting after the line is created. The
	// clawback reserve transition must not re-evaluate this counterparty state.
	require.Equal(t, "tesSUCCESS", env.Submit(
		accountset.AccountSet(issuer).DefaultRipple().Build(),
	).Code)
	env.Close()

	return &clawbackSponsorFixture{
		env:            env,
		issuer:         issuer,
		holder:         holder,
		reserveSponsor: reserveSponsor,
		lineKey:        keylet.Line(issuer.ID, holder.ID, "USD"),
	}
}

func clawbackLine(t *testing.T, env *jtx.TestEnv, key keylet.Keylet) *state.RippleState {
	t.Helper()
	data, err := env.LedgerEntry(key)
	require.NoError(t, err)
	require.NotNil(t, data)
	line, err := state.ParseRippleState(data)
	require.NoError(t, err)
	return line
}

func clawbackLineSideValues(
	issuer, holder *jtx.Account,
	line *state.RippleState,
) clawbackLineSides {
	if keylet.IsLowAccount(issuer.ID, holder.ID) {
		return clawbackLineSides{
			issuerReserveFlag:  state.LsfLowReserve,
			holderReserveFlag:  state.LsfHighReserve,
			issuerNoRippleFlag: state.LsfLowNoRipple,
			holderNoRippleFlag: state.LsfHighNoRipple,
			issuerSponsor:      line.LowSponsor,
			holderSponsor:      line.HighSponsor,
			issuerQualityIn:    line.LowQualityIn,
			holderQualityIn:    line.HighQualityIn,
		}
	}
	return clawbackLineSides{
		issuerReserveFlag:  state.LsfHighReserve,
		holderReserveFlag:  state.LsfLowReserve,
		issuerNoRippleFlag: state.LsfHighNoRipple,
		holderNoRippleFlag: state.LsfLowNoRipple,
		issuerSponsor:      line.HighSponsor,
		holderSponsor:      line.LowSponsor,
		issuerQualityIn:    line.HighQualityIn,
		holderQualityIn:    line.LowQualityIn,
	}
}

func requireClawbackReserveSponsorState(
	t *testing.T,
	f *clawbackSponsorFixture,
	line *state.RippleState,
	wantIssuerReserve, wantHolderReserve bool,
	wantIssuerSponsor, wantHolderSponsor string,
) {
	t.Helper()
	sides := clawbackLineSideValues(f.issuer, f.holder, line)
	wantFlags := sides.issuerNoRippleFlag
	if wantIssuerReserve {
		wantFlags |= sides.issuerReserveFlag
	}
	if wantHolderReserve {
		wantFlags |= sides.holderReserveFlag
	}
	require.Equal(t, wantFlags, line.Flags)
	require.Zero(t, line.Flags&sides.holderNoRippleFlag)
	require.Equal(t, wantIssuerSponsor, sides.issuerSponsor)
	require.Equal(t, wantHolderSponsor, sides.holderSponsor)
}

func TestClawbackSponsoredHolderReserveDeletion(t *testing.T) {
	testCases := []struct {
		name            string
		issuerLow       bool
		sponsorIsIssuer bool
	}{
		{name: "issuer-low/issuer-sponsors", issuerLow: true, sponsorIsIssuer: true},
		{name: "issuer-high/issuer-sponsors", issuerLow: false, sponsorIsIssuer: true},
		{name: "issuer-low/third-party-sponsors", issuerLow: true},
		{name: "issuer-high/third-party-sponsors", issuerLow: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newClawbackSponsorFixture(t, testCase.issuerLow, testCase.sponsorIsIssuer)
			lineBefore := clawbackLine(t, f.env, f.lineKey)
			requireClawbackReserveSponsorState(
				t, f, lineBefore, false, true, "", f.reserveSponsor.Address,
			)
			holderBefore := accountState(t, f.env, f.holder)
			issuerBefore := accountState(t, f.env, f.issuer)
			sponsorBefore := accountState(t, f.env, f.reserveSponsor)
			require.Equal(t, uint32(1), holderBefore.OwnerCount)
			require.Equal(t, uint32(1), holderBefore.SponsoredOwnerCount)
			require.Equal(t, uint32(1), sponsorBefore.SponsoringOwnerCount)

			partial := clawback.Claw(f.issuer, f.holder, "USD", 10).Build()
			partialResult := f.env.Submit(partial)
			require.Equal(t, "tesSUCCESS", partialResult.Code)
			f.env.Close()
			require.Equal(t, 90.0, f.env.BalanceIOU(f.holder, "USD", f.issuer))
			lineAfterPartial := clawbackLine(t, f.env, f.lineKey)
			require.Equal(t, lineBefore.Flags, lineAfterPartial.Flags)
			require.Equal(t, lineBefore.LowSponsor, lineAfterPartial.LowSponsor)
			require.Equal(t, lineBefore.HighSponsor, lineAfterPartial.HighSponsor)
			require.Equal(t, holderBefore.OwnerCount, accountState(t, f.env, f.holder).OwnerCount)
			require.Equal(t, holderBefore.SponsoredOwnerCount, accountState(t, f.env, f.holder).SponsoredOwnerCount)
			require.Equal(t, issuerBefore.OwnerCount, accountState(t, f.env, f.issuer).OwnerCount)
			require.Equal(t, sponsorBefore.SponsoringOwnerCount, accountState(t, f.env, f.reserveSponsor).SponsoringOwnerCount)
			partialLineNode := affectedNodeByKey(partialResult.Metadata, f.lineKey)
			require.NotNil(t, partialLineNode)
			require.Equal(t, "ModifiedNode", partialLineNode.NodeType)
			require.NotContains(t, partialLineNode.PreviousFields, "Flags")

			issuerOwnerCountBeforeTicket := accountState(t, f.env, f.issuer).OwnerCount
			var full tx.Transaction = clawback.Claw(f.issuer, f.holder, "USD", 90).Build()
			if testCase.sponsorIsIssuer {
				ticketSequence := f.env.CreateTickets(f.issuer, 1)
				f.env.Close()
				full = jtx.WithTicketSeq(full, ticketSequence)
			}
			holderBeforeFull := accountState(t, f.env, f.holder)
			issuerBeforeFull := accountState(t, f.env, f.issuer)
			sponsorBeforeFull := accountState(t, f.env, f.reserveSponsor)
			fullResult := f.env.Submit(full)
			require.Equal(t, "tesSUCCESS", fullResult.Code)
			f.env.Close()

			require.False(t, f.env.LedgerEntryExists(f.lineKey))
			require.Equal(t, 0.0, f.env.BalanceIOU(f.holder, "USD", f.issuer))
			holderAfter := accountState(t, f.env, f.holder)
			require.Equal(t, holderBeforeFull.OwnerCount-1, holderAfter.OwnerCount)
			require.Equal(t, holderBeforeFull.SponsoredOwnerCount-1, holderAfter.SponsoredOwnerCount)

			issuerAfter := accountState(t, f.env, f.issuer)
			wantIssuerOwnerCount := issuerBeforeFull.OwnerCount
			if testCase.sponsorIsIssuer {
				wantIssuerOwnerCount = issuerOwnerCountBeforeTicket
			}
			require.Equal(t, wantIssuerOwnerCount, issuerAfter.OwnerCount,
				"clawback must not add an issuer-side reserve")
			require.Equal(t, issuerBeforeFull.SponsoredOwnerCount, issuerAfter.SponsoredOwnerCount)
			if testCase.sponsorIsIssuer {
				require.Equal(t, issuerBeforeFull.SponsoringOwnerCount-1, issuerAfter.SponsoringOwnerCount)
				jtx.RequireTicketCount(t, f.env, f.issuer, 0)
			} else {
				require.Equal(t, issuerBeforeFull.SponsoringOwnerCount, issuerAfter.SponsoringOwnerCount)
			}

			sponsorAfter := accountState(t, f.env, f.reserveSponsor)
			wantSponsorOwnerCount := sponsorBeforeFull.OwnerCount
			if testCase.sponsorIsIssuer {
				wantSponsorOwnerCount = issuerOwnerCountBeforeTicket
			}
			require.Equal(t, wantSponsorOwnerCount, sponsorAfter.OwnerCount)
			require.Equal(t, sponsorBeforeFull.SponsoredOwnerCount, sponsorAfter.SponsoredOwnerCount)
			require.Equal(t, sponsorBeforeFull.SponsoringOwnerCount-1, sponsorAfter.SponsoringOwnerCount)
			require.False(t, accountState(t, f.env, f.holder).HasSponsor)
			require.Empty(t, accountState(t, f.env, f.holder).Sponsor)

			lineNode := affectedNodeByKey(fullResult.Metadata, f.lineKey)
			require.NotNil(t, lineNode)
			require.Equal(t, "DeletedNode", lineNode.NodeType)
			require.Equal(t, "RippleState", lineNode.LedgerEntryType)
			require.NotContains(t, lineNode.FinalFields, "LowSponsor")
			require.NotContains(t, lineNode.FinalFields, "HighSponsor")
			sponsorNode := affectedNodeByKey(fullResult.Metadata, keylet.Account(f.reserveSponsor.ID))
			require.NotNil(t, sponsorNode)
			require.Equal(t, "AccountRoot", sponsorNode.LedgerEntryType)
			require.Contains(t, sponsorNode.PreviousFields, "SponsoringOwnerCount")
		})
	}
}

func TestClawbackPreservesSponsoredIssuerReserve(t *testing.T) {
	for _, issuerLow := range []bool{true, false} {
		t.Run(map[bool]string{true: "issuer-low", false: "issuer-high"}[issuerLow], func(t *testing.T) {
			f := newClawbackSponsorFixture(t, issuerLow, false)

			remaining := int32(1)
			require.Equal(t, "tesSUCCESS", setSponsorship(f.env, f.reserveSponsor, f.issuer, 0, &remaining).Code)
			f.env.Close()
			qualityIn := uint32(7)
			issuerSet := trustsettx.NewTrustSet(
				f.issuer.Address,
				tx.NewIssuedAmount(0, 0, "USD", f.holder.Address),
			)
			issuerSet.QualityIn = &qualityIn
			issuerSet.Sponsor = f.reserveSponsor.Address
			reserveFlags := tx.SpfSponsorReserve
			issuerSet.SponsorFlags = &reserveFlags
			require.Equal(t, "tesSUCCESS", f.env.Submit(issuerSet).Code)
			f.env.Close()

			lineBefore := clawbackLine(t, f.env, f.lineKey)
			requireClawbackReserveSponsorState(
				t, f, lineBefore, true, true, f.reserveSponsor.Address, f.reserveSponsor.Address,
			)
			sidesBefore := clawbackLineSideValues(f.issuer, f.holder, lineBefore)
			require.Equal(t, uint32(7), sidesBefore.issuerQualityIn)
			require.Zero(t, sidesBefore.holderQualityIn)
			holderBefore := accountState(t, f.env, f.holder)
			issuerBefore := accountState(t, f.env, f.issuer)
			sponsorBefore := accountState(t, f.env, f.reserveSponsor)
			require.Equal(t, uint32(2), sponsorBefore.SponsoringOwnerCount)

			result := f.env.Submit(clawback.Claw(f.issuer, f.holder, "USD", 100).Build())
			require.Equal(t, "tesSUCCESS", result.Code)
			f.env.Close()

			require.True(t, f.env.LedgerEntryExists(f.lineKey))
			require.Equal(t, 0.0, f.env.BalanceIOU(f.holder, "USD", f.issuer))
			lineAfter := clawbackLine(t, f.env, f.lineKey)
			requireClawbackReserveSponsorState(
				t, f, lineAfter, true, false, f.reserveSponsor.Address, "",
			)
			sidesAfter := clawbackLineSideValues(f.issuer, f.holder, lineAfter)
			require.Equal(t, uint32(7), sidesAfter.issuerQualityIn)
			require.Zero(t, sidesAfter.holderQualityIn)

			holderAfter := accountState(t, f.env, f.holder)
			require.Equal(t, holderBefore.OwnerCount-1, holderAfter.OwnerCount)
			require.Equal(t, holderBefore.SponsoredOwnerCount-1, holderAfter.SponsoredOwnerCount)
			issuerAfter := accountState(t, f.env, f.issuer)
			require.Equal(t, issuerBefore.OwnerCount, issuerAfter.OwnerCount)
			require.Equal(t, issuerBefore.SponsoredOwnerCount, issuerAfter.SponsoredOwnerCount)
			require.Equal(t, issuerBefore.SponsoringOwnerCount, issuerAfter.SponsoringOwnerCount)
			sponsorAfter := accountState(t, f.env, f.reserveSponsor)
			require.Equal(t, sponsorBefore.OwnerCount, sponsorAfter.OwnerCount)
			require.Equal(t, sponsorBefore.SponsoredOwnerCount, sponsorAfter.SponsoredOwnerCount)
			require.Equal(t, sponsorBefore.SponsoringOwnerCount-1, sponsorAfter.SponsoringOwnerCount)

			lineNode := affectedNodeByKey(result.Metadata, f.lineKey)
			require.NotNil(t, lineNode)
			require.Equal(t, "ModifiedNode", lineNode.NodeType)
			require.Contains(t, lineNode.PreviousFields, "Flags")
			require.NotNil(t, affectedNodeByKey(result.Metadata, keylet.Account(f.reserveSponsor.ID)))
		})
	}
}

func TestClawbackTransactionSponsorDoesNotCreateIssuerReserve(t *testing.T) {
	f := newClawbackSponsorFixture(t, true, true)
	txSponsor := jtx.NewAccount("clawback-transaction-sponsor")
	f.env.Fund(txSponsor)
	f.env.Close()

	issuerBefore := accountState(t, f.env, f.issuer)
	holderBefore := accountState(t, f.env, f.holder)
	txSponsorBefore := accountState(t, f.env, txSponsor)
	transaction := clawback.Claw(f.issuer, f.holder, "USD", 100).Build()
	transaction.Sponsor = txSponsor.Address
	reserveFlags := tx.SpfSponsorReserve
	transaction.SponsorFlags = &reserveFlags
	attachSponsorSignature(t, f.env, transaction, f.issuer, txSponsor)

	result := f.env.SubmitSigned(transaction)
	require.Equal(t, "tesSUCCESS", result.Code)
	f.env.Close()

	require.False(t, f.env.LedgerEntryExists(f.lineKey))
	issuerAfter := accountState(t, f.env, f.issuer)
	require.Equal(t, issuerBefore.OwnerCount, issuerAfter.OwnerCount)
	require.Equal(t, issuerBefore.SponsoredOwnerCount, issuerAfter.SponsoredOwnerCount)
	require.Equal(t, issuerBefore.SponsoringOwnerCount-1, issuerAfter.SponsoringOwnerCount)
	holderAfter := accountState(t, f.env, f.holder)
	require.Equal(t, holderBefore.OwnerCount-1, holderAfter.OwnerCount)
	require.Equal(t, holderBefore.SponsoredOwnerCount-1, holderAfter.SponsoredOwnerCount)
	txSponsorAfter := accountState(t, f.env, txSponsor)
	require.Equal(t, txSponsorBefore.OwnerCount, txSponsorAfter.OwnerCount)
	require.Equal(t, txSponsorBefore.SponsoredOwnerCount, txSponsorAfter.SponsoredOwnerCount)
	require.Equal(t, txSponsorBefore.SponsoringOwnerCount, txSponsorAfter.SponsoringOwnerCount)
	require.Nil(t, affectedNodeByKey(result.Metadata, keylet.Account(txSponsor.ID)))
	lineNode := affectedNodeByKey(result.Metadata, f.lineKey)
	require.NotNil(t, lineNode)
	require.Equal(t, "DeletedNode", lineNode.NodeType)
	require.NotContains(t, lineNode.FinalFields, "LowSponsor")
	require.NotContains(t, lineNode.FinalFields, "HighSponsor")
}
