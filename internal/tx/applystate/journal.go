package applystate

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

type journal struct {
	overlay *overlay.Overlay
	err     error
	cfg     overlay.Config
}

type journalRecord struct {
	item  *TrackedEntry
	owner *threadedOwner
}

const (
	journalMagic          uint32 = 0x41535431 // "AST1"
	journalVersion               = byte(1)
	journalFlagItem              = byte(1 << 0)
	journalFlagOwner             = byte(1 << 1)
	journalFlagReinserted        = byte(1 << 0)
	journalFlagThreadPrev        = byte(1 << 1)
	nilBlobLength         uint32 = math.MaxUint32
)

func newJournal(cfg overlay.Config) *journal {
	o, err := overlay.New(nil, cfg)
	if err != nil {
		return &journal{err: err, cfg: cfg}
	}
	return &journal{overlay: o, cfg: cfg}
}

func (j *journal) fork() *journal {
	if j.err != nil {
		return &journal{err: j.err, cfg: j.cfg}
	}
	child, err := j.overlay.Fork()
	if err != nil {
		return &journal{err: err, cfg: j.cfg}
	}
	return &journal{overlay: child, cfg: j.cfg}
}

// ExecutionJournalConfig shares admission accounts with nested transaction tables.
func (t *ApplyStateTable) ExecutionJournalConfig() overlay.Config {
	return t.journal.cfg
}

func (j *journal) close() error {
	if j.err != nil {
		return nil
	}
	return j.overlay.Close()
}

func (j *journal) get(ctx context.Context, key [32]byte) (journalRecord, bool, error) {
	if j.err != nil {
		return journalRecord{}, false, j.err
	}
	value, found, err := j.overlay.Get(ctx, key)
	if err != nil || !found {
		return journalRecord{}, found, err
	}
	record, err := decodeJournalRecord(value)
	if err != nil {
		return journalRecord{}, false, err
	}
	return record, true, nil
}

func (j *journal) put(ctx context.Context, key [32]byte, record journalRecord) error {
	if j.err != nil {
		return j.err
	}
	if record.item == nil && record.owner == nil {
		return j.overlay.DeleteContext(ctx, key)
	}
	value, err := encodeJournalRecord(record)
	if err != nil {
		return err
	}
	return j.overlay.SetContext(ctx, key, value)
}

func (j *journal) putItem(ctx context.Context, key [32]byte, item *TrackedEntry) error {
	record, _, err := j.get(ctx, key)
	if err != nil {
		return err
	}
	record.item = item
	return j.put(ctx, key, record)
}

func (j *journal) deleteItem(ctx context.Context, key [32]byte) error {
	record, found, err := j.get(ctx, key)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	record.item = nil
	return j.put(ctx, key, record)
}

func (j *journal) putOwner(ctx context.Context, key [32]byte, owner *threadedOwner) error {
	record, _, err := j.get(ctx, key)
	if err != nil {
		return err
	}
	record.owner = owner
	return j.put(ctx, key, record)
}

func (j *journal) iterate(ctx context.Context, fn func([32]byte, journalRecord) bool) error {
	return j.iterateAfter(ctx, nil, fn)
}

func (j *journal) iterateAfter(ctx context.Context, after *[32]byte, fn func([32]byte, journalRecord) bool) error {
	if j.err != nil {
		return j.err
	}
	it, err := j.overlay.Changes(ctx, after)
	if err != nil {
		return err
	}
	defer func() { _ = it.Close() }()
	for {
		change, done, nextErr := it.Next(ctx)
		if nextErr != nil {
			return nextErr
		}
		if done {
			return nil
		}
		if change.Deleted {
			continue
		}
		record, decodeErr := decodeJournalRecord(change.Value)
		if decodeErr != nil {
			return decodeErr
		}
		if !fn(change.Key, record) {
			return nil
		}
	}
}

func encodeJournalRecord(record journalRecord) ([]byte, error) {
	if record.item == nil && record.owner == nil {
		return nil, errors.New("applystate: empty journal record")
	}
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, journalMagic)
	b.WriteByte(journalVersion)
	var flags byte
	if record.item != nil {
		flags |= journalFlagItem
	}
	if record.owner != nil {
		flags |= journalFlagOwner
	}
	b.WriteByte(flags)
	if record.item != nil {
		item := record.item
		if item.Action < ActionCache || item.Action > ActionErase {
			return nil, fmt.Errorf("applystate: invalid journal action %d", item.Action)
		}
		b.WriteByte(byte(item.Action))
		var itemFlags byte
		if item.reinserted {
			itemFlags |= journalFlagReinserted
		}
		if item.hasThreadPrev {
			itemFlags |= journalFlagThreadPrev
		}
		b.WriteByte(itemFlags)
		if err := writeBlob(&b, item.Original); err != nil {
			return nil, err
		}
		if err := writeBlob(&b, item.Current); err != nil {
			return nil, err
		}
		if item.hasThreadPrev {
			b.Write(item.threadPrevTxnID[:])
			_ = binary.Write(&b, binary.BigEndian, item.threadPrevTxnLgrSq)
		}
	}
	if record.owner != nil {
		owner := record.owner
		_ = binary.Write(&b, binary.BigEndian, uint16(owner.EntryType))
		b.Write(owner.OldPreviousTxnID[:])
		_ = binary.Write(&b, binary.BigEndian, owner.OldPreviousTxnLgrSeq)
		if err := writeBlob(&b, owner.Updated); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}

func writeBlob(b *bytes.Buffer, value []byte) error {
	if value == nil {
		_ = binary.Write(b, binary.BigEndian, nilBlobLength)
		return nil
	}
	if uint64(len(value)) > uint64(math.MaxUint32) {
		return errors.New("applystate: journal value exceeds uint32 length")
	}
	_ = binary.Write(b, binary.BigEndian, uint32(len(value)))
	_, _ = b.Write(value)
	return nil
}

type journalDecoder struct {
	value []byte
	off   int
}

func (d *journalDecoder) take(n int) ([]byte, error) {
	if n < 0 || n > len(d.value)-d.off {
		return nil, errors.New("applystate: truncated journal record")
	}
	part := d.value[d.off : d.off+n]
	d.off += n
	return part, nil
}

func (d *journalDecoder) u8() (byte, error) {
	v, err := d.take(1)
	if err != nil {
		return 0, err
	}
	return v[0], nil
}

func (d *journalDecoder) u16() (uint16, error) {
	v, err := d.take(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(v), nil
}

func (d *journalDecoder) u32() (uint32, error) {
	v, err := d.take(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(v), nil
}

func (d *journalDecoder) blob() ([]byte, error) {
	n, err := d.u32()
	if err != nil {
		return nil, err
	}
	if n == nilBlobLength {
		return nil, nil
	}
	if uint64(n) > uint64(len(d.value)-d.off) {
		return nil, errors.New("applystate: truncated journal blob")
	}
	value, err := d.take(int(n))
	if err != nil {
		return nil, err
	}
	return value[:len(value):len(value)], nil
}

// decodeJournalRecord borrows blob bytes from value. journal.get receives an
// owned overlay value, while iterateAfter consumes its owned iterator value
// during one callback; clipping capacities prevents appends from overwriting
// adjacent encoded fields.
func decodeJournalRecord(value []byte) (journalRecord, error) {
	d := journalDecoder{value: value}
	magicBytes, err := d.take(4)
	if err != nil {
		return journalRecord{}, err
	}
	if binary.BigEndian.Uint32(magicBytes) != journalMagic {
		return journalRecord{}, errors.New("applystate: invalid journal record magic")
	}
	version, err := d.u8()
	if err != nil {
		return journalRecord{}, err
	}
	if version != journalVersion {
		return journalRecord{}, fmt.Errorf("applystate: unsupported journal record version %d", version)
	}
	flags, err := d.u8()
	if err != nil {
		return journalRecord{}, err
	}
	if flags&^(journalFlagItem|journalFlagOwner) != 0 || flags == 0 {
		return journalRecord{}, errors.New("applystate: invalid journal record flags")
	}
	var record journalRecord
	if flags&journalFlagItem != 0 {
		action, err := d.u8()
		if err != nil {
			return journalRecord{}, err
		}
		if action > byte(ActionErase) {
			return journalRecord{}, fmt.Errorf("applystate: invalid journal action %d", action)
		}
		itemFlags, err := d.u8()
		if err != nil {
			return journalRecord{}, err
		}
		if itemFlags&^(journalFlagReinserted|journalFlagThreadPrev) != 0 {
			return journalRecord{}, errors.New("applystate: invalid journal item flags")
		}
		original, err := d.blob()
		if err != nil {
			return journalRecord{}, err
		}
		current, err := d.blob()
		if err != nil {
			return journalRecord{}, err
		}
		item := &TrackedEntry{Action: Action(action), Original: original, Current: current}
		item.reinserted = itemFlags&journalFlagReinserted != 0
		item.hasThreadPrev = itemFlags&journalFlagThreadPrev != 0
		if item.hasThreadPrev {
			prev, err := d.take(32)
			if err != nil {
				return journalRecord{}, err
			}
			copy(item.threadPrevTxnID[:], prev)
			seq, err := d.u32()
			if err != nil {
				return journalRecord{}, err
			}
			item.threadPrevTxnLgrSq = seq
		}
		record.item = item
	}
	if flags&journalFlagOwner != 0 {
		typeValue, err := d.u16()
		if err != nil {
			return journalRecord{}, err
		}
		prev, err := d.take(32)
		if err != nil {
			return journalRecord{}, err
		}
		seq, err := d.u32()
		if err != nil {
			return journalRecord{}, err
		}
		updated, err := d.blob()
		if err != nil {
			return journalRecord{}, err
		}
		owner := &threadedOwner{EntryType: entryTypeFromUint16(typeValue), OldPreviousTxnLgrSeq: seq, Updated: updated}
		copy(owner.OldPreviousTxnID[:], prev)
		record.owner = owner
	}
	if d.off != len(value) {
		return journalRecord{}, errors.New("applystate: trailing journal record bytes")
	}
	return record, nil
}

// Kept in this file so codec changes cannot accidentally depend on the
// protocol's string representation of ledger entry types.
func entryTypeFromUint16(v uint16) entry.Type {
	return entry.Type(v)
}
