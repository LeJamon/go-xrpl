package escrow_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/internal/testing/metadata"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type escrowReserveRules struct {
	name    string
	sponsor bool
	cleanup bool
}

func escrowReserveRuleCases() []escrowReserveRules {
	return []escrowReserveRules{
		{name: "SponsorOff_CleanupOff"},
		{name: "SponsorOff_CleanupOn", cleanup: true},
		{name: "SponsorOn_CleanupOff", sponsor: true},
		{name: "SponsorOn_CleanupOn", sponsor: true, cleanup: true},
	}
}

type escrowReserveBoundary struct {
	name          string
	requiresGate  bool
	belowReserve1 bool
}

func escrowReserveBoundaryCases() []escrowReserveBoundary {
	return []escrowReserveBoundary{
		{name: "ReserveOne", requiresGate: true},
		{name: "ReserveOneMinusDrop", belowReserve1: true},
		{name: "ReserveTwo"},
	}
}

func (boundary escrowReserveBoundary) balance(env *jtx.TestEnv) uint64 {
	reserveOne := env.ReserveBase() + env.ReserveIncrement()
	if boundary.belowReserve1 {
		return reserveOne - 1
	}
	if boundary.requiresGate {
		return reserveOne
	}
	return reserveOne + env.ReserveIncrement()
}

func requireEscrowTxAccounting(t *testing.T, env *jtx.TestEnv, result jtx.TxResult, source *jtx.Account, sequenceBefore uint32) {
	t.Helper()

	require.Equal(t, env.BaseFee(), result.Fee)
	require.True(t, result.Applied)
	require.NotNil(t, result.Metadata)
	require.Equal(t, result.Code, result.Metadata.TransactionResult.String())
	require.Equal(t, sequenceBefore+1, env.Seq(source))
}

type escrowReserveDirectorySnapshot map[keylet.Keylet][]byte

type escrowReserveStateSnapshot struct {
	escrow          []byte
	ownerDirectory  escrowReserveDirectorySnapshot
	destinationDir  escrowReserveDirectorySnapshot
	issuerDirectory escrowReserveDirectorySnapshot
}

func cloneEscrowLedgerEntry(t *testing.T, env *jtx.TestEnv, entryKey keylet.Keylet) []byte {
	t.Helper()

	data, err := env.LedgerEntry(entryKey)
	require.NoError(t, err)
	return append([]byte(nil), data...)
}

func snapshotEscrowReserveDirectory(t *testing.T, env *jtx.TestEnv, account *jtx.Account) escrowReserveDirectorySnapshot {
	t.Helper()

	rootKey := keylet.OwnerDir(account.ID)
	snapshot := make(escrowReserveDirectorySnapshot)
	pageKey := rootKey
	for {
		data := cloneEscrowLedgerEntry(t, env, pageKey)
		if data == nil {
			return snapshot
		}
		snapshot[pageKey] = data

		directory, err := state.ParseDirectoryNode(data)
		require.NoError(t, err)
		if directory.IndexNext == 0 {
			return snapshot
		}
		pageKey = keylet.DirPage(rootKey.Key, directory.IndexNext)
	}
}

func snapshotEscrowReserveState(t *testing.T, env *jtx.TestEnv, escrowKey keylet.Keylet, owner, destination, issuer *jtx.Account) escrowReserveStateSnapshot {
	t.Helper()

	return escrowReserveStateSnapshot{
		escrow:          cloneEscrowLedgerEntry(t, env, escrowKey),
		ownerDirectory:  snapshotEscrowReserveDirectory(t, env, owner),
		destinationDir:  snapshotEscrowReserveDirectory(t, env, destination),
		issuerDirectory: snapshotEscrowReserveDirectory(t, env, issuer),
	}
}

func requireEscrowReserveStateUnchanged(t *testing.T, env *jtx.TestEnv, snapshot escrowReserveStateSnapshot, escrowKey keylet.Keylet, owner, destination, issuer *jtx.Account) {
	t.Helper()

	require.Equal(t, snapshot.escrow, cloneEscrowLedgerEntry(t, env, escrowKey))
	require.Equal(t, snapshot.ownerDirectory, snapshotEscrowReserveDirectory(t, env, owner))
	require.Equal(t, snapshot.destinationDir, snapshotEscrowReserveDirectory(t, env, destination))
	require.Equal(t, snapshot.issuerDirectory, snapshotEscrowReserveDirectory(t, env, issuer))
}

func requireEscrowFailureMetadata(t *testing.T, result jtx.TxResult, feePayer *jtx.Account) {
	t.Helper()

	require.NotNil(t, result.Metadata)
	require.Len(t, result.Metadata.AffectedNodes, 1)
	node := result.Metadata.AffectedNodes[0]
	require.Equal(t, "ModifiedNode", node.NodeType)
	require.Equal(t, "AccountRoot", node.LedgerEntryType)
	require.Equal(t, fmt.Sprintf("%X", keylet.Account(feePayer.ID).Key), node.LedgerIndex)
	require.NotNil(t, node.FinalFields)
	require.NotNil(t, node.PreviousFields)
	require.Len(t, node.PreviousFields, 2)
	require.Contains(t, node.PreviousFields, "Balance")
	require.Contains(t, node.PreviousFields, "Sequence")
}

func newEscrowReserveEnv(t *testing.T, rules escrowReserveRules) *jtx.TestEnv {
	t.Helper()

	env := jtx.NewTestEnv(t)
	env.EnableFeature("TokenEscrow")
	if rules.sponsor {
		env.EnableFeature("Sponsor")
	} else {
		env.DisableFeature("Sponsor")
	}
	if rules.cleanup {
		env.EnableFeature("fixCleanup3_4_0")
	} else {
		env.DisableFeature("fixCleanup3_4_0")
	}
	env.Close()
	return env
}

func requireEscrowRetained(t *testing.T, result jtx.TxResult, env *jtx.TestEnv, owner, destination *jtx.Account, escrowKey keylet.Keylet) {
	t.Helper()

	jtx.RequireTxClaimed(t, result, result.Code)
	require.True(t, env.LedgerEntryExists(escrowKey), "failed transaction must retain escrow")
	requireOwnerDirContains(t, env, owner, escrowKey.Key, true)
	if owner.ID != destination.ID {
		requireOwnerDirContains(t, env, destination, escrowKey.Key, true)
	}
	requireNoEscrowDeletedNode(t, result)
}

func requireEscrowDeleted(t *testing.T, result jtx.TxResult, env *jtx.TestEnv, owner, destination *jtx.Account, escrowKey keylet.Keylet) {
	t.Helper()

	jtx.RequireTxSuccess(t, result)
	requireEscrowMetaNode(t, result, "DeletedNode", escrowKey)
	require.False(t, env.LedgerEntryExists(escrowKey), "successful transaction must erase escrow")
	requireOwnerDirContains(t, env, owner, escrowKey.Key, false)
	if owner.ID != destination.ID {
		requireOwnerDirContains(t, env, destination, escrowKey.Key, false)
	}
}

func requireNoEscrowDeletedNode(t *testing.T, result jtx.TxResult) {
	t.Helper()
	if result.Metadata == nil {
		return
	}
	require.Empty(t, metadata.FindNodes(result.Metadata, "DeletedNode", "Escrow"))
}

type reserveIOUFixture struct {
	env         *jtx.TestEnv
	gateway     *jtx.Account
	owner       *jtx.Account
	destination *jtx.Account
	thirdParty  *jtx.Account
}

func newReserveIOUFixture(t *testing.T, rules escrowReserveRules) reserveIOUFixture {
	t.Helper()

	env := newEscrowReserveEnv(t, rules)
	gateway := jtx.NewAccount("reserve-gateway")
	owner := jtx.NewAccount("reserve-owner")
	destination := jtx.NewAccount("reserve-destination")
	thirdParty := jtx.NewAccount("reserve-third-party")
	fund5000(env, gateway, owner, destination, thirdParty)

	result := env.Submit(accountset.AccountSet(gateway).AllowTrustLineLocking().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	limit := tx.NewIssuedAmountFromFloat64(10000, "USD", gateway.Address)
	env.Trust(owner, limit)
	env.Trust(destination, limit)
	env.Close()

	result = env.Submit(payment.PayIssued(gateway, owner, usd(10000, gateway)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(gateway, destination, usd(1000, gateway)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	return reserveIOUFixture{
		env:         env,
		gateway:     gateway,
		owner:       owner,
		destination: destination,
		thirdParty:  thirdParty,
	}
}

type reserveMPTFixture struct {
	env         *jtx.TestEnv
	issuer      *jtx.Account
	owner       *jtx.Account
	destination *jtx.Account
	thirdParty  *jtx.Account
	token       *mpttest.MPTTester
}

func newReserveMPTFixture(t *testing.T, rules escrowReserveRules) reserveMPTFixture {
	t.Helper()

	env := newEscrowReserveEnv(t, rules)
	issuer := jtx.NewAccount("reserve-mpt-issuer")
	owner := jtx.NewAccount("reserve-mpt-owner")
	destination := jtx.NewAccount("reserve-mpt-destination")
	thirdParty := jtx.NewAccount("reserve-mpt-third-party")
	env.FundAmount(thirdParty, uint64(xrp(5000)))

	token := mpttest.NewMPTTester(t, env, issuer, mpttest.MPTInit{
		Holders: []*jtx.Account{owner, destination},
	})
	token.Create(mpttest.CreateOpts{
		OwnerCount: mpttest.PtrUint32(1),
		Flags:      mpttest.TfMPTCanEscrow | mpttest.TfMPTCanTransfer,
	})
	token.Authorize(mpttest.AuthorizeOpts{Account: owner})
	token.Authorize(mpttest.AuthorizeOpts{Account: destination})
	token.Pay(issuer, owner, 10000)
	token.Pay(issuer, destination, 1000)
	env.Close()

	return reserveMPTFixture{
		env:         env,
		issuer:      issuer,
		owner:       owner,
		destination: destination,
		thirdParty:  thirdParty,
		token:       token,
	}
}

func createReserveIOUEscrow(t *testing.T, fixture reserveIOUFixture, destination *jtx.Account, amount tx.Amount) (uint32, keylet.Keylet) {
	t.Helper()
	env := fixture.env
	seq := env.Seq(fixture.owner)
	result := env.Submit(
		escrow.EscrowCreate(fixture.owner, destination, 0).
			IOUAmount(amount).
			FinishTime(env.Now().Add(time.Second)).
			CancelTime(env.Now().Add(2 * time.Second)).
			Build())
	jtx.RequireTxSuccess(t, result)
	escrowKey := keylet.Escrow(fixture.owner.ID, seq)
	requireEscrowMetaNode(t, result, "CreatedNode", escrowKey)
	env.Close()
	return seq, escrowKey
}

func createReserveMPTEscrow(t *testing.T, fixture reserveMPTFixture, destination *jtx.Account, amount int64) (uint32, keylet.Keylet) {
	t.Helper()
	env := fixture.env
	seq := env.Seq(fixture.owner)
	result := env.Submit(
		escrow.EscrowCreate(fixture.owner, destination, 0).
			MPTAmount(fixture.token.MPTAmount(amount)).
			FinishTime(env.Now().Add(time.Second)).
			CancelTime(env.Now().Add(2 * time.Second)).
			Build())
	jtx.RequireTxSuccess(t, result)
	escrowKey := keylet.Escrow(fixture.owner.ID, seq)
	requireEscrowMetaNode(t, result, "CreatedNode", escrowKey)
	env.Close()
	return seq, escrowKey
}

func setReserveAccountBalance(t *testing.T, env *jtx.TestEnv, account *jtx.Account, balance uint64) {
	t.Helper()
	accountKey := keylet.Account(account.ID)
	data, err := env.LedgerEntry(accountKey)
	require.NoError(t, err)
	require.NotNil(t, data)
	accountRoot, err := state.ParseAccountRoot(data)
	require.NoError(t, err)
	accountRoot.Balance = balance
	updated, err := state.SerializeAccountRoot(accountRoot)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(accountKey, updated))
	env.Close()
	require.Equal(t, balance, env.Balance(account))
}

func deleteReserveIOUTrustLine(t *testing.T, fixture reserveIOUFixture, holder *jtx.Account) {
	t.Helper()
	fixture.env.Trust(holder, usd(0, fixture.gateway))
	fixture.env.Close()
	require.False(t, fixture.env.TrustLineExists(holder, fixture.gateway, "USD"))
}

func deleteReserveIOUOwnerTrustLine(t *testing.T, fixture reserveIOUFixture) {
	t.Helper()
	deleteReserveIOUTrustLine(t, fixture, fixture.owner)
}

func TestEscrowReserve_CancelXRP(t *testing.T) {
	for _, rules := range escrowReserveRuleCases() {
		t.Run(rules.name, func(t *testing.T) {
			env := newEscrowReserveEnv(t, rules)
			owner := jtx.NewAccount("reserve-xrp-owner")
			destination := jtx.NewAccount("reserve-xrp-destination")
			thirdParty := jtx.NewAccount("reserve-xrp-third-party")
			fund5000(env, owner, destination, thirdParty)
			env.Close()

			amount := uint64(xrp(100))
			ownerBefore := env.Balance(owner)
			thirdPartyBefore := env.Balance(thirdParty)
			seq := env.Seq(owner)
			result := env.Submit(
				escrow.EscrowCreate(owner, destination, int64(amount)).
					FinishTime(env.Now().Add(time.Second)).
					CancelTime(env.Now().Add(2 * time.Second)).
					Build())
			jtx.RequireTxSuccess(t, result)
			escrowKey := keylet.Escrow(owner.ID, seq)
			requireEscrowMetaNode(t, result, "CreatedNode", escrowKey)
			env.Close()

			cancellerSequenceBefore := env.Seq(thirdParty)
			result = env.Submit(escrow.EscrowCancel(thirdParty, owner, seq).Build())
			requireEscrowTxAccounting(t, env, result, thirdParty, cancellerSequenceBefore)
			requireEscrowDeleted(t, result, env, owner, destination, escrowKey)
			require.Equal(t, ownerBefore-env.BaseFee(), env.Balance(owner))
			require.Equal(t, thirdPartyBefore-env.BaseFee(), env.Balance(thirdParty))
			require.Equal(t, uint32(0), env.OwnerCount(owner))
			require.Equal(t, uint32(0), env.OwnerCount(destination))
		})
	}
}

func TestEscrowReserve_CancelIOUExistingHolding(t *testing.T) {
	for _, cancellerIsOwner := range []bool{true, false} {
		cancellerName := "ThirdParty"
		if cancellerIsOwner {
			cancellerName = "Self"
		}
		for _, rules := range escrowReserveRuleCases() {
			t.Run(cancellerName+"/"+rules.name, func(t *testing.T) {
				fixture := newReserveIOUFixture(t, rules)
				ownerBefore := fixture.env.OwnerCount(fixture.owner)
				seq, escrowKey := createReserveIOUEscrow(t, fixture, fixture.destination, usd(1000, fixture.gateway))
				thirdPartyBefore := fixture.env.Balance(fixture.thirdParty)
				canceller := fixture.thirdParty
				if cancellerIsOwner {
					canceller = fixture.owner
				}

				cancellerSequenceBefore := fixture.env.Seq(canceller)
				result := fixture.env.Submit(escrow.EscrowCancel(canceller, fixture.owner, seq).Build())
				requireEscrowTxAccounting(t, fixture.env, result, canceller, cancellerSequenceBefore)
				requireEscrowDeleted(t, result, fixture.env, fixture.owner, fixture.destination, escrowKey)
				require.Equal(t, ownerBefore, fixture.env.OwnerCount(fixture.owner))
				require.Equal(t, usd(10000, fixture.gateway), *fixture.env.IOUBalance(fixture.owner, fixture.gateway, "USD"))
				require.Equal(t, usd(1000, fixture.gateway), *fixture.env.IOUBalance(fixture.destination, fixture.gateway, "USD"))
				if cancellerIsOwner {
					require.Equal(t, thirdPartyBefore, fixture.env.Balance(fixture.thirdParty))
				} else {
					require.Equal(t, thirdPartyBefore-fixture.env.BaseFee(), fixture.env.Balance(fixture.thirdParty))
				}
			})
		}
	}
}

func TestEscrowReserve_CancelIOUDeletedHolding(t *testing.T) {
	for _, rules := range escrowReserveRuleCases() {
		for _, boundary := range escrowReserveBoundaryCases() {
			t.Run(rules.name+"/"+boundary.name, func(t *testing.T) {
				fixture := newReserveIOUFixture(t, rules)
				seq, escrowKey := createReserveIOUEscrow(t, fixture, fixture.destination, usd(10000, fixture.gateway))
				deleteReserveIOUOwnerTrustLine(t, fixture)
				setReserveAccountBalance(t, fixture.env, fixture.owner, boundary.balance(fixture.env))
				ownerBeforeCancel := fixture.env.Balance(fixture.owner)
				sourceSequenceBefore := fixture.env.Seq(fixture.owner)
				snapshot := snapshotEscrowReserveState(t, fixture.env, escrowKey, fixture.owner, fixture.destination, fixture.gateway)

				result := fixture.env.Submit(escrow.EscrowCancel(fixture.owner, fixture.owner, seq).Build())
				requireEscrowTxAccounting(t, fixture.env, result, fixture.owner, sourceSequenceBefore)
				expectSuccess := !boundary.belowReserve1 && (!boundary.requiresGate || rules.cleanup)
				if expectSuccess {
					requireEscrowDeleted(t, result, fixture.env, fixture.owner, fixture.destination, escrowKey)
					require.Equal(t, ownerBeforeCancel-fixture.env.BaseFee(), fixture.env.Balance(fixture.owner))
					require.True(t, fixture.env.TrustLineExists(fixture.owner, fixture.gateway, "USD"))
					require.Equal(t, usd(10000, fixture.gateway), *fixture.env.IOUBalance(fixture.owner, fixture.gateway, "USD"))
					require.Equal(t, uint32(1), fixture.env.OwnerCount(fixture.owner))
					return
				}

				jtx.RequireTxClaimed(t, result, "tecNO_LINE_INSUF_RESERVE")
				requireEscrowFailureMetadata(t, result, fixture.owner)
				requireEscrowRetained(t, result, fixture.env, fixture.owner, fixture.destination, escrowKey)
				requireEscrowReserveStateUnchanged(t, fixture.env, snapshot, escrowKey, fixture.owner, fixture.destination, fixture.gateway)
				require.Equal(t, ownerBeforeCancel-fixture.env.BaseFee(), fixture.env.Balance(fixture.owner))
				require.False(t, fixture.env.TrustLineExists(fixture.owner, fixture.gateway, "USD"))
				require.Equal(t, usd(0, fixture.gateway), *fixture.env.IOUBalance(fixture.owner, fixture.gateway, "USD"))
				require.Equal(t, uint32(1), fixture.env.OwnerCount(fixture.owner))
			})
		}
	}
}

func TestEscrowReserve_CancelMPTExistingHolding(t *testing.T) {
	// An ordinary MPT escrow keeps the escrowed amount in LockedAmount, so its
	// holder entry cannot be deleted while this test's escrow is live. The
	// deleted-holding boundary is therefore covered by the IOU cases above.
	for _, cancellerIsOwner := range []bool{true, false} {
		cancellerName := "ThirdParty"
		if cancellerIsOwner {
			cancellerName = "Self"
		}
		for _, rules := range escrowReserveRuleCases() {
			t.Run(cancellerName+"/"+rules.name, func(t *testing.T) {
				fixture := newReserveMPTFixture(t, rules)
				ownerBefore := fixture.env.OwnerCount(fixture.owner)
				seq, escrowKey := createReserveMPTEscrow(t, fixture, fixture.destination, 1000)
				thirdPartyBefore := fixture.env.Balance(fixture.thirdParty)
				canceller := fixture.thirdParty
				if cancellerIsOwner {
					canceller = fixture.owner
				}

				cancellerSequenceBefore := fixture.env.Seq(canceller)
				result := fixture.env.Submit(escrow.EscrowCancel(canceller, fixture.owner, seq).Build())
				requireEscrowTxAccounting(t, fixture.env, result, canceller, cancellerSequenceBefore)
				requireEscrowDeleted(t, result, fixture.env, fixture.owner, fixture.destination, escrowKey)
				fixture.token.RequireMPTokenAmount(fixture.owner, 10000)
				require.Equal(t, ownerBefore, fixture.env.OwnerCount(fixture.owner))
				if cancellerIsOwner {
					require.Equal(t, thirdPartyBefore, fixture.env.Balance(fixture.thirdParty))
				} else {
					require.Equal(t, thirdPartyBefore-fixture.env.BaseFee(), fixture.env.Balance(fixture.thirdParty))
				}
			})
		}
	}
}
