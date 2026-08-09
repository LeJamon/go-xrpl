package applystate

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	ledgercore "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/keylet"
	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
)

var errInjectedMutation = errors.New("injected base mutation failure")

type recordedMutation struct {
	action string
	key    [32]byte
}

type recordingBaseView struct {
	*mockBaseView
	mutations []recordedMutation
	reads     [][32]byte
	destroyed drops.XRPAmount
	adjustErr error
	attempts  int
	failAt    int
}

func newRecordingBaseView() *recordingBaseView {
	return &recordingBaseView{mockBaseView: newMockBaseView()}
}

func (m *recordingBaseView) Read(k keylet.Keylet) ([]byte, error) {
	m.reads = append(m.reads, k.Key)
	return m.mockBaseView.Read(k)
}

func (m *recordingBaseView) Insert(k keylet.Keylet, data []byte) error {
	m.attempts++
	if m.failAt == m.attempts {
		return errInjectedMutation
	}
	m.mutations = append(m.mutations, recordedMutation{action: "insert", key: k.Key})
	return m.mockBaseView.Insert(k, data)
}

func (m *recordingBaseView) Update(k keylet.Keylet, data []byte) error {
	m.attempts++
	if m.failAt == m.attempts {
		return errInjectedMutation
	}
	m.mutations = append(m.mutations, recordedMutation{action: "update", key: k.Key})
	return m.mockBaseView.Update(k, data)
}

func (m *recordingBaseView) Erase(k keylet.Keylet) error {
	m.attempts++
	if m.failAt == m.attempts {
		return errInjectedMutation
	}
	m.mutations = append(m.mutations, recordedMutation{action: "erase", key: k.Key})
	return m.mockBaseView.Erase(k)
}

func (m *recordingBaseView) AdjustDropsDestroyed(amount drops.XRPAmount) error {
	if m.adjustErr != nil {
		return m.adjustErr
	}
	m.destroyed = m.destroyed.Add(amount)
	return nil
}

func (m *recordingBaseView) ApplyAtomically(apply func(ledgercore.Writer) error) error {
	staged := newRecordingBaseView()
	for key, data := range m.data {
		staged.data[key] = bytes.Clone(data)
	}
	staged.mutations = append(staged.mutations, m.mutations...)
	staged.reads = append(staged.reads, m.reads...)
	staged.destroyed = m.destroyed
	staged.adjustErr = m.adjustErr
	staged.attempts = m.attempts
	staged.failAt = m.failAt

	err := apply(staged)
	m.attempts = staged.attempts
	m.reads = staged.reads
	if err != nil {
		return err
	}
	m.mockBaseView = staged.mockBaseView
	m.mutations = staged.mutations
	m.destroyed = staged.destroyed
	return nil
}

func encodeApplyTestEntry(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	hexData, err := binarycodec.Encode(fields)
	if err != nil {
		t.Fatalf("encode ledger entry: %v", err)
	}
	data, err := hex.DecodeString(hexData)
	if err != nil {
		t.Fatalf("decode ledger entry: %v", err)
	}
	return data
}

func applyTestAccountRootFields() map[string]any {
	return map[string]any{
		"LedgerEntryType": "AccountRoot",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Balance":         "1000000",
		"Sequence":        uint32(1),
		"OwnerCount":      uint32(0),
		"Flags":           uint32(0),
	}
}

func TestApplyMetadataBuildErrorDoesNotMutateBase(t *testing.T) {
	valid := encodeApplyTestEntry(t, applyTestAccountRootFields())
	invalidFields := applyTestAccountRootFields()
	invalidFields["DestinationTag"] = uint32(1)
	invalid := encodeApplyTestEntry(t, invalidFields)

	base := newRecordingBaseView()
	table := NewApplyStateTable(base, [32]byte{1}, 2, amendment.AllSupportedRules())
	if err := table.Insert(kl(1), valid); err != nil {
		t.Fatalf("insert valid entry: %v", err)
	}
	if err := table.Insert(kl(255), invalid); err != nil {
		t.Fatalf("insert invalid entry: %v", err)
	}
	if err := table.AdjustDropsDestroyed(drops.NewXRPAmount(1)); err != nil {
		t.Fatal(err)
	}
	metadata, err := table.Apply()
	if err == nil {
		t.Fatal("Apply succeeded with a metadata field invalid for AccountRoot")
	}
	if metadata != nil {
		t.Fatalf("Apply metadata = %+v, want nil", metadata)
	}
	var unknownField *ledgerfields.ErrUnknownField
	if !errors.As(err, &unknownField) {
		t.Fatalf("Apply error = %v, want ErrUnknownField", err)
	}
	if unknownField.EntryType != "AccountRoot" || unknownField.TypeCode != 2 || unknownField.FieldCode != 14 {
		t.Fatalf("unexpected unknown field: %+v", unknownField)
	}
	if base.attempts != 0 {
		t.Fatalf("made %d base mutation attempts before returning the build error", base.attempts)
	}
	if len(base.mutations) != 0 {
		t.Fatalf("committed %d base mutations before returning the build error", len(base.mutations))
	}
	if len(base.data) != 0 {
		t.Fatalf("left %d entries in the base after the build error", len(base.data))
	}
	if !base.destroyed.IsZero() {
		t.Fatalf("destroyed %s before returning the build error", base.destroyed)
	}
	if got := table.items[key(1)].Current; !bytes.Equal(got, valid) {
		t.Fatalf("failed apply changed valid tracked entry: got %x want %x", got, valid)
	}
	if len(table.threadOnlyOwners) != 0 {
		t.Fatalf("failed apply retained %d threaded owners", len(table.threadOnlyOwners))
	}
}

func TestApplyBaseWriteErrorRollsBackAndCanRetry(t *testing.T) {
	entry := encodeApplyTestEntry(t, applyTestAccountRootFields())
	base := newRecordingBaseView()
	base.failAt = 2
	table := NewApplyStateTable(base, [32]byte{1}, 2, amendment.AllSupportedRules())
	if err := table.Insert(kl(1), entry); err != nil {
		t.Fatalf("insert first entry: %v", err)
	}
	if err := table.Insert(kl(2), entry); err != nil {
		t.Fatalf("insert second entry: %v", err)
	}
	if err := table.AdjustDropsDestroyed(drops.NewXRPAmount(1)); err != nil {
		t.Fatal(err)
	}

	metadata, err := table.Apply()
	if !errors.Is(err, errInjectedMutation) {
		t.Fatalf("Apply error = %v, want %v", err, errInjectedMutation)
	}
	if metadata != nil {
		t.Fatalf("Apply metadata = %+v, want nil", metadata)
	}
	if base.attempts != 2 {
		t.Fatalf("got %d base mutation attempts, want 2", base.attempts)
	}
	if len(base.mutations) != 0 {
		t.Fatalf("committed %d base mutations after write failure", len(base.mutations))
	}
	if len(base.data) != 0 {
		t.Fatalf("left %d entries in the base after write failure", len(base.data))
	}
	if !base.destroyed.IsZero() {
		t.Fatalf("destroyed %s after write failure", base.destroyed)
	}
	for _, k := range [][32]byte{key(1), key(2)} {
		if got := table.items[k].Current; !bytes.Equal(got, entry) {
			t.Fatalf("failed apply changed tracked entry %x: got %x want %x", k, got, entry)
		}
	}
	if len(table.threadOnlyOwners) != 0 {
		t.Fatalf("failed apply retained %d threaded owners", len(table.threadOnlyOwners))
	}

	metadata, err = table.Apply()
	if err != nil {
		t.Fatalf("retry Apply: %v", err)
	}
	if len(metadata.AffectedNodes) != 2 {
		t.Fatalf("retry affected nodes = %d, want 2", len(metadata.AffectedNodes))
	}
	for _, node := range metadata.AffectedNodes {
		if node.PreviousTxnID != "" || node.PreviousTxnLgrSeq != 0 {
			t.Fatalf("retry emitted self-referential previous transaction: %+v", node)
		}
	}
	if len(base.data) != 2 || len(base.mutations) != 2 {
		t.Fatalf("retry committed data/mutations = %d/%d, want 2/2", len(base.data), len(base.mutations))
	}
	if base.destroyed != drops.NewXRPAmount(1) {
		t.Fatalf("retry destroyed %s, want 1 drop", base.destroyed)
	}
}

func TestApplyDestroyedDropsErrorRollsBackAndCanRetry(t *testing.T) {
	entry := encodeApplyTestEntry(t, applyTestAccountRootFields())
	base := newRecordingBaseView()
	base.adjustErr = errors.New("destroyed drops commit failed")
	table := NewApplyStateTable(base, [32]byte{1}, 2, amendment.AllSupportedRules())
	if err := table.Insert(kl(1), entry); err != nil {
		t.Fatalf("insert entry: %v", err)
	}
	if err := table.AdjustDropsDestroyed(drops.NewXRPAmount(1)); err != nil {
		t.Fatalf("adjust staged drops: %v", err)
	}

	metadata, err := table.Apply()
	if !errors.Is(err, base.adjustErr) {
		t.Fatalf("Apply error = %v, want %v", err, base.adjustErr)
	}
	if metadata != nil {
		t.Fatalf("Apply metadata = %+v, want nil", metadata)
	}
	if len(base.data) != 0 || len(base.mutations) != 0 || !base.destroyed.IsZero() {
		t.Fatalf("failed commit mutated base: entries=%d mutations=%d destroyed=%s", len(base.data), len(base.mutations), base.destroyed)
	}

	base.adjustErr = nil
	if _, err := table.Apply(); err != nil {
		t.Fatalf("retry Apply: %v", err)
	}
	if len(base.data) != 1 || len(base.mutations) != 1 || base.destroyed != drops.NewXRPAmount(1) {
		t.Fatalf("retry did not commit atomically: entries=%d mutations=%d destroyed=%s", len(base.data), len(base.mutations), base.destroyed)
	}
}

func TestApplyStateTableAtomicFailureDoesNotMutateTable(t *testing.T) {
	base := newMockBaseView()
	table := NewApplyStateTable(base, [32]byte{1}, 2, amendment.AllSupportedRules())
	if err := table.Insert(kl(1), []byte{1}); err != nil {
		t.Fatalf("seed table: %v", err)
	}
	injected := errors.New("injected nested apply failure")

	err := table.ApplyAtomically(func(view ledgercore.Writer) error {
		staged := view.(*ApplyStateTable)
		data, err := staged.Read(kl(1))
		if err != nil {
			return err
		}
		data[0] = 9
		if err := view.Insert(kl(2), []byte{2}); err != nil {
			return err
		}
		if err := view.AdjustDropsDestroyed(drops.NewXRPAmount(1)); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("ApplyAtomically error = %v, want %v", err, injected)
	}
	if data, _ := table.Read(kl(1)); !bytes.Equal(data, []byte{1}) {
		t.Fatalf("existing entry changed to %v after failed nested apply", data)
	}
	if exists, _ := table.Exists(kl(2)); exists {
		t.Fatal("failed nested apply inserted a new entry")
	}
	if !table.drops.IsZero() {
		t.Fatalf("failed nested apply destroyed %s", table.drops)
	}
}

func TestApplyStateTableAtomicSuccessCopiesCallbackData(t *testing.T) {
	base := newMockBaseView()
	table := NewApplyStateTable(base, [32]byte{1}, 2, amendment.AllSupportedRules())
	data := []byte{1}

	err := table.ApplyAtomically(func(view ledgercore.Writer) error {
		return view.Insert(kl(1), data)
	})
	if err != nil {
		t.Fatalf("ApplyAtomically: %v", err)
	}

	data[0] = 9
	got, err := table.Read(kl(1))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(got, []byte{1}) {
		t.Fatalf("stored data changed through callback-owned slice: got %v", got)
	}
}

func TestApplyFlushesTrackedItemsInKeyOrder(t *testing.T) {
	entry := encodeApplyTestEntry(t, applyTestAccountRootFields())
	base := newRecordingBaseView()
	table := NewApplyStateTable(base, [32]byte{1}, 2, amendment.AllSupportedRules())

	for i := 32; i >= 1; i-- {
		if err := table.Insert(kl(byte(i)), entry); err != nil {
			t.Fatalf("insert entry %d: %v", i, err)
		}
	}

	metadata, err := table.Apply()
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(base.mutations) != 32 {
		t.Fatalf("got %d mutations, want 32", len(base.mutations))
	}
	if len(metadata.AffectedNodes) != 32 {
		t.Fatalf("got %d affected nodes, want 32", len(metadata.AffectedNodes))
	}
	for i := 1; i <= 32; i++ {
		wantKey := key(byte(i))
		mutation := base.mutations[i-1]
		if mutation.action != "insert" || mutation.key != wantKey {
			t.Fatalf("mutation %d = {%s %x}, want {insert %x}", i-1, mutation.action, mutation.key, wantKey)
		}
		if metadata.AffectedNodes[i-1].LedgerIndex != hexUpper(wantKey) {
			t.Fatalf("affected node %d has index %s, want %s", i-1, metadata.AffectedNodes[i-1].LedgerIndex, hexUpper(wantKey))
		}
	}
}

func TestApplyThreadsTrackedItemsInKeyOrder(t *testing.T) {
	base := newRecordingBaseView()
	table := NewApplyStateTable(base, [32]byte{1}, 2, amendment.AllSupportedRules())
	wantReads := make([][32]byte, 8)

	for i := 8; i >= 1; i-- {
		var accountID [20]byte
		accountID[19] = byte(i)
		account, err := addresscodec.EncodeAccountIDToClassicAddress(accountID[:])
		if err != nil {
			t.Fatalf("encode account %d: %v", i, err)
		}
		entry := encodeApplyTestEntry(t, map[string]any{
			"LedgerEntryType": "Offer",
			"Account":         account,
			"Sequence":        uint32(i),
			"TakerPays":       "1",
			"TakerGets":       "1",
			"BookDirectory":   "0000000000000000000000000000000000000000000000000000000000000000",
			"BookNode":        "0",
			"OwnerNode":       "0",
			"Flags":           uint32(0),
		})
		if err := table.Insert(kl(byte(i)), entry); err != nil {
			t.Fatalf("insert entry %d: %v", i, err)
		}
		wantReads[i-1] = keylet.Account(accountID).Key
	}

	if _, err := table.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(base.reads) != len(wantReads) {
		t.Fatalf("got %d owner reads, want %d", len(base.reads), len(wantReads))
	}
	for i, want := range wantReads {
		if base.reads[i] != want {
			t.Fatalf("owner read %d = %x, want %x", i, base.reads[i], want)
		}
	}
}

func TestApplyFlushesThreadOnlyOwnersInKeyOrder(t *testing.T) {
	base := newRecordingBaseView()
	table := NewApplyStateTable(base, [32]byte{}, 2, amendment.AllSupportedRules())

	for i := 32; i >= 1; i-- {
		ownerKey := key(byte(i))
		table.threadOnlyOwners[ownerKey] = &ThreadedOwner{Updated: []byte{byte(i)}}
	}

	if _, err := table.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(base.mutations) != 32 {
		t.Fatalf("got %d mutations, want 32", len(base.mutations))
	}
	for i := 1; i <= 32; i++ {
		wantKey := key(byte(i))
		mutation := base.mutations[i-1]
		if mutation.action != "update" || mutation.key != wantKey {
			t.Fatalf("mutation %d = {%s %x}, want {update %x}", i-1, mutation.action, mutation.key, wantKey)
		}
	}
}
