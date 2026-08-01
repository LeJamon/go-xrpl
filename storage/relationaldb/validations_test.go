package relationaldb

import (
	"errors"
	"math"
	"testing"
)

type validationRow struct {
	ledgerSeq  int64
	initialSeq int64
	ledgerHash []byte
	nodePubKey []byte
	signTime   int64
	seenTime   int64
	flags      int64
	raw        []byte
}

func (r validationRow) Scan(dest ...any) error {
	*dest[0].(*int64) = r.ledgerSeq
	*dest[1].(*int64) = r.initialSeq
	*dest[2].(*[]byte) = r.ledgerHash
	*dest[3].(*[]byte) = r.nodePubKey
	*dest[4].(*int64) = r.signTime
	*dest[5].(*int64) = r.seenTime
	*dest[6].(*int64) = r.flags
	*dest[7].(*[]byte) = r.raw
	return nil
}

func TestScanValidationRecordRejectsOutOfRangeIntegers(t *testing.T) {
	base := validationRow{
		ledgerHash: make([]byte, 32),
		nodePubKey: make([]byte, 33),
	}
	tests := []struct {
		name   string
		mutate func(*validationRow)
	}{
		{name: "negative ledger sequence", mutate: func(r *validationRow) { r.ledgerSeq = -1 }},
		{name: "large ledger sequence", mutate: func(r *validationRow) { r.ledgerSeq = math.MaxUint32 + 1 }},
		{name: "negative initial sequence", mutate: func(r *validationRow) { r.initialSeq = -1 }},
		{name: "large initial sequence", mutate: func(r *validationRow) { r.initialSeq = math.MaxUint32 + 1 }},
		{name: "negative flags", mutate: func(r *validationRow) { r.flags = -1 }},
		{name: "large flags", mutate: func(r *validationRow) { r.flags = math.MaxUint32 + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := base
			test.mutate(&row)
			if _, err := ScanValidationRecord(row); !errors.Is(err, ErrInvalidData) {
				t.Fatalf("ScanValidationRecord() error = %v, want ErrInvalidData", err)
			}
		})
	}
}

func TestScanValidationRecordAcceptsUint32Maximum(t *testing.T) {
	row := validationRow{
		ledgerSeq:  math.MaxUint32,
		initialSeq: math.MaxUint32,
		ledgerHash: make([]byte, 32),
		nodePubKey: make([]byte, 33),
		flags:      math.MaxUint32,
	}
	got, err := ScanValidationRecord(row)
	if err != nil {
		t.Fatal(err)
	}
	if got.LedgerSeq != math.MaxUint32 || got.InitialSeq != math.MaxUint32 || got.Flags != math.MaxUint32 {
		t.Fatalf("uint32 maximum did not round trip: %+v", got)
	}
}
