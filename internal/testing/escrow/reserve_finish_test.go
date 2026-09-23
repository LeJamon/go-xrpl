package escrow_test

import (
	"encoding/hex"
	"testing"
	"time"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func createReserveIOUEscrowForFinish(t *testing.T, fixture reserveIOUFixture, destination *jtx.Account, amount int64) (uint32, keylet.Keylet) {
	t.Helper()
	env := fixture.env
	seq := env.Seq(fixture.owner)
	result := env.Submit(
		escrow.EscrowCreate(fixture.owner, destination, 0).
			IOUAmount(usd(amount, fixture.gateway)).
			FinishTime(env.Now().Add(time.Second)).
			Build())
	jtx.RequireTxSuccess(t, result)
	escrowKey := keylet.Escrow(fixture.owner.ID, seq)
	requireEscrowMetaNode(t, result, "CreatedNode", escrowKey)
	env.Close()
	return seq, escrowKey
}

func createReserveMPTEscrowForFinish(t *testing.T, fixture reserveMPTFixture, destination *jtx.Account, amount int64) (uint32, keylet.Keylet) {
	t.Helper()
	env := fixture.env
	seq := env.Seq(fixture.owner)
	result := env.Submit(
		escrow.EscrowCreate(fixture.owner, destination, 0).
			MPTAmount(fixture.token.MPTAmount(amount)).
			FinishTime(env.Now().Add(time.Second)).
			Build())
	jtx.RequireTxSuccess(t, result)
	escrowKey := keylet.Escrow(fixture.owner.ID, seq)
	requireEscrowMetaNode(t, result, "CreatedNode", escrowKey)
	env.Close()
	return seq, escrowKey
}

type reserveMPTStateSnapshot struct {
	issuance         []byte
	ownerToken       []byte
	destinationToken []byte
}

func reserveMPTIssuanceID(t *testing.T, fixture reserveMPTFixture) [24]byte {
	t.Helper()

	raw, err := hex.DecodeString(fixture.token.IssuanceID())
	require.NoError(t, err)
	require.Len(t, raw, 24)
	var id [24]byte
	copy(id[:], raw)
	return id
}

func snapshotReserveMPTState(t *testing.T, fixture reserveMPTFixture, destination *jtx.Account) reserveMPTStateSnapshot {
	t.Helper()

	id := reserveMPTIssuanceID(t, fixture)
	issuanceKey := keylet.MPTIssuance(id)
	return reserveMPTStateSnapshot{
		issuance:         cloneEscrowLedgerEntry(t, fixture.env, issuanceKey),
		ownerToken:       cloneEscrowLedgerEntry(t, fixture.env, keylet.MPTokenByID(id, fixture.owner.ID)),
		destinationToken: cloneEscrowLedgerEntry(t, fixture.env, keylet.MPTokenByID(id, destination.ID)),
	}
}

func requireReserveMPTStateUnchanged(t *testing.T, fixture reserveMPTFixture, destination *jtx.Account, snapshot reserveMPTStateSnapshot) {
	t.Helper()

	id := reserveMPTIssuanceID(t, fixture)
	require.Equal(t, snapshot.issuance, cloneEscrowLedgerEntry(t, fixture.env, keylet.MPTIssuance(id)))
	require.Equal(t, snapshot.ownerToken, cloneEscrowLedgerEntry(t, fixture.env, keylet.MPTokenByID(id, fixture.owner.ID)))
	require.Equal(t, snapshot.destinationToken, cloneEscrowLedgerEntry(t, fixture.env, keylet.MPTokenByID(id, destination.ID)))
}

func TestEscrowReserve_FinishXRP(t *testing.T) {
	cases := []struct {
		name       string
		self       bool
		thirdParty bool
	}{
		{name: "Self", self: true},
		{name: "Cross"},
		{name: "ThirdParty", thirdParty: true},
	}

	for _, testCase := range cases {
		for _, rules := range escrowReserveRuleCases() {
			t.Run(testCase.name+"/"+rules.name, func(t *testing.T) {
				env := newEscrowReserveEnv(t, rules)
				owner := jtx.NewAccount("reserve-finish-xrp-owner")
				destination := jtx.NewAccount("reserve-finish-xrp-destination")
				thirdParty := jtx.NewAccount("reserve-finish-xrp-third-party")
				fund5000(env, owner, destination, thirdParty)
				env.Close()

				if testCase.self {
					destination = owner
				}
				finisher := destination
				if testCase.thirdParty {
					finisher = thirdParty
				}

				amount := uint64(xrp(100))
				seq := env.Seq(owner)
				result := env.Submit(
					escrow.EscrowCreate(owner, destination, int64(amount)).
						FinishTime(env.Now().Add(time.Second)).
						Build())
				jtx.RequireTxSuccess(t, result)
				escrowKey := keylet.Escrow(owner.ID, seq)
				requireEscrowMetaNode(t, result, "CreatedNode", escrowKey)
				env.Close()

				ownerBeforeFinish := env.Balance(owner)
				destinationBeforeFinish := env.Balance(destination)
				finisherBeforeFinish := env.Balance(finisher)
				finisherSequenceBefore := env.Seq(finisher)
				result = env.Submit(escrow.EscrowFinish(finisher, owner, seq).Build())
				requireEscrowTxAccounting(t, env, result, finisher, finisherSequenceBefore)
				requireEscrowDeleted(t, result, env, owner, destination, escrowKey)

				switch {
				case testCase.self:
					require.Equal(t, ownerBeforeFinish+amount-env.BaseFee(), env.Balance(owner))
				case testCase.thirdParty:
					require.Equal(t, ownerBeforeFinish, env.Balance(owner))
					require.Equal(t, destinationBeforeFinish+amount, env.Balance(destination))
					require.Equal(t, finisherBeforeFinish-env.BaseFee(), env.Balance(finisher))
				default:
					require.Equal(t, ownerBeforeFinish, env.Balance(owner))
					require.Equal(t, destinationBeforeFinish+amount-env.BaseFee(), env.Balance(destination))
				}
				require.Equal(t, uint32(0), env.OwnerCount(owner))
			})
		}
	}
}

func TestEscrowReserve_FinishIOUExistingHolding(t *testing.T) {
	cases := []struct {
		name       string
		self       bool
		thirdParty bool
	}{
		{name: "Self", self: true},
		{name: "Cross"},
		{name: "ThirdParty", thirdParty: true},
	}

	for _, testCase := range cases {
		for _, rules := range escrowReserveRuleCases() {
			t.Run(testCase.name+"/"+rules.name, func(t *testing.T) {
				fixture := newReserveIOUFixture(t, rules)
				destination := fixture.destination
				if testCase.self {
					destination = fixture.owner
				}
				finisher := destination
				if testCase.thirdParty {
					finisher = fixture.thirdParty
				}

				ownerBefore := fixture.env.OwnerCount(fixture.owner)
				destinationBefore := fixture.env.OwnerCount(destination)
				seq, escrowKey := createReserveIOUEscrowForFinish(t, fixture, destination, 1000)
				finisherBefore := fixture.env.Balance(finisher)
				finisherSequenceBefore := fixture.env.Seq(finisher)
				result := fixture.env.Submit(escrow.EscrowFinish(finisher, fixture.owner, seq).Build())
				requireEscrowTxAccounting(t, fixture.env, result, finisher, finisherSequenceBefore)
				requireEscrowDeleted(t, result, fixture.env, fixture.owner, destination, escrowKey)

				if testCase.self {
					require.Equal(t, usd(10000, fixture.gateway), *fixture.env.IOUBalance(fixture.owner, fixture.gateway, "USD"))
					require.Equal(t, ownerBefore, fixture.env.OwnerCount(fixture.owner))
				} else {
					require.Equal(t, usd(9000, fixture.gateway), *fixture.env.IOUBalance(fixture.owner, fixture.gateway, "USD"))
					require.Equal(t, ownerBefore, fixture.env.OwnerCount(fixture.owner))
					require.Equal(t, usd(2000, fixture.gateway), *fixture.env.IOUBalance(destination, fixture.gateway, "USD"))
					require.Equal(t, destinationBefore, fixture.env.OwnerCount(destination))
				}
				if testCase.thirdParty {
					require.Equal(t, finisherBefore-fixture.env.BaseFee(), fixture.env.Balance(finisher))
				}
			})
		}
	}
}

func TestEscrowReserve_FinishIOUDeletedHolding(t *testing.T) {
	for _, rules := range escrowReserveRuleCases() {
		for _, boundary := range escrowReserveBoundaryCases() {
			t.Run(rules.name+"/"+boundary.name, func(t *testing.T) {
				fixture := newReserveIOUFixture(t, rules)
				seq, escrowKey := createReserveIOUEscrowForFinish(t, fixture, fixture.owner, 10000)
				deleteReserveIOUOwnerTrustLine(t, fixture)
				setReserveAccountBalance(t, fixture.env, fixture.owner, boundary.balance(fixture.env))
				ownerBeforeFinish := fixture.env.Balance(fixture.owner)
				sourceSequenceBefore := fixture.env.Seq(fixture.owner)
				snapshot := snapshotEscrowReserveState(t, fixture.env, escrowKey, fixture.owner, fixture.owner, fixture.gateway)

				result := fixture.env.Submit(escrow.EscrowFinish(fixture.owner, fixture.owner, seq).Build())
				requireEscrowTxAccounting(t, fixture.env, result, fixture.owner, sourceSequenceBefore)
				expectSuccess := !boundary.belowReserve1 && (!boundary.requiresGate || rules.sponsor || rules.cleanup)
				if expectSuccess {
					requireEscrowDeleted(t, result, fixture.env, fixture.owner, fixture.owner, escrowKey)
					require.Equal(t, ownerBeforeFinish-fixture.env.BaseFee(), fixture.env.Balance(fixture.owner))
					require.True(t, fixture.env.TrustLineExists(fixture.owner, fixture.gateway, "USD"))
					require.Equal(t, usd(10000, fixture.gateway), *fixture.env.IOUBalance(fixture.owner, fixture.gateway, "USD"))
					require.Equal(t, uint32(1), fixture.env.OwnerCount(fixture.owner))
					return
				}

				jtx.RequireTxClaimed(t, result, "tecNO_LINE_INSUF_RESERVE")
				requireEscrowFailureMetadata(t, result, fixture.owner)
				requireEscrowRetained(t, result, fixture.env, fixture.owner, fixture.owner, escrowKey)
				requireEscrowReserveStateUnchanged(t, fixture.env, snapshot, escrowKey, fixture.owner, fixture.owner, fixture.gateway)
				require.Equal(t, ownerBeforeFinish-fixture.env.BaseFee(), fixture.env.Balance(fixture.owner))
				require.False(t, fixture.env.TrustLineExists(fixture.owner, fixture.gateway, "USD"))
				require.Equal(t, usd(0, fixture.gateway), *fixture.env.IOUBalance(fixture.owner, fixture.gateway, "USD"))
				require.Equal(t, uint32(1), fixture.env.OwnerCount(fixture.owner))
			})
		}
	}
}

func TestEscrowReserve_FinishIOUMissingDestinationHolding(t *testing.T) {
	for _, rules := range escrowReserveRuleCases() {
		t.Run(rules.name, func(t *testing.T) {
			fixture := newReserveIOUFixture(t, rules)
			ownerBefore := fixture.env.OwnerCount(fixture.owner)
			seq, escrowKey := createReserveIOUEscrowForFinish(t, fixture, fixture.destination, 1000)

			result := fixture.env.Submit(
				payment.PayIssued(fixture.destination, fixture.gateway, usd(1000, fixture.gateway)).Build())
			jtx.RequireTxSuccess(t, result)
			fixture.env.Close()
			deleteReserveIOUTrustLine(t, fixture, fixture.destination)
			require.Equal(t, usd(9000, fixture.gateway), *fixture.env.IOUBalance(fixture.owner, fixture.gateway, "USD"))
			require.Equal(t, usd(0, fixture.gateway), *fixture.env.IOUBalance(fixture.destination, fixture.gateway, "USD"))

			sourceSequenceBefore := fixture.env.Seq(fixture.thirdParty)
			finisherBalanceBefore := fixture.env.Balance(fixture.thirdParty)
			snapshot := snapshotEscrowReserveState(t, fixture.env, escrowKey, fixture.owner, fixture.destination, fixture.gateway)
			result = fixture.env.Submit(escrow.EscrowFinish(fixture.thirdParty, fixture.owner, seq).Build())
			requireEscrowTxAccounting(t, fixture.env, result, fixture.thirdParty, sourceSequenceBefore)
			jtx.RequireTxClaimed(t, result, "tecNO_LINE")
			requireEscrowFailureMetadata(t, result, fixture.thirdParty)
			requireEscrowRetained(t, result, fixture.env, fixture.owner, fixture.destination, escrowKey)
			requireEscrowReserveStateUnchanged(t, fixture.env, snapshot, escrowKey, fixture.owner, fixture.destination, fixture.gateway)
			require.Equal(t, finisherBalanceBefore-fixture.env.BaseFee(), fixture.env.Balance(fixture.thirdParty))
			require.Equal(t, ownerBefore+1, fixture.env.OwnerCount(fixture.owner))
			require.Zero(t, fixture.env.OwnerCount(fixture.destination))
		})
	}
}

func TestEscrowReserve_FinishMPTMissingDestinationHolding(t *testing.T) {
	for _, rules := range escrowReserveRuleCases() {
		for _, boundary := range escrowReserveBoundaryCases() {
			t.Run(rules.name+"/"+boundary.name, func(t *testing.T) {
				fixture := newReserveMPTFixture(t, rules)
				destination := jtx.NewAccount("reserve-mpt-missing-destination")
				fixture.env.FundAmount(destination, uint64(xrp(5000)))
				fixture.env.Close()
				ownerBefore := fixture.env.OwnerCount(fixture.owner)

				seq, escrowKey := createReserveMPTEscrowForFinish(t, fixture, destination, 1000)
				setReserveAccountBalance(t, fixture.env, destination, boundary.balance(fixture.env))
				destinationBefore := fixture.env.OwnerCount(destination)
				destinationBalanceBefore := fixture.env.Balance(destination)
				sourceSequenceBefore := fixture.env.Seq(destination)
				escrowSnapshot := snapshotEscrowReserveState(t, fixture.env, escrowKey, fixture.owner, destination, fixture.issuer)
				mptSnapshot := snapshotReserveMPTState(t, fixture, destination)

				result := fixture.env.Submit(escrow.EscrowFinish(destination, fixture.owner, seq).Build())
				requireEscrowTxAccounting(t, fixture.env, result, destination, sourceSequenceBefore)
				if boundary.belowReserve1 {
					jtx.RequireTxClaimed(t, result, "tecINSUFFICIENT_RESERVE")
					requireEscrowFailureMetadata(t, result, destination)
					requireEscrowRetained(t, result, fixture.env, fixture.owner, destination, escrowKey)
					requireEscrowReserveStateUnchanged(t, fixture.env, escrowSnapshot, escrowKey, fixture.owner, destination, fixture.issuer)
					requireReserveMPTStateUnchanged(t, fixture, destination, mptSnapshot)
					require.Equal(t, destinationBalanceBefore-fixture.env.BaseFee(), fixture.env.Balance(destination))
					require.Equal(t, destinationBefore, fixture.env.OwnerCount(destination))
					fixture.token.RequireMPTokenAmount(fixture.owner, 9000)
					require.Equal(t, uint64(1000), fixture.token.HolderLockedAmount(fixture.owner))
					require.Equal(t, uint64(1000), fixture.token.IssuanceLockedAmount())
					require.Equal(t, uint64(11000), fixture.token.IssuanceOutstandingAmount())
					require.False(t, fixture.env.LedgerEntryExists(keylet.MPTokenByID(reserveMPTIssuanceID(t, fixture), destination.ID)))
					return
				}

				requireEscrowDeleted(t, result, fixture.env, fixture.owner, destination, escrowKey)
				require.Equal(t, destinationBalanceBefore-fixture.env.BaseFee(), fixture.env.Balance(destination))
				require.Equal(t, destinationBefore+1, fixture.env.OwnerCount(destination))
				require.Equal(t, ownerBefore, fixture.env.OwnerCount(fixture.owner))
				fixture.token.RequireMPTokenAmount(fixture.owner, 9000)
				fixture.token.RequireMPTokenAmount(destination, 1000)
				require.Zero(t, fixture.token.HolderLockedAmount(fixture.owner))
				require.Zero(t, fixture.token.IssuanceLockedAmount())
				require.Equal(t, uint64(11000), fixture.token.IssuanceOutstandingAmount())
			})
		}
	}
}

func TestEscrowReserve_FinishMPTMissingDestinationThirdParty(t *testing.T) {
	for _, rules := range escrowReserveRuleCases() {
		t.Run(rules.name, func(t *testing.T) {
			fixture := newReserveMPTFixture(t, rules)
			destination := jtx.NewAccount("reserve-mpt-third-party-destination")
			fixture.env.FundAmount(destination, uint64(xrp(5000)))
			fixture.env.Close()

			seq, escrowKey := createReserveMPTEscrowForFinish(t, fixture, destination, 1000)
			setReserveAccountBalance(t, fixture.env, destination, escrowReserveBoundaryCases()[0].balance(fixture.env))
			destinationBalanceBefore := fixture.env.Balance(destination)
			destinationOwnerCountBefore := fixture.env.OwnerCount(destination)
			finisherBalanceBefore := fixture.env.Balance(fixture.thirdParty)
			finisherSequenceBefore := fixture.env.Seq(fixture.thirdParty)
			escrowSnapshot := snapshotEscrowReserveState(t, fixture.env, escrowKey, fixture.owner, destination, fixture.issuer)
			mptSnapshot := snapshotReserveMPTState(t, fixture, destination)

			result := fixture.env.Submit(escrow.EscrowFinish(fixture.thirdParty, fixture.owner, seq).Build())
			requireEscrowTxAccounting(t, fixture.env, result, fixture.thirdParty, finisherSequenceBefore)
			jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
			requireEscrowFailureMetadata(t, result, fixture.thirdParty)
			requireEscrowRetained(t, result, fixture.env, fixture.owner, destination, escrowKey)
			requireEscrowReserveStateUnchanged(t, fixture.env, escrowSnapshot, escrowKey, fixture.owner, destination, fixture.issuer)
			requireReserveMPTStateUnchanged(t, fixture, destination, mptSnapshot)
			require.Equal(t, destinationBalanceBefore, fixture.env.Balance(destination))
			require.Equal(t, destinationOwnerCountBefore, fixture.env.OwnerCount(destination))
			require.Equal(t, finisherBalanceBefore-fixture.env.BaseFee(), fixture.env.Balance(fixture.thirdParty))
			fixture.token.RequireMPTokenAmount(fixture.owner, 9000)
			require.Equal(t, uint64(1000), fixture.token.HolderLockedAmount(fixture.owner))
			require.Equal(t, uint64(1000), fixture.token.IssuanceLockedAmount())
			require.Equal(t, uint64(11000), fixture.token.IssuanceOutstandingAmount())
			require.False(t, fixture.env.LedgerEntryExists(keylet.MPTokenByID(reserveMPTIssuanceID(t, fixture), destination.ID)))
		})
	}
}

func TestEscrowReserve_FinishMPTExistingHolding(t *testing.T) {
	cases := []struct {
		name       string
		self       bool
		thirdParty bool
	}{
		{name: "Self", self: true},
		{name: "Cross"},
		{name: "ThirdParty", thirdParty: true},
	}

	for _, testCase := range cases {
		for _, rules := range escrowReserveRuleCases() {
			t.Run(testCase.name+"/"+rules.name, func(t *testing.T) {
				fixture := newReserveMPTFixture(t, rules)
				destination := fixture.destination
				if testCase.self {
					destination = fixture.owner
				}
				finisher := destination
				if testCase.thirdParty {
					finisher = fixture.thirdParty
				}

				ownerBefore := fixture.env.OwnerCount(fixture.owner)
				destinationBefore := fixture.env.OwnerCount(destination)
				seq, escrowKey := createReserveMPTEscrowForFinish(t, fixture, destination, 1000)
				finisherBefore := fixture.env.Balance(finisher)
				finisherSequenceBefore := fixture.env.Seq(finisher)
				result := fixture.env.Submit(escrow.EscrowFinish(finisher, fixture.owner, seq).Build())
				requireEscrowTxAccounting(t, fixture.env, result, finisher, finisherSequenceBefore)
				requireEscrowDeleted(t, result, fixture.env, fixture.owner, destination, escrowKey)

				if testCase.self {
					fixture.token.RequireMPTokenAmount(fixture.owner, 10000)
					require.Equal(t, ownerBefore, fixture.env.OwnerCount(fixture.owner))
				} else {
					fixture.token.RequireMPTokenAmount(fixture.owner, 9000)
					require.Equal(t, ownerBefore, fixture.env.OwnerCount(fixture.owner))
					fixture.token.RequireMPTokenAmount(destination, 2000)
					require.Equal(t, destinationBefore, fixture.env.OwnerCount(destination))
				}
				require.Zero(t, fixture.token.HolderLockedAmount(fixture.owner))
				require.Zero(t, fixture.token.IssuanceLockedAmount())
				if testCase.thirdParty {
					require.Equal(t, finisherBefore-fixture.env.BaseFee(), fixture.env.Balance(finisher))
				}
			})
		}
	}
}
