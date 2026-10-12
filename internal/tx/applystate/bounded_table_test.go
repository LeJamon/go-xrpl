package applystate

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	ledgercore "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/keylet"
)

type boundedMockBase struct {
	*mockBaseView
	cfg overlay.Config
}

type boundedRecordingBase struct {
	*recordingBaseView
	cfg overlay.Config
}

func (b *boundedRecordingBase) ExecutionJournalConfig() (overlay.Config, bool) {
	return b.cfg, true
}

func (b *boundedMockBase) ExecutionJournalConfig() (overlay.Config, bool) {
	return b.cfg, true
}

func TestBoundedTableMatchesOverlaySemantics(t *testing.T) {
	base := &boundedMockBase{
		mockBaseView: newMockBaseView(),
		cfg:          overlay.Config{MaxMemoryBytes: 256 << 10},
	}
	base.data[key(1)] = []byte("one")
	table := NewApplyStateTable(base, [32]byte{}, 0, nil)
	if table.journal == nil {
		t.Fatal("provider did not select bounded journal")
	}
	defer table.Close()

	if got, err := table.Read(keylet.Keylet{Key: key(1)}); err != nil || !bytes.Equal(got, []byte("one")) {
		t.Fatalf("Read = %q, %v", got, err)
	}
	if err := table.Update(kl(1), []byte("updated")); err != nil {
		t.Fatal(err)
	}
	if err := table.Insert(kl(2), []byte("two")); err != nil {
		t.Fatal(err)
	}
	if err := table.Erase(kl(2)); err != nil {
		t.Fatal(err)
	}
	if exists, err := table.Exists(kl(2)); err != nil || exists {
		t.Fatalf("Exists erased insert = %v, %v", exists, err)
	}

	keyFound, value, found, err := table.Succ(key(0))
	if err != nil || !found || keyFound != key(1) || !bytes.Equal(value, []byte("updated")) {
		t.Fatalf("Succ = %x, %q, %v, %v", keyFound, value, found, err)
	}
	var seen int
	if err := table.ForEach(func(k [32]byte, value []byte) bool {
		seen++
		return k == key(1) && bytes.Equal(value, []byte("updated"))
	}); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("ForEach yielded %d records", seen)
	}
	if err := table.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBoundedTableForwardsConfigToNestedTable(t *testing.T) {
	base := &boundedMockBase{
		mockBaseView: newMockBaseView(),
		cfg:          overlay.Config{MaxMemoryBytes: 256 << 10},
	}
	outer := NewApplyStateTable(base, [32]byte{1}, 2, amendment.AllSupportedRules())
	defer outer.Close()
	inner := NewApplyStateTable(outer, [32]byte{2}, 3, amendment.AllSupportedRules())
	defer inner.Close()
	if inner.journal == nil {
		t.Fatal("nested table did not inherit bounded journal configuration")
	}
}

func TestBoundedTableForkAtomicFailurePreservesParent(t *testing.T) {
	base := &boundedMockBase{mockBaseView: newMockBaseView(), cfg: overlay.Config{MaxMemoryBytes: 256 << 10}}
	table := NewApplyStateTable(base, [32]byte{}, 0, nil)
	defer table.Close()
	if err := table.Insert(kl(1), []byte("parent")); err != nil {
		t.Fatal(err)
	}
	sentinel := errTestBounded
	if err := table.ApplyAtomically(func(writer ledgercore.Writer) error {
		if err := writer.Insert(kl(2), []byte("child")); err != nil {
			return err
		}
		return sentinel
	}); err != sentinel {
		t.Fatalf("ApplyAtomically error = %v, want %v", err, sentinel)
	}
	if exists, err := table.Exists(kl(2)); err != nil || exists {
		t.Fatalf("failed child leaked: exists=%v err=%v", exists, err)
	}
}

func TestBoundedTableSpillsLargeRecords(t *testing.T) {
	base := &boundedMockBase{
		mockBaseView: newMockBaseView(),
		cfg: overlay.Config{
			MaxMemoryBytes: 16 << 10,
			MaxDiskBytes:   4 << 20,
			SpillDir:       t.TempDir(),
			MaxSegments:    2,
			MaxMergeFanIn:  2,
		},
	}
	table := NewApplyStateTable(base, [32]byte{}, 0, nil)
	for i := byte(0); i < 32; i++ {
		if err := table.Insert(kl(i), bytes.Repeat([]byte{i}, 1024)); err != nil {
			t.Fatalf("Insert %d: %v stats=%+v", i, err, table.journal.overlay.Stats())
		}
	}
	if table.journal.overlay.Stats().Segments == 0 {
		t.Fatalf("large journal did not spill: %+v", table.journal.overlay.Stats())
	}
	if got, err := table.Read(kl(17)); err != nil || len(got) != 1024 || got[0] != 17 {
		var first byte
		if len(got) != 0 {
			first = got[0]
		}
		t.Fatalf("spilled Read = len(%d), first(%d), err(%v)", len(got), first, err)
	}
	if err := table.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBoundedPreviewMatchesLegacyMetadata(t *testing.T) {
	entry := encodeApplyTestEntry(t, applyTestAccountRootFields())
	updatedFields := applyTestAccountRootFields()
	updatedFields["Balance"] = "999990"
	updated := encodeApplyTestEntry(t, updatedFields)

	legacyBase := newRecordingBaseView()
	boundedBase := &boundedRecordingBase{recordingBaseView: newRecordingBaseView(), cfg: overlay.Config{MaxMemoryBytes: 256 << 10}}
	accountKey := keylet.Keylet{Key: key(1)}
	legacyBase.data[accountKey.Key] = bytes.Clone(entry)
	boundedBase.data[accountKey.Key] = bytes.Clone(entry)
	legacy := NewApplyStateTable(legacyBase, [32]byte{2}, 3, amendment.AllSupportedRules())
	bounded := NewApplyStateTable(boundedBase, [32]byte{2}, 3, amendment.AllSupportedRules())
	defer bounded.Close()
	if err := legacy.Update(accountKey, updated); err != nil {
		t.Fatal(err)
	}
	if err := bounded.Update(accountKey, updated); err != nil {
		t.Fatal(err)
	}
	legacyMetadata, err := legacy.Preview()
	if err != nil {
		t.Fatalf("legacy Preview: %v", err)
	}
	boundedMetadata, err := bounded.Preview()
	if err != nil {
		t.Fatalf("bounded Preview: %v", err)
	}
	if !reflect.DeepEqual(legacyMetadata, boundedMetadata) {
		t.Fatalf("metadata differs\\nlegacy=%+v\\nbounded=%+v", legacyMetadata, boundedMetadata)
	}
	if !bytes.Equal(legacyBase.data[accountKey.Key], boundedBase.data[accountKey.Key]) {
		t.Fatal("Preview changed one base view")
	}
}

func TestBoundedApplyThreadsOwnersLikeLegacy(t *testing.T) {
	base := &boundedRecordingBase{recordingBaseView: newRecordingBaseView(), cfg: overlay.Config{MaxMemoryBytes: 256 << 10}}
	table := NewApplyStateTable(base, [32]byte{1}, 2, amendment.AllSupportedRules())
	defer table.Close()
	for i := byte(1); i <= 8; i++ {
		var accountID [20]byte
		accountID[19] = i
		account, err := addresscodec.EncodeAccountIDToClassicAddress(accountID[:])
		if err != nil {
			t.Fatal(err)
		}
		data := encodeApplyTestEntry(t, map[string]any{
			"LedgerEntryType":   "Offer",
			"PreviousTxnID":     "0000000000000000000000000000000000000000000000000000000000000000",
			"PreviousTxnLgrSeq": uint32(0),
			"Account":           account,
			"Sequence":          uint32(i),
			"TakerPays":         "1",
			"TakerGets":         "1",
			"BookDirectory":     "0000000000000000000000000000000000000000000000000000000000000000",
			"BookNode":          "0",
			"OwnerNode":         "0",
			"Flags":             uint32(0),
		})
		if err := table.Insert(kl(i), data); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := table.Apply(); err != nil {
		t.Fatal(err)
	}
	if len(base.reads) != 8 {
		t.Fatalf("owner reads = %d, want 8", len(base.reads))
	}
	for i, got := range base.reads {
		var accountID [20]byte
		accountID[19] = byte(i + 1)
		want := keylet.Account(accountID).Key
		if got != want {
			t.Fatalf("owner read %d = %x, want %x", i, got, want)
		}
	}
}

func TestBoundedApplyFailureCanRetry(t *testing.T) {
	entry := encodeApplyTestEntry(t, applyTestAccountRootFields())
	base := &boundedRecordingBase{recordingBaseView: newRecordingBaseView(), cfg: overlay.Config{MaxMemoryBytes: 256 << 10}}
	base.failAt = 2
	table := NewApplyStateTable(base, [32]byte{1}, 2, amendment.AllSupportedRules())
	defer table.Close()
	if err := table.Insert(kl(1), entry); err != nil {
		t.Fatal(err)
	}
	if err := table.Insert(kl(2), entry); err != nil {
		t.Fatal(err)
	}
	if _, err := table.Apply(); !errors.Is(err, errInjectedMutation) {
		t.Fatalf("Apply error = %v", err)
	}
	if len(base.data) != 0 || len(base.mutations) != 0 {
		t.Fatalf("failed apply mutated base: entries=%d mutations=%d", len(base.data), len(base.mutations))
	}
	base.failAt = 0
	if metadata, err := table.Apply(); err != nil || len(metadata.AffectedNodes) != 2 {
		t.Fatalf("retry Apply = metadata(%v), err(%v)", metadata, err)
	}
	if len(base.data) != 2 || len(base.mutations) != 2 {
		t.Fatalf("retry did not commit: entries=%d mutations=%d", len(base.data), len(base.mutations))
	}
}

var errTestBounded = errors.New("bounded test error")
