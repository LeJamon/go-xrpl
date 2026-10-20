package payment_test

import (
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	ledgercore "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/offer"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

// This synthetic fixture isolates mass self-offer cleanup with real SHAMap
// mutation, invariants, metadata, and atomic block application, but no disk I/O.
// It is inspired by testnet ledger 21685033, not an exact replay of that ledger.
func newSelfOfferCleanupFixture(tb testing.TB, count int) (*ledgercore.Ledger, func() *offer.OfferCreate, [][32]byte) {
	tb.Helper()
	owner := [20]byte{1}
	address := state.EncodeAccountIDSafe(owner)
	base, err := ledgercore.NewOpenWithHeader(
		header.LedgerHeader{LedgerIndex: 21685033, Drops: 100_000_000_000_000_000},
		shamap.New(shamap.TypeState), shamap.New(shamap.TypeTransaction), drops.Fees{})
	if err != nil {
		tb.Fatal(err)
	}
	account := &state.AccountRoot{Account: address, Balance: 1_000_000_000_000, OwnerCount: uint32(count), Sequence: 2000}
	data, err := state.SerializeAccountRoot(account)
	if err != nil {
		tb.Fatal(err)
	}
	if err := base.Insert(keylet.Account(owner), data); err != nil {
		tb.Fatal(err)
	}
	book := keylet.BookDir(keylet.CurrencyBytes("USD"), owner, keylet.CurrencyBytes("XRP"), [20]byte{})
	keys := make([][32]byte, count)
	for i := range count {
		pays := tx.NewIssuedAmountFromFloat64(float64(i+1), "USD", address)
		gets := tx.NewXRPAmount(1_000_000)
		quality := payment.QualityFromAmounts(payment.ToEitherAmount(pays), payment.ToEitherAmount(gets))
		directory := keylet.Quality(book, quality.Value)
		keys[i] = keylet.Offer(owner, uint32(i+1)).Key
		ownerNode, err := state.DirInsert(base, keylet.OwnerDir(owner), keys[i], false, func(dir *state.DirectoryNode) { dir.Owner = owner })
		if err != nil {
			tb.Fatal(err)
		}
		bookNode, err := state.DirInsert(base, directory, keys[i], true, func(dir *state.DirectoryNode) {
			dir.TakerPaysCurrency = keylet.CurrencyBytes("USD")
			dir.TakerPaysIssuer = owner
		})
		if err != nil {
			tb.Fatal(err)
		}
		data, err := state.SerializeLedgerOffer(&state.LedgerOffer{
			Account: address, Sequence: uint32(i + 1), TakerPays: pays, TakerGets: gets,
			BookDirectory: directory.Key, BookNode: bookNode.Page, OwnerNode: ownerNode.Page,
		})
		if err != nil {
			tb.Fatal(err)
		}
		if err := base.Insert(keylet.Keylet{Key: keys[i]}, data); err != nil {
			tb.Fatal(err)
		}
	}
	makeTransaction := func() *offer.OfferCreate {
		txn := offer.NewOfferCreate(address, tx.NewIssuedAmountFromFloat64(1_000_000, "USD", address), tx.NewXRPAmount(1))
		txn.GetCommon().SetSequence(2000)
		txn.GetCommon().Fee = "12"
		return txn
	}
	return base, makeTransaction, keys
}

func selfOfferCleanupConfig() tx.EngineConfig {
	return tx.EngineConfig{
		BaseFee: 10, ReserveBase: 1_000_000, ReserveIncrement: 200_000,
		LedgerSequence: 21685033, NetworkID: 1, Rules: amendment.AllSupportedRules(),
		SkipSignatureVerification: true, Standalone: true,
	}
}

func TestSelfOfferCleanupStateAndMetadata(t *testing.T) {
	// Captured on unmodified main (937b5f490), before the onlyBelow bitmap
	// optimization. Check consensus-visible output, not just tesSUCCESS.
	for _, tc := range []struct {
		count        int
		metadataHash string
		ledgerHash   string
	}{
		{324, "d3c12bda09be5a68606085c1af9beda94b6f1489dbc2eda73d8cc816a97cb9c6", "fe2cb33ef3ee0c51b36b9e7f329991c72a7192abe303a760a3566fe3818433fa"},
		{700, "7b9d14e8092056bb0adb9bf1e7f3c587f1d6be3791ed9c86e16806a0db304aa4", "6b0c24e90ed0c267b068cbff0deac4b6fa444e17608c5fe3cb123f474e4f5e00"},
	} {
		t.Run(fmt.Sprint(tc.count), func(t *testing.T) {
			base, makeTransaction, keys := newSelfOfferCleanupFixture(t, tc.count)
			fresh, err := base.MutableSnapshotUnflushed()
			if err != nil {
				t.Fatal(err)
			}
			eng := engine.NewEngine(fresh, selfOfferCleanupConfig())
			result, err := engine.NewBlockProcessor(eng).ApplyLedgerTransaction(makeTransaction(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if !result.ApplyResult.Applied || result.ApplyResult.Result != ter.TesSUCCESS {
				t.Fatalf("cleanup failed: %+v", result.ApplyResult)
			}
			for _, key := range keys {
				data, err := fresh.Read(keylet.Keylet{Key: key})
				if err != nil || data != nil {
					t.Fatalf("own offer remains: key=%x error=%v", key, err)
				}
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(result.TxWithMetaBlob)); got != tc.metadataHash {
				t.Fatalf("metadata hash=%s, want %s", got, tc.metadataHash)
			}
			if err := fresh.Close(time.Unix(1792505410, 0), 0); err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprintf("%x", fresh.Hash()); got != tc.ledgerHash {
				t.Fatalf("ledger hash=%s, want %s", got, tc.ledgerHash)
			}
		})
	}
}

func BenchmarkSelfOfferCleanup(b *testing.B) {
	for _, count := range []int{324, 700} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			b.StopTimer()
			base, makeTransaction, _ := newSelfOfferCleanupFixture(b, count)
			config := selfOfferCleanupConfig()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				fresh, err := base.MutableSnapshotUnflushed()
				if err != nil {
					b.Fatal(err)
				}
				txn := makeTransaction()
				processor := engine.NewBlockProcessor(engine.NewEngine(fresh, config))
				b.StartTimer()
				result, err := processor.ApplyLedgerTransaction(txn, nil)
				b.StopTimer()
				if err != nil {
					b.Fatal(err)
				}
				if !result.ApplyResult.Applied || result.ApplyResult.Result != ter.TesSUCCESS {
					b.Fatalf("cleanup failed: %+v", result.ApplyResult)
				}
			}
		})
	}
}
