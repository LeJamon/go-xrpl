package service

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/localtxs"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/stretchr/testify/require"
)

type failingStartupStore struct {
	nodestore.Database
	err error
}

func (s *failingStartupStore) StoreBatch(context.Context, []*nodestore.Node) error {
	return s.err
}

func TestService_StartupFreshAndNetworkAreDistinct(t *testing.T) {
	table := amendment.NewTable()
	table.Veto(amendment.DefaultYesFeatures()[0].ID)
	table.UpVote(amendment.FeatureAMM)

	fresh, err := New(Config{
		Standalone:    false,
		Startup:       StartupConfig{Mode: StartupFresh},
		GenesisConfig: genesis.DefaultConfig(),
		Table:         table,
	})
	require.NoError(t, err)
	require.NoError(t, fresh.Start())
	t.Cleanup(fresh.Stop)
	require.False(t, fresh.NeedsInitialSync())
	require.Nil(t, fresh.GetValidatedLedger())
	freshAmendments, err := fresh.GetClosedLedger().Read(keylet.Amendments())
	require.NoError(t, err)
	require.NotNil(t, freshAmendments)
	freshAmendmentSLE, err := pseudo.ParseAmendmentsSLE(freshAmendments)
	require.NoError(t, err)
	require.ElementsMatch(t, table.Desired(), freshAmendmentSLE.Amendments)

	network, err := New(Config{
		Standalone:    false,
		Startup:       StartupConfig{Mode: StartupNetwork},
		GenesisConfig: genesis.DefaultConfig(),
	})
	require.NoError(t, err)
	require.NoError(t, network.Start())
	t.Cleanup(network.Stop)
	require.True(t, network.NeedsInitialSync())
	require.Nil(t, network.GetValidatedLedger())
	networkAmendments, err := network.GetClosedLedger().Read(keylet.Amendments())
	require.NoError(t, err)
	require.Nil(t, networkAmendments)
}

func TestService_StartupFreshDurablyStoresGenesisAndInitialLedger(t *testing.T) {
	t.Parallel()
	db := newTestNodeStore(t, 1_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	svc, err := New(Config{
		Standalone:    true,
		Startup:       StartupConfig{Mode: StartupFresh},
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	for _, hash := range [][32]byte{svc.genesisLedger.Hash(), svc.GetClosedLedger().Hash()} {
		node, err := db.Fetch(t.Context(), nodestore.Hash256(hash))
		require.NoError(t, err)
		require.NotNil(t, node)
	}
}

func TestService_StartupFreshFailsWhenDurableStoreFails(t *testing.T) {
	t.Parallel()
	base := newTestNodeStore(t, 1_000)
	t.Cleanup(func() { require.NoError(t, base.Close()) })
	sentinel := errors.New("store failed")
	db := &failingStartupStore{Database: base, err: sentinel}
	svc, err := New(Config{
		Standalone:    true,
		Startup:       StartupConfig{Mode: StartupFresh},
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
	})
	require.NoError(t, err)
	require.ErrorIs(t, svc.Start(), sentinel)
}

func TestService_StartupNormalUsesEmptyInitialAmendments(t *testing.T) {
	svc, err := New(Config{
		Standalone:    true,
		Startup:       StartupConfig{Mode: StartupNormal},
		GenesisConfig: genesis.DefaultConfig(),
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	data, err := svc.GetClosedLedger().Read(keylet.Amendments())
	require.NoError(t, err)
	require.Nil(t, data)
}

func TestService_ExplicitLoadFailureUsesConfiguredFastLoadFallback(t *testing.T) {
	tests := []struct {
		name    string
		startup StartupConfig
	}{
		{
			name:    "latest ledger",
			startup: StartupConfig{Mode: StartupLoad},
		},
		{
			name:    "empty ledger file",
			startup: StartupConfig{Mode: StartupLoadFile},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fatalLoad, err := New(Config{
				Standalone:    false,
				Startup:       test.startup,
				GenesisConfig: genesis.DefaultConfig(),
			})
			require.NoError(t, err)
			t.Cleanup(fatalLoad.Stop)
			require.Error(t, fatalLoad.Start())

			fallback, err := New(Config{
				Standalone:    false,
				Startup:       test.startup,
				GenesisConfig: genesis.DefaultConfig(),
				FastLoad:      true,
			})
			require.NoError(t, err)
			require.NoError(t, fallback.Start())
			t.Cleanup(fallback.Stop)
			require.False(t, fallback.NeedsInitialSync())
			require.True(t, fallback.IsFastLoadProvisional())
			require.False(t, fallback.GetServerInfo().NeedsNetworkLedger)
			require.Nil(t, fallback.GetValidatedLedger())
			fallbackAmendments, err := fallback.GetClosedLedger().Read(keylet.Amendments())
			require.NoError(t, err)
			require.Nil(t, fallbackAmendments)
		})
	}
}

func TestService_StartupLoadIdentifiers(t *testing.T) {
	ctx := context.Background()
	db, rm := newStartupTestStorage(t, ctx)
	target := persistStartupTarget(t, ctx, db, rm, false)
	targetHash := target.Hash()

	tests := []struct {
		name string
		id   string
	}{
		{name: "latest"},
		{name: "keyword", id: "LaTeSt"},
		{name: "hash", id: fmt.Sprintf("%X", targetHash)},
		{name: "sequence", id: fmt.Sprintf("%d", target.Sequence())},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc, err := New(Config{
				Standalone:    true,
				Startup:       StartupConfig{Mode: StartupLoad, Ledger: test.id},
				GenesisConfig: genesis.DefaultConfig(),
				NodeStore:     db,
				SHAMapFamily:  backend.New(db),
				RelationalDB:  rm,
			})
			require.NoError(t, err)
			require.NoError(t, svc.Start())
			t.Cleanup(svc.Stop)
			require.Equal(t, targetHash, svc.GetClosedLedger().Hash())
			require.Equal(t, targetHash, svc.GetValidatedLedger().Hash())
			require.False(t, svc.NeedsInitialSync())
		})
	}
}

func TestService_StartupLoadSynchronizesAmendmentTable(t *testing.T) {
	ctx := context.Background()
	db, rm := newStartupTestStorage(t, ctx)
	unknown := amendment.FeatureID("startup-test-unsupported-amendment")
	genesisConfig := genesis.DefaultConfig()
	genesisConfig.Amendments = append(genesisConfig.Amendments, unknown)
	target := persistStartupTargetWithGenesis(t, ctx, db, rm, genesisConfig)

	table := amendment.NewTable()
	svc, err := New(Config{
		Standalone:    true,
		Startup:       StartupConfig{Mode: StartupLoad, Ledger: fmt.Sprintf("%X", target.Hash())},
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
		Table:         table,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	require.True(t, table.IsEnabled(unknown))
	require.True(t, svc.IsAmendmentBlocked())
	require.False(t, table.NeedValidatedLedger(target.Sequence()))
}

func TestService_StartupLoadRejectsLedgerWithoutSuccessor(t *testing.T) {
	ctx := context.Background()
	db, rm := newStartupTestStorage(t, ctx)
	source := persistStartupTarget(t, ctx, db, rm, false)
	stateMap, err := source.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := source.TxMapSnapshot()
	require.NoError(t, err)
	hdr := source.Header()
	hdr.LedgerIndex = ^uint32(0)
	hdr.Hash = header.CalculateHash(hdr)
	maxLedger, err := ledger.NewFromHeader(hdr, stateMap, txMap, source.Fees())
	require.NoError(t, err)

	persister, err := New(Config{
		NodeStore:    db,
		SHAMapFamily: backend.New(db),
		RelationalDB: rm,
	})
	require.NoError(t, err)
	require.NoError(t, persister.persistValidatedLedger(ctx, maxLedger, false))

	svc, err := New(Config{
		Standalone:    true,
		Startup:       StartupConfig{Mode: StartupLoad, Ledger: fmt.Sprintf("%X", maxLedger.Hash())},
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	t.Cleanup(svc.Stop)
	require.ErrorContains(t, svc.Start(), "has no successor")
}

func TestService_StartupLedgerFilePersistsForSubsequentLoad(t *testing.T) {
	ctx := context.Background()
	db, rm := newStartupTestStorage(t, ctx)
	path := filepath.Join(t.TempDir(), "ledger.json")
	document := ledgerFileMarshal(t, map[string]any{
		"ledger": map[string]any{
			"accountState": []any{ledgerFileTestAccountRoot(ledgerFileTestIndex)},
		},
	})
	require.NoError(t, os.WriteFile(path, []byte(document), 0o600))

	importer, err := New(Config{
		Standalone:    true,
		Startup:       StartupConfig{Mode: StartupLoadFile, Ledger: path},
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, importer.Start())
	importedHash := importer.GetClosedLedger().Hash()
	importer.Stop()

	reader, err := New(Config{
		Standalone:    true,
		Startup:       StartupConfig{Mode: StartupLoad},
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, reader.Start())
	t.Cleanup(reader.Stop)
	require.Equal(t, importedHash, reader.GetClosedLedger().Hash())
}

func TestService_StartupReplayStagesParentAndRebuildsTarget(t *testing.T) {
	ctx := context.Background()
	db, rm := newStartupTestStorage(t, ctx)
	parent, target, txHash := persistStartupReplayChain(t, ctx, db, rm)
	targetHash := target.Hash()
	targetHeader := target.Header()

	svc, err := New(Config{
		Standalone:    true,
		Startup:       StartupConfig{Mode: StartupReplay, Ledger: fmt.Sprintf("%X", targetHash)},
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	require.Equal(t, parent.Hash(), svc.GetClosedLedger().Hash())
	openStateRoot, err := svc.GetOpenLedger().StateMapHash()
	require.NoError(t, err)
	require.Equal(t, parent.Header().AccountHash, openStateRoot)
	hasTx, err := svc.GetOpenLedger().TxExists(txHash)
	require.NoError(t, err)
	require.True(t, hasTx)

	seq, err := svc.acceptLedgerAt(ctx, time.Unix(1, 0))
	require.NoError(t, err)
	require.Equal(t, target.Sequence(), seq)
	replayed := svc.GetClosedLedger()
	require.Equal(t, targetHash, replayed.Hash())
	require.Equal(t, targetHeader.AccountHash, replayed.Header().AccountHash)
	require.Equal(t, targetHeader.TxHash, replayed.Header().TxHash)
	require.Equal(t, protocol.ToRippleTime(targetHeader.CloseTime), protocol.ToRippleTime(replayed.Header().CloseTime))
	require.Equal(t, targetHeader.CloseTimeResolution, replayed.Header().CloseTimeResolution)
	require.Equal(t, targetHeader.CloseFlags, replayed.Header().CloseFlags)
}

func TestService_StartupReplayOverridesFirstConsensusBuildOnly(t *testing.T) {
	ctx := context.Background()
	db, rm := newStartupTestStorage(t, ctx)
	_, target, _ := persistStartupReplayChain(t, ctx, db, rm)
	targetHash := target.Hash()
	extraBlob, extraHash := startupPaymentBlob(t, "startup-replay-extra", 2)

	svc, err := New(Config{
		Standalone:    false,
		Startup:       StartupConfig{Mode: StartupReplay, Ledger: fmt.Sprintf("%X", targetHash)},
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	parent := svc.GetClosedLedger()
	seq, err := svc.AcceptConsensusResult(
		ctx,
		parent,
		[][]byte{extraBlob},
		nil,
		time.Unix(1, 0),
		false,
	)
	require.NoError(t, err)
	require.Equal(t, target.Sequence(), seq)
	require.Equal(t, targetHash, svc.GetClosedLedger().Hash())
	hasExtra, err := svc.OpenLedgerHasTx(extraHash)
	require.NoError(t, err)
	require.True(t, hasExtra)

	parent = svc.GetClosedLedger()
	seq, err = svc.AcceptConsensusResult(ctx, parent, nil, nil, target.CloseTime().Add(time.Minute), true)
	require.NoError(t, err)
	require.Equal(t, target.Sequence()+1, seq)
	require.NotEqual(t, targetHash, svc.GetClosedLedger().Hash())
}

func TestService_StartupReplayStandaloneRetriesAfterOpenViewFailure(t *testing.T) {
	ctx := context.Background()
	db, rm := newStartupTestStorage(t, ctx)
	_, target, _ := persistStartupReplayChain(t, ctx, db, rm)
	targetHash := target.Hash()

	svc, err := New(Config{
		Standalone:    true,
		Startup:       StartupConfig{Mode: StartupReplay, Ledger: fmt.Sprintf("%X", targetHash)},
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	closed := svc.closedLedger
	legacyOpen := svc.openLedger
	current := svc.openLedgerView.Current()
	svc.localTxs.PushBack(current.Sequence(), openledger.PendingTx{Blob: []byte{0xff}, Hash: [32]byte{1}})

	_, err = svc.acceptLedgerAt(ctx, time.Unix(1, 0))
	require.Error(t, err)
	require.NotNil(t, svc.startupReplay)
	require.Same(t, closed, svc.closedLedger)
	require.Same(t, legacyOpen, svc.openLedger)
	require.Same(t, current, svc.openLedgerView.Current())

	svc.localTxs = localtxs.New()
	seq, err := svc.acceptLedgerAt(ctx, time.Unix(1, 0))
	require.NoError(t, err)
	require.Equal(t, target.Sequence(), seq)
	require.Equal(t, targetHash, svc.closedLedger.Hash())
	require.Nil(t, svc.startupReplay)
}

func TestService_StartupReplayConsensusRetriesAfterOpenViewFailure(t *testing.T) {
	ctx := context.Background()
	db, rm := newStartupTestStorage(t, ctx)
	_, target, _ := persistStartupReplayChain(t, ctx, db, rm)
	targetHash := target.Hash()

	svc, err := New(Config{
		Standalone:    false,
		Startup:       StartupConfig{Mode: StartupReplay, Ledger: fmt.Sprintf("%X", targetHash)},
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	closed := svc.closedLedger
	legacyOpen := svc.openLedger
	current := svc.openLedgerView.Current()
	svc.localTxs.PushBack(current.Sequence(), openledger.PendingTx{Blob: []byte{0xff}, Hash: [32]byte{1}})

	_, err = svc.AcceptConsensusResult(ctx, closed, nil, nil, time.Unix(1, 0), true)
	require.Error(t, err)
	require.NotNil(t, svc.startupReplay)
	require.Same(t, closed, svc.closedLedger)
	require.Same(t, legacyOpen, svc.openLedger)
	require.Same(t, current, svc.openLedgerView.Current())

	svc.localTxs = localtxs.New()
	seq, err := svc.AcceptConsensusResult(ctx, closed, nil, nil, time.Unix(1, 0), true)
	require.NoError(t, err)
	require.Equal(t, target.Sequence(), seq)
	require.Equal(t, targetHash, svc.closedLedger.Hash())
	require.Nil(t, svc.startupReplay)
}

func TestService_StartupReplayCancelsAfterPreferredLedgerSwitch(t *testing.T) {
	ctx := context.Background()
	db, rm := newStartupTestStorage(t, ctx)
	_, target, _ := persistStartupReplayChain(t, ctx, db, rm)
	targetHash := target.Hash()

	svc, err := New(Config{
		Standalone:    false,
		Startup:       StartupConfig{Mode: StartupReplay, Ledger: fmt.Sprintf("%X", targetHash)},
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	require.NoError(t, svc.SwitchToPreferredLedger(target))
	seq, err := svc.AcceptConsensusResult(
		ctx,
		target,
		nil,
		nil,
		target.CloseTime().Add(time.Minute),
		true,
	)
	require.NoError(t, err)
	require.Equal(t, target.Sequence()+1, seq)
	require.NotEqual(t, targetHash, svc.GetClosedLedger().Hash())
}

func newStartupTestStorage(t *testing.T, ctx context.Context) (nodestore.Database, *sqlitedb.RepositoryManager) {
	t.Helper()
	db := newTestNodeStore(t, 10_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm := newTestRepositories(t, ctx)
	return db, rm
}

func persistStartupTarget(
	t *testing.T,
	ctx context.Context,
	db nodestore.Database,
	rm *sqlitedb.RepositoryManager,
	withTransaction bool,
) *ledger.Ledger {
	t.Helper()
	if withTransaction {
		_, target, _ := persistStartupReplayChain(t, ctx, db, rm)
		return target
	}
	return persistStartupTargetWithGenesis(t, ctx, db, rm, genesis.DefaultConfig())
}

func persistStartupTargetWithGenesis(
	t *testing.T,
	ctx context.Context,
	db nodestore.Database,
	rm *sqlitedb.RepositoryManager,
	genesisConfig genesis.Config,
) *ledger.Ledger {
	t.Helper()
	writer, err := New(Config{
		Standalone:    true,
		Startup:       StartupConfig{Mode: StartupFresh},
		GenesisConfig: genesisConfig,
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, writer.Start())
	_, err = writer.AcceptLedger(ctx)
	require.NoError(t, err)
	writer.FlushPersists()
	target := writer.GetValidatedLedger()
	writer.Stop()
	return target
}

func persistStartupReplayChain(
	t *testing.T,
	ctx context.Context,
	db nodestore.Database,
	rm *sqlitedb.RepositoryManager,
) (*ledger.Ledger, *ledger.Ledger, [32]byte) {
	t.Helper()
	writer, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, writer.Start())
	parent := writer.GetClosedLedger()
	require.NoError(t, writer.persistValidatedLedger(ctx, parent, false))

	blob, txHash := startupPaymentBlob(t, "startup-replay-destination", 1)
	parsed, err := tx.ParseFromBinary(blob)
	require.NoError(t, err)
	result, err := writer.SubmitTransaction(parsed, blob, false)
	require.NoError(t, err)
	require.True(t, result.Applied)
	_, err = writer.AcceptLedger(ctx)
	require.NoError(t, err)
	writer.FlushPersists()
	target := writer.GetValidatedLedger()
	writer.Stop()
	return parent, target, txHash
}

func startupPaymentBlob(t testing.TB, destinationName string, sequence uint32) ([]byte, [32]byte) {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	master := jtx.MasterAccount()
	destination := jtx.NewAccount(destinationName)
	transaction := payment.Pay(master, destination, 1_000_000).Sequence(sequence).Build()
	env.SignWith(transaction, master)
	txJSON, err := transaction.Flatten()
	require.NoError(t, err)
	blobHex, err := binarycodec.Encode(txJSON)
	require.NoError(t, err)
	blob, err := hex.DecodeString(blobHex)
	require.NoError(t, err)
	txHash, err := tx.ComputeTransactionHash(transaction)
	require.NoError(t, err)
	return blob, txHash
}
