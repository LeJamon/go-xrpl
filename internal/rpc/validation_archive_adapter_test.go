package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/storage/relationaldb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeValidationRepo is the storage-layer counterpart to
// fakeValidationArchive: it lets us exercise the adapter's row→DTO
// projection in isolation, without standing up a SQLite database.
type fakeValidationRepo struct {
	rows  []*relationaldb.ValidationRecord
	count int64
}

func (f *fakeValidationRepo) Save(ctx context.Context, v *relationaldb.ValidationRecord) error {
	return nil
}
func (f *fakeValidationRepo) SaveBatch(ctx context.Context, vs []*relationaldb.ValidationRecord) error {
	return nil
}
func (f *fakeValidationRepo) GetValidationsForLedger(ctx context.Context, seq relationaldb.LedgerIndex) ([]*relationaldb.ValidationRecord, error) {
	return f.rows, nil
}
func (f *fakeValidationRepo) GetValidationsByValidator(ctx context.Context, nodeKey []byte, limit int) ([]*relationaldb.ValidationRecord, error) {
	return f.rows, nil
}
func (f *fakeValidationRepo) GetValidationCount(ctx context.Context) (int64, error) {
	return f.count, nil
}
func (f *fakeValidationRepo) DeleteOlderThanSeq(ctx context.Context, maxSeq relationaldb.LedgerIndex, batchSize int) (int64, error) {
	return 0, nil
}

// TestValidationArchiveAdapter_ProjectsRowsCorrectly pins the
// time-conversion + byte-copy semantics of the adapter. Storage uses
// time.Time; the RPC DTO uses unix seconds. If anyone changes one
// side without the other, the round-trip in this test breaks first.
func TestValidationArchiveAdapter_ProjectsRowsCorrectly(t *testing.T) {
	signTime := time.Unix(1700000300, 0).UTC()
	seenTime := time.Unix(1700000301, 0).UTC()
	hash := relationaldb.Hash{}
	for i := range hash {
		hash[i] = byte(i)
	}
	pubkey := []byte{0x02, 0xAA, 0xBB, 0xCC}
	raw := []byte{0xDE, 0xAD, 0xBE, 0xEF}

	repo := &fakeValidationRepo{
		rows: []*relationaldb.ValidationRecord{
			{
				LedgerSeq:  100,
				LedgerHash: hash,
				NodePubKey: pubkey,
				SignTime:   signTime,
				SeenTime:   seenTime,
				Flags:      7,
				Raw:        raw,
			},
			nil, // adapter must skip nil rows defensively
		},
		count: 42,
	}

	adapter := NewValidationArchiveAdapter(repo)
	require.NotNil(t, adapter)

	out, err := adapter.GetValidationsByValidator(pubkey, 10)
	require.NoError(t, err)
	require.Len(t, out, 1, "nil row must be filtered out, not panic")

	got := out[0]
	assert.EqualValues(t, 100, got.LedgerSeq)
	assert.Equal(t, [32]byte(hash), got.LedgerHash)
	assert.Equal(t, signTime.Unix(), got.SignTimeS, "sign time must round-trip via unix seconds")
	assert.Equal(t, seenTime.Unix(), got.SeenTimeS)
	assert.EqualValues(t, 7, got.Flags)
	assert.Equal(t, pubkey, got.NodePubKey)
	assert.Equal(t, raw, got.Raw)

	// Verify defensive copies: mutating the source must not leak
	// into the DTO that the handler will eventually serialise.
	pubkey[0] = 0xFF
	raw[0] = 0xFF
	assert.NotEqual(t, byte(0xFF), got.NodePubKey[0], "NodePubKey must be a defensive copy")
	assert.NotEqual(t, byte(0xFF), got.Raw[0], "Raw must be a defensive copy")

	count, err := adapter.GetValidationCount()
	require.NoError(t, err)
	assert.EqualValues(t, 42, count)
}

// TestValidationArchiveAdapter_NilRepoReturnsNil documents the
// constructor's fail-soft contract: when the relational DB is not
// configured, NewValidationArchiveAdapter returns nil so the caller
// can leave Services.ValidationArchive unset.
func TestValidationArchiveAdapter_NilRepoReturnsNil(t *testing.T) {
	assert.Nil(t, NewValidationArchiveAdapter(nil))
}
