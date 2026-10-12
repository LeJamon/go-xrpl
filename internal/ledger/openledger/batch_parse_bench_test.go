package openledger_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	batchtest "github.com/LeJamon/go-xrpl/internal/testing/batch"
	"github.com/LeJamon/go-xrpl/internal/tx"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/offer"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/trustset"
	"github.com/stretchr/testify/require"
)

type batchBenchmarkFixture struct {
	name        string
	count       int
	json        []byte
	binary      []byte
	hash        [32]byte
	innerHashes [][32]byte
}

type batchBenchmarkVariant struct {
	name       string
	fieldHeavy bool
	mixed      bool
}

// BenchmarkOpenLedgerParseJSON and BenchmarkOpenLedgerParseFromBinary isolate
// the two wire-parser paths using the same valid, signed Batch fixtures.
func BenchmarkOpenLedgerParseJSON(b *testing.B) {
	for _, fixture := range newBatchBenchmarkFixtures(b) {
		b.Run(fixture.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(fixture.json)))
			b.ResetTimer()
			for range b.N {
				if _, err := tx.ParseJSON(fixture.json); err != nil {
					b.Fatalf("ParseJSON: %v", err)
				}
			}
		})
	}
}

func BenchmarkOpenLedgerParseFromBinary(b *testing.B) {
	for _, fixture := range newBatchBenchmarkFixtures(b) {
		b.Run(fixture.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(fixture.binary)))
			b.ResetTimer()
			for range b.N {
				if _, err := tx.ParseFromBinary(fixture.binary); err != nil {
					b.Fatalf("ParseFromBinary: %v", err)
				}
			}
		})
	}
}

// BenchmarkOpenLedgerParsePendingTx measures the real ingress preparation path,
// including fresh object parsing and canonical transaction hashing.
func BenchmarkOpenLedgerParsePendingTx(b *testing.B) {
	for _, fixture := range newBatchBenchmarkFixtures(b) {
		b.Run(fixture.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(fixture.binary)))
			b.ResetTimer()
			for range b.N {
				pending, err := openledger.ParsePendingTx(fixture.binary)
				if err != nil {
					b.Fatalf("ParsePendingTx: %v", err)
				}
				if pending.Hash != fixture.hash {
					b.Fatalf("hash changed: got %x want %x", pending.Hash, fixture.hash)
				}
			}
		})
	}
}

// BenchmarkOpenLedgerSubmitBatch measures real open-ledger admission after
// parsing a fresh PendingTx. Setup and view publication remain outside timing.
func BenchmarkOpenLedgerSubmitBatch(b *testing.B) {
	fixture := newBatchAdmissionBenchmarkFixture(b)
	b.ReportAllocs()
	b.SetBytes(int64(len(fixture.blob)))
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		view, err := openledger.New(fixture.parent, openledger.Config{Rules: fixture.apply.Rules})
		if err != nil {
			b.Fatalf("openledger.New: %v", err)
		}
		cfg := fixture.apply
		cfg.LedgerSequence = view.Current().Sequence()
		b.StartTimer()
		pending, err := openledger.ParsePendingTx(fixture.blob)
		if err != nil {
			b.Fatalf("ParsePendingTx: %v", err)
		}
		outcome := view.SubmitDetailed(pending, cfg, nil)
		b.StopTimer()
		if outcome.Class != openledger.ResultSuccess || !outcome.Applied {
			b.Fatalf("SubmitDetailed: class=%d result=%s applied=%v message=%s", outcome.Class, outcome.Result, outcome.Applied, outcome.Message)
		}
		exists, err := view.Current().TxExists(pending.Hash)
		if err != nil || !exists {
			b.Fatalf("SubmitDetailed did not publish %x: exists=%v err=%v", pending.Hash, exists, err)
		}
	}
}

func newBatchBenchmarkFixtures(tb testing.TB) []batchBenchmarkFixture {
	tb.Helper()
	env := jtx.NewTestEnv(tb)
	env.SetVerifySignatures(true)
	outer := jtx.NewAccount("batch-benchmark-outer")
	other := jtx.NewAccount("batch-benchmark-other")
	issuer := jtx.NewAccount("batch-benchmark-issuer")
	destination := jtx.NewAccount("batch-benchmark-destination")
	variants := []batchBenchmarkVariant{
		{name: "compact"},
		{name: "mixed", mixed: true},
		{name: "field-heavy", mixed: true, fieldHeavy: true},
	}

	fixtures := make([]batchBenchmarkFixture, 0, 23)
	for count := 2; count <= 8; count++ {
		for _, variant := range variants {
			fixtures = append(fixtures, buildBatchBenchmarkFixture(
				tb, env, outer, other, issuer, destination, count, variant,
				false, false, false,
			))
		}
	}
	fixtures = append(fixtures,
		buildBatchBenchmarkFixture(tb, env, outer, other, issuer, destination, 4,
			batchBenchmarkVariant{name: "batch-signer-single", mixed: true, fieldHeavy: true},
			true, false, false),
		buildBatchBenchmarkFixture(tb, env, outer, other, issuer, destination, 4,
			batchBenchmarkVariant{name: "outer-multi-signed", mixed: true, fieldHeavy: true},
			false, true, false),
	)
	return fixtures
}

func buildBatchBenchmarkFixture(
	tb testing.TB,
	env *jtx.TestEnv,
	outer, other, issuer, destination *jtx.Account,
	count int,
	variant batchBenchmarkVariant,
	withBatchSigner, outerMulti, admission bool,
) batchBenchmarkFixture {
	tb.Helper()
	numBatchSigners := uint32(0)
	if withBatchSigner {
		numBatchSigners = 1
	}
	flag := batchtx.BatchFlagAllOrNothing
	if outerMulti {
		flag = batchtx.BatchFlagIndependent
		numBatchSigners = 2
	}
	builder := batchtest.NewBatchBuilder(
		outer,
		1,
		batchtest.CalcBatchFee(env.BaseFee(), numBatchSigners, uint32(count)),
		flag,
	)
	for i := 0; i < count; i++ {
		innerAccount := outer
		if withBatchSigner && i%2 == 1 {
			innerAccount = other
		}
		var inner tx.Transaction
		if admission {
			inner = batchtest.MakeInnerAccountSet(innerAccount, uint32(i+2))
			if i%2 == 1 {
				inner.GetCommon().AddMemo("74657874", strings.Repeat("AB", 64), "746578742F706C61696E")
			}
		} else {
			inner = makeBatchBenchmarkInner(innerAccount, issuer, destination, i+2, i, variant.fieldHeavy, variant.mixed)
		}
		builder.AddInnerTx(inner)
	}
	if withBatchSigner {
		builder.AddSigner(other)
	}
	batch := builder.MustBuild()
	if outerMulti {
		signOuterMulti(tb, batch, other, issuer)
	} else {
		env.SignWith(batch, outer)
	}
	return finalizeBatchBenchmarkFixture(tb, batch, count, variant.name)
}

func makeBatchBenchmarkInner(
	account, issuer, destination *jtx.Account,
	sequence, index int,
	fieldHeavy, mixed bool,
) tx.Transaction {
	var inner tx.Transaction
	if !mixed {
		inner = batchtest.MakeInnerPaymentXRP(account, destination, int64(index+1), uint32(sequence))
	} else {
		switch index % 4 {
		case 0:
			inner = batchtest.MakeInnerPaymentXRP(account, destination, int64(index+1), uint32(sequence))
		case 1:
			inner = batchtest.MakeInnerAccountSet(account, uint32(sequence))
		case 2:
			inner = newBatchBenchmarkOffer(account, issuer, uint32(sequence))
		default:
			inner = newBatchBenchmarkTrustSet(account, issuer, uint32(sequence))
		}
	}
	if fieldHeavy {
		common := inner.GetCommon()
		common.AddMemo("74657874", strings.Repeat("AB", 128), "746578742F706C61696E")
		common.AddMemo("6D657461", strings.Repeat("CD", 64), "")
		if inner.TxType() == tx.TypePayment {
			tag := uint32(index + 1)
			common.SourceTag = &tag
		}
	}
	return inner
}

func newBatchBenchmarkOffer(account, issuer *jtx.Account, sequence uint32) *offer.OfferCreate {
	offerTx := offer.NewOfferCreate(account.Address, tx.NewXRPAmount(1_000_000), issuer.IOU("USD", 1))
	configureBatchBenchmarkInner(offerTx.GetCommon(), sequence)
	return offerTx
}

func newBatchBenchmarkTrustSet(account, issuer *jtx.Account, sequence uint32) *trustset.TrustSet {
	trustTx := trustset.NewTrustSet(account.Address, issuer.IOU("USD", 100))
	configureBatchBenchmarkInner(trustTx.GetCommon(), sequence)
	return trustTx
}

func configureBatchBenchmarkInner(common *tx.Common, sequence uint32) {
	common.Fee = "0"
	common.SigningPubKey = ""
	common.Sequence = &sequence
	flags := tx.TfInnerBatchTxn
	common.Flags = &flags
}

func signOuterMulti(tb testing.TB, batch *batchtx.Batch, signers ...*jtx.Account) {
	tb.Helper()
	batch.SigningPubKey = ""
	batch.TxnSignature = ""
	for _, signer := range signers {
		signature, err := sign.SignTransactionForMultiSign(batch, signer.Address, privateKeyHex(signer))
		require.NoError(tb, err)
		require.NoError(tb, sign.AddMultiSigner(batch, signer.Address, signer.PublicKeyHex(), signature))
	}
}

func finalizeBatchBenchmarkFixture(
	tb testing.TB,
	batch *batchtx.Batch,
	count int,
	name string,
) batchBenchmarkFixture {
	tb.Helper()
	common := batch.GetCommon()
	require.True(tb, common.SigningPubKey != "" && common.TxnSignature != "" || len(common.Signers) > 0,
		"%s has no outer signature", name)
	flat, err := batch.Flatten()
	require.NoError(tb, err)
	jsonBlob, err := json.Marshal(flat)
	require.NoError(tb, err)
	binaryBlob, err := tx.SerializeTransaction(batch)
	require.NoError(tb, err)
	parsedJSON, err := tx.ParseJSON(jsonBlob)
	require.NoError(tb, err)
	jsonRoundTrip, err := tx.SerializeTransaction(parsedJSON)
	require.NoError(tb, err)
	require.Equal(tb, binaryBlob, jsonRoundTrip, "%s JSON round-trip changed canonical bytes", name)
	parsedBinary, err := tx.ParseFromBinary(binaryBlob)
	require.NoError(tb, err)
	require.Equal(tb, binaryBlob, parsedBinary.GetRawBytes())
	require.NoError(tb, engine.PrewarmSignature(parsedBinary))
	parsedBatch, ok := parsedBinary.(*batchtx.Batch)
	require.True(tb, ok)
	require.NoError(tb, parsedBatch.VerifyBatchSignatures())
	require.Len(tb, parsedBatch.RawTransactions, count)
	batchHash, err := tx.ComputeTransactionHash(parsedBinary)
	require.NoError(tb, err)
	innerHashes := make([][32]byte, count)
	for i, raw := range parsedBatch.RawTransactions {
		innerHashes[i], err = tx.ComputeTransactionHash(raw.RawTransaction.InnerTx)
		require.NoError(tb, err)
	}
	return batchBenchmarkFixture{
		name:        fmt.Sprintf("%s/inners=%d/bytes=%d", name, count, len(binaryBlob)),
		count:       count,
		json:        append([]byte(nil), jsonBlob...),
		binary:      append([]byte(nil), binaryBlob...),
		hash:        batchHash,
		innerHashes: innerHashes,
	}
}

func privateKeyHex(account *jtx.Account) string {
	if account.IsEd25519() {
		return "ED" + account.PrivateKeyHex()
	}
	return "00" + account.PrivateKeyHex()
}

type batchAdmissionBenchmarkFixture struct {
	blob   []byte
	parent *ledger.Ledger
	apply  openledger.ApplyConfig
}

func newBatchAdmissionBenchmarkFixture(tb testing.TB) batchAdmissionBenchmarkFixture {
	tb.Helper()
	env := jtx.NewTestEnv(tb)
	env.SetVerifySignatures(true)
	env.EnableFeatureNow("BatchV1_1")
	outer := jtx.MasterAccount()
	other := jtx.NewAccount("openledger-batch-benchmark-other")
	issuer := jtx.NewAccount("openledger-batch-benchmark-issuer")
	destination := jtx.NewAccount("openledger-batch-benchmark-destination")
	fixture := buildBatchBenchmarkFixture(
		tb, env, outer, other, issuer, destination, 8,
		batchBenchmarkVariant{name: "admission", mixed: true, fieldHeavy: true},
		false, false, true,
	)
	env.Close()
	parent := env.LastClosedLedger()
	require.NotNil(tb, parent)
	rules := amendment.AllSupportedRules()
	require.True(tb, rules.Enabled(amendment.FeatureBatchV1_1))
	return batchAdmissionBenchmarkFixture{
		blob:   fixture.binary,
		parent: parent,
		apply: openledger.ApplyConfig{
			BaseFee:          env.BaseFee(),
			ReserveBase:      env.ReserveBase(),
			ReserveIncrement: env.ReserveIncrement(),
			Rules:            rules,
		},
	}
}

func TestBatchBenchmarkFixtures(t *testing.T) {
	fixtures := newBatchBenchmarkFixtures(t)
	seenCounts := make(map[int]bool)
	for _, fixture := range fixtures {
		seenCounts[fixture.count] = true
		parsed, err := tx.ParseFromBinary(fixture.binary)
		require.NoError(t, err)
		require.Equal(t, fixture.binary, parsed.GetRawBytes(), fixture.name)
		require.NoError(t, engine.PrewarmSignature(parsed), fixture.name)
		pending, err := openledger.ParsePendingTx(fixture.binary)
		require.NoError(t, err)
		require.Equal(t, fixture.hash, pending.Hash, fixture.name)
		require.Equal(t, fixture.binary, pending.Blob, fixture.name)
		parsedBatch, ok := pending.Parsed.(*batchtx.Batch)
		require.True(t, ok, fixture.name)
		require.Len(t, parsedBatch.RawTransactions, fixture.count, fixture.name)
		for i, raw := range parsedBatch.RawTransactions {
			hash, hashErr := tx.ComputeTransactionHash(raw.RawTransaction.InnerTx)
			require.NoError(t, hashErr)
			require.Equal(t, fixture.innerHashes[i], hash, fixture.name)
		}
	}
	for count := 2; count <= 8; count++ {
		require.True(t, seenCounts[count], "missing batch fixture count %d", count)
	}
}
