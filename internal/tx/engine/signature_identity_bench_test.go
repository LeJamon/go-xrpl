package engine

import (
	"bytes"
	"fmt"
	"sort"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/sigcache"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

type signatureIdentityCachePath uint8

const (
	signatureIdentityLocalCache signatureIdentityCachePath = iota
	signatureIdentityGlobalCache
	signatureIdentityCold
)

type signatureIdentityFixture struct {
	txn    txcore.Transaction
	raw    []byte
	id     [32]byte
	engine *Engine
}

type signatureIdentityKeypair struct {
	private string
	public  string
	account string
	id      [20]byte
}

func BenchmarkSignatureIdentity(b *testing.B) {
	benchmarks := []struct {
		name  string
		build func(testing.TB) signatureIdentityFixture
		path  signatureIdentityCachePath
	}{
		{name: "Ordinary/LocalCacheHit", build: buildOrdinarySignatureIdentityFixture, path: signatureIdentityLocalCache},
		{name: "Ordinary/GlobalCacheHitColdLocal", build: buildOrdinarySignatureIdentityFixture, path: signatureIdentityGlobalCache},
		{name: "Ordinary/ColdCryptoVerify", build: buildOrdinarySignatureIdentityFixture, path: signatureIdentityCold},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			runSignatureIdentityBenchmark(b, benchmark.build, benchmark.path)
		})
	}

	for _, innerCount := range []int{2, 4, 8} {
		for _, benchmark := range []struct {
			name string
			path signatureIdentityCachePath
		}{
			{name: "LocalCacheHit", path: signatureIdentityLocalCache},
			{name: "GlobalCacheHitColdLocal", path: signatureIdentityGlobalCache},
			{name: "ColdCryptoVerify", path: signatureIdentityCold},
		} {
			b.Run(fmt.Sprintf("Batch%d/%s", innerCount, benchmark.name), func(b *testing.B) {
				runSignatureIdentityBenchmark(b, func(tb testing.TB) signatureIdentityFixture {
					return buildBatchSignatureIdentityFixture(tb, innerCount)
				}, benchmark.path)
			})
		}
	}
}

func runSignatureIdentityBenchmark(
	b *testing.B,
	build func(testing.TB) signatureIdentityFixture,
	path signatureIdentityCachePath,
) {
	b.Helper()
	fixture := build(b)
	b.Cleanup(sigcache.Reset)
	b.ReportAllocs()
	b.SetBytes(int64(len(fixture.raw)))

	sigcache.Reset()
	if path != signatureIdentityCold {
		if got := fixture.engine.verifySignatures(fixture.txn); got != ter.TesSUCCESS {
			b.Fatalf("fixture verification = %s, want tesSUCCESS", got)
		}
		if !fixture.txn.GetCommon().SignatureVerified() {
			b.Fatal("fixture verification did not populate the local signature verdict")
		}
	}
	if path == signatureIdentityGlobalCache {
		fixture.txn.GetCommon().SetRawBytes(fixture.raw)
		assertSignatureIdentityCacheState(b, fixture, true)
	}
	if path == signatureIdentityCold {
		assertSignatureIdentityCacheState(b, fixture, false)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if path != signatureIdentityLocalCache {
			b.StopTimer()
			fixture.txn.GetCommon().SetRawBytes(fixture.raw)
			if path == signatureIdentityCold {
				sigcache.Reset()
			}
			assertSignatureIdentityCacheState(b, fixture, path == signatureIdentityGlobalCache)
			b.StartTimer()
		}

		if got := fixture.engine.verifySignatures(fixture.txn); got != ter.TesSUCCESS {
			b.Fatalf("verifySignatures = %s, want tesSUCCESS", got)
		}
	}
}

func assertSignatureIdentityCacheState(b *testing.B, fixture signatureIdentityFixture, wantGlobal bool) {
	b.Helper()
	if fixture.txn.GetCommon().SignatureVerified() {
		b.Fatal("cache fixture retained a local signature verdict")
	}
	if got := sigcache.Verified(fixture.id); got != wantGlobal {
		b.Fatalf("process cache verdict = %t, want %t", got, wantGlobal)
	}
}

func buildOrdinarySignatureIdentityFixture(tb testing.TB) signatureIdentityFixture {
	tb.Helper()
	registerSignatureIdentityTypes()
	keypair := signatureIdentityKeypairFor(tb, "signature-identity-ordinary")
	txn := txcore.NewBaseTx(txcore.TypeAccountSet, keypair.account)
	txn.Fee = "10"
	txn.SetSequence(1)
	txn.SigningPubKey = keypair.public
	var err error
	txn.TxnSignature, err = sign.SignTransaction(txn, keypair.private)
	if err != nil {
		tb.Fatalf("sign ordinary transaction: %v", err)
	}
	return newSignatureIdentityFixture(tb, txn)
}

func buildBatchSignatureIdentityFixture(tb testing.TB, innerCount int) signatureIdentityFixture {
	tb.Helper()
	registerSignatureIdentityTypes()
	outer := signatureIdentityKeypairFor(tb, "signature-identity-batch-outer")
	batchTxn := batch.NewBatch(outer.account)
	batchTxn.Fee = "1000"
	batchTxn.SetSequence(1)
	batchTxn.SetFlags(batch.BatchFlagAllOrNothing)

	signers := make([]signatureIdentityKeypair, innerCount)
	for i := range signers {
		signer := signatureIdentityKeypairFor(tb, fmt.Sprintf("signature-identity-batch-inner-%d", i))
		signers[i] = signer
		inner := payment.NewPayment(signer.account, outer.account, txcore.NewXRPAmount(int64(i+1)))
		inner.Fee = "0"
		inner.SigningPubKey = ""
		inner.SetSequence(uint32(i + 1))
		inner.SetFlags(txcore.TfInnerBatchTxn)
		batchTxn.AddInnerTransaction(inner)
	}

	message, err := batchTxn.BatchSigningMessage()
	if err != nil {
		tb.Fatalf("build Batch signing message: %v", err)
	}
	sort.Slice(signers, func(i, j int) bool {
		return bytes.Compare(signers[i].id[:], signers[j].id[:]) < 0
	})
	batchTxn.BatchSigners = make([]batch.BatchSigner, len(signers))
	for i, signer := range signers {
		signingData := append(append([]byte(nil), message...), signer.id[:]...)
		batchSignature, signErr := ed25519.Algorithm{}.Sign(string(signingData), signer.private)
		if signErr != nil {
			tb.Fatalf("sign Batch signer %d: %v", i, signErr)
		}
		batchTxn.BatchSigners[i] = batch.BatchSigner{BatchSigner: batch.BatchSignerData{
			Account:           signer.account,
			SigningPubKey:     signer.public,
			BatchTxnSignature: batchSignature,
		}}
	}

	batchTxn.SigningPubKey = outer.public
	batchTxn.TxnSignature, err = sign.SignTransaction(batchTxn, outer.private)
	if err != nil {
		tb.Fatalf("sign Batch outer transaction: %v", err)
	}
	return newSignatureIdentityFixture(tb, batchTxn)
}

func newSignatureIdentityFixture(tb testing.TB, txn txcore.Transaction) signatureIdentityFixture {
	tb.Helper()
	raw, err := txcore.SerializeTransaction(txn)
	if err != nil {
		tb.Fatalf("serialize signature fixture: %v", err)
	}
	parsed, err := txcore.ParseFromBinary(raw)
	if err != nil {
		tb.Fatalf("parse signature fixture: %v", err)
	}
	if !bytes.Equal(parsed.GetRawBytes(), raw) {
		tb.Fatal("parsed signature fixture did not retain canonical wire bytes")
	}
	matches, err := txcore.CurrentFieldsMatchRaw(parsed)
	if err != nil {
		tb.Fatalf("match parsed signature fixture fields: %v", err)
	}
	if !matches {
		tb.Fatal("parsed signature fixture fields do not match wire bytes")
	}
	id, err := txcore.ComputeCurrentTransactionHash(parsed)
	if err != nil {
		tb.Fatalf("compute signature fixture ID: %v", err)
	}

	fixture := signatureIdentityFixture{
		txn:    parsed,
		raw:    raw,
		id:     id,
		engine: verifyingEngine(amendment.AllSupportedRules()),
	}
	sigcache.Reset()
	if got := fixture.engine.preflight(fixture.txn); got != ter.TesSUCCESS {
		tb.Fatalf("preflight parsed signature fixture: %s", got)
	}
	fixture.txn.GetCommon().SetRawBytes(raw)
	sigcache.Reset()
	return fixture
}

func signatureIdentityKeypairFor(tb testing.TB, seed string) signatureIdentityKeypair {
	tb.Helper()
	private, public, err := ed25519.Algorithm{}.DeriveKeypair([]byte(seed), false)
	if err != nil {
		tb.Fatalf("derive signature fixture keypair: %v", err)
	}
	account, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(public)
	if err != nil {
		tb.Fatalf("encode signature fixture account: %v", err)
	}
	id, err := state.DecodeAccountID(account)
	if err != nil {
		tb.Fatalf("decode signature fixture account: %v", err)
	}
	return signatureIdentityKeypair{private: private, public: public, account: account, id: id}
}

func registerSignatureIdentityTypes() {
	for _, entry := range []struct {
		txType   txcore.Type
		register func()
	}{
		{txcore.TypeAccountSet, account.Register},
		{txcore.TypeBatch, batch.Register},
		{txcore.TypePayment, payment.Register},
	} {
		if _, err := txcore.NewFromType(entry.txType); err != nil {
			entry.register()
		}
	}
}
