package applystate

import (
	"bytes"
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func TestJournalDecodedBlobsDoNotShareRecordBuffers(t *testing.T) {
	j := newJournal(overlay.Config{MaxMemoryBytes: 1 << 20})
	if j.err != nil {
		t.Fatal(j.err)
	}
	defer func() { _ = j.close() }()

	key := [32]byte{1}
	var previous [32]byte
	previous[0] = 9
	record := journalRecord{
		item: &TrackedEntry{
			Action: ActionModify, Original: []byte("original"), Current: []byte("current"),
			reinserted: true, hasThreadPrev: true, threadPrevTxnID: previous, threadPrevTxnLgrSq: 17,
		},
		owner: &threadedOwner{
			EntryType: entry.TypeAccountRoot, OldPreviousTxnID: previous, OldPreviousTxnLgrSeq: 23,
			Updated: []byte("updated"),
		},
	}
	if err := j.put(context.Background(), key, record); err != nil {
		t.Fatal(err)
	}
	first, found, err := j.get(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("first journal get found=%v err=%v", found, err)
	}
	second, found, err := j.get(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("second journal get found=%v err=%v", found, err)
	}
	if first.item == nil || second.item == nil || first.owner == nil || second.owner == nil {
		t.Fatal("decoded journal record lost item or owner")
	}
	for name, value := range map[string][]byte{
		"first original": first.item.Original, "first current": first.item.Current,
		"first updated": first.owner.Updated,
	} {
		if cap(value) != len(value) {
			t.Fatalf("%s capacity=%d, len=%d; append could overwrite adjacent fields", name, cap(value), len(value))
		}
	}

	originalBefore := bytes.Clone(second.item.Original)
	secondCurrentBefore := bytes.Clone(second.item.Current)
	secondUpdatedBefore := bytes.Clone(second.owner.Updated)
	currentBefore := bytes.Clone(first.item.Current)
	updatedBefore := bytes.Clone(first.owner.Updated)
	_ = append(first.item.Original, 0xaa)
	if !bytes.Equal(first.item.Current, currentBefore) {
		t.Fatal("appending Original changed adjacent Current bytes")
	}
	_ = append(first.item.Current, 0xbb)
	if !bytes.Equal(first.owner.Updated, updatedBefore) {
		t.Fatal("appending Current changed adjacent owner bytes")
	}
	first.item.Original[0] ^= 0xff
	if !bytes.Equal(second.item.Original, originalBefore) {
		t.Fatal("separate journal gets share decoded blob storage")
	}
	first.item.Current[0] ^= 0xff
	first.owner.Updated[0] ^= 0xff
	if !bytes.Equal(second.item.Current, secondCurrentBefore) || !bytes.Equal(second.owner.Updated, secondUpdatedBefore) {
		t.Fatal("separate journal gets share current or owner blob storage")
	}
}
