package tx

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields"
)

// BenchmarkBuildModifiedNode_AccountRoot isolates the metadata-build path
// without the rest of the apply pipeline (signing, hashing, SHAMap, ledger
// writes). The original + current blobs model the Balance/Sequence delta
// every Payment produces on both sender and destination.
func BenchmarkBuildModifiedNode_AccountRoot(b *testing.B) {
	original, current, key := buildAccountRootPair(b)

	b.Run("typed", func(b *testing.B) {
		prev := ledgerfields.SetDisabledForBenchmarks(false)
		defer ledgerfields.SetDisabledForBenchmarks(prev)
		runBuildModifiedBench(b, key, original, current)
	})
	b.Run("generic", func(b *testing.B) {
		prev := ledgerfields.SetDisabledForBenchmarks(true)
		defer ledgerfields.SetDisabledForBenchmarks(prev)
		runBuildModifiedBench(b, key, original, current)
	})
}

func runBuildModifiedBench(b *testing.B, key [32]byte, original, current []byte) {
	tbl := &ApplyStateTable{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := tbl.buildModifiedNode(key, original, current)
		if err != nil {
			b.Fatalf("buildModifiedNode: %v", err)
		}
	}
}

func buildAccountRootPair(b *testing.B) (original, current []byte, key [32]byte) {
	b.Helper()
	// Original AccountRoot
	orig := &state.AccountRoot{
		Account:           "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		Balance:           100_000_000_000, // 100,000 XRP
		Sequence:          42,
		OwnerCount:        2,
		Flags:             0,
		PreviousTxnLgrSeq: 12345,
	}
	// One bit set per Hash256 just to give the codec something to round-trip.
	for i := range orig.PreviousTxnID {
		orig.PreviousTxnID[i] = byte(i)
	}
	origBytes, err := state.SerializeAccountRoot(orig)
	if err != nil {
		b.Fatalf("SerializeAccountRoot original: %v", err)
	}

	cur := *orig
	cur.Balance = 99_999_999_990 // sent 10 drops
	cur.Sequence = 43            // bumped
	curBytes, err := state.SerializeAccountRoot(&cur)
	if err != nil {
		b.Fatalf("SerializeAccountRoot current: %v", err)
	}

	for i := range key {
		key[i] = 0xAA
	}
	return origBytes, curBytes, key
}

// BenchmarkBuildModifiedNode_Offer mirrors the AccountRoot bench but for an
// Offer ledger entry whose TakerGets shrunk (the shape of a partially-filled
// offer mid-OfferCreate). Both XRP-amount and IOU-amount offer paths are
// covered.
func BenchmarkBuildModifiedNode_Offer(b *testing.B) {
	original, current, key := buildOfferPair(b)

	b.Run("typed", func(b *testing.B) {
		prev := ledgerfields.SetDisabledForBenchmarks(false)
		defer ledgerfields.SetDisabledForBenchmarks(prev)
		runBuildModifiedBench(b, key, original, current)
	})
	b.Run("generic", func(b *testing.B) {
		prev := ledgerfields.SetDisabledForBenchmarks(true)
		defer ledgerfields.SetDisabledForBenchmarks(prev)
		runBuildModifiedBench(b, key, original, current)
	})
}

func buildOfferPair(b *testing.B) (original, current []byte, key [32]byte) {
	b.Helper()
	orig := &state.LedgerOffer{
		Account:           "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		Sequence:          7,
		TakerPays:         state.NewXRPAmountFromInt(1_000_000_000),
		TakerGets:         state.NewXRPAmountFromInt(2_000_000_000),
		Flags:             0,
		PreviousTxnLgrSeq: 100,
		BookNode:          0,
		OwnerNode:         0,
	}
	for i := range orig.BookDirectory {
		orig.BookDirectory[i] = byte(i + 1)
	}
	for i := range orig.PreviousTxnID {
		orig.PreviousTxnID[i] = byte(i + 2)
	}
	origBytes, err := state.SerializeLedgerOffer(orig)
	if err != nil {
		b.Fatalf("SerializeLedgerOffer original: %v", err)
	}

	cur := *orig
	cur.TakerGets = state.NewXRPAmountFromInt(1_500_000_000) // partial fill
	curBytes, err := state.SerializeLedgerOffer(&cur)
	if err != nil {
		b.Fatalf("SerializeLedgerOffer current: %v", err)
	}

	for i := range key {
		key[i] = 0xBB
	}
	return origBytes, curBytes, key
}

// BenchmarkBuildModifiedNode_DirectoryNode and …_RippleState mirror the
// AccountRoot / Offer benches for the remaining hot-path entry types.
// DirectoryNode is touched by every OfferCreate (book + owner directory
// updates); RippleState by every IOU-leg Payment.

func BenchmarkBuildModifiedNode_DirectoryNode(b *testing.B) {
	original, current, key := buildDirectoryNodePair(b)
	b.Run("typed", func(b *testing.B) {
		prev := ledgerfields.SetDisabledForBenchmarks(false)
		defer ledgerfields.SetDisabledForBenchmarks(prev)
		runBuildModifiedBench(b, key, original, current)
	})
	b.Run("generic", func(b *testing.B) {
		prev := ledgerfields.SetDisabledForBenchmarks(true)
		defer ledgerfields.SetDisabledForBenchmarks(prev)
		runBuildModifiedBench(b, key, original, current)
	})
}

func BenchmarkBuildModifiedNode_RippleState(b *testing.B) {
	original, current, key := buildRippleStatePair(b)
	b.Run("typed", func(b *testing.B) {
		prev := ledgerfields.SetDisabledForBenchmarks(false)
		defer ledgerfields.SetDisabledForBenchmarks(prev)
		runBuildModifiedBench(b, key, original, current)
	})
	b.Run("generic", func(b *testing.B) {
		prev := ledgerfields.SetDisabledForBenchmarks(true)
		defer ledgerfields.SetDisabledForBenchmarks(prev)
		runBuildModifiedBench(b, key, original, current)
	})
}

func buildDirectoryNodePair(b *testing.B) (original, current []byte, key [32]byte) {
	b.Helper()
	orig := &state.DirectoryNode{
		Flags:     0,
		IndexNext: 0,
	}
	for i := range orig.RootIndex {
		orig.RootIndex[i] = byte(i)
	}
	for i := range orig.Owner {
		orig.Owner[i] = byte(i + 1)
	}
	// Add a few index entries so Indexes (sMD_Never) exercises the skip path.
	orig.Indexes = make([][32]byte, 2)
	for i := range orig.Indexes[0] {
		orig.Indexes[0][i] = byte(i + 0x10)
		orig.Indexes[1][i] = byte(i + 0x20)
	}
	origBytes, err := state.SerializeDirectoryNode(orig, false)
	if err != nil {
		b.Fatalf("SerializeDirectoryNode original: %v", err)
	}

	cur := *orig
	cur.IndexNext = 7
	curBytes, err := state.SerializeDirectoryNode(&cur, false)
	if err != nil {
		b.Fatalf("SerializeDirectoryNode current: %v", err)
	}
	for i := range key {
		key[i] = 0xCC
	}
	return origBytes, curBytes, key
}

func buildRippleStatePair(b *testing.B) (original, current []byte, key [32]byte) {
	b.Helper()
	orig := &state.RippleState{
		Balance:           state.NewIssuedAmountFromDecimalString("100", "USD", "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"),
		LowLimit:          state.NewIssuedAmountFromDecimalString("0", "USD", "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"),
		HighLimit:         state.NewIssuedAmountFromDecimalString("1000", "USD", "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"),
		LowNode:           0,
		HighNode:          0,
		Flags:             0,
		PreviousTxnLgrSeq: 10,
	}
	for i := range orig.PreviousTxnID {
		orig.PreviousTxnID[i] = byte(i + 3)
	}
	origBytes, err := state.SerializeRippleState(orig)
	if err != nil {
		b.Fatalf("SerializeRippleState original: %v", err)
	}

	cur := *orig
	cur.Balance = state.NewIssuedAmountFromDecimalString("99.5", "USD", "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK") // partial payment
	curBytes, err := state.SerializeRippleState(&cur)
	if err != nil {
		b.Fatalf("SerializeRippleState current: %v", err)
	}
	for i := range key {
		key[i] = 0xDD
	}
	return origBytes, curBytes, key
}
