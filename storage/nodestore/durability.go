package nodestore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
)

const durableIdentitySize = 8 + 1 + 16 + 8 + 32

var (
	durableIdentityMagic = [8]byte{'g', 'o', 'X', 'R', 'P', 'L', 'd', 'i'}
	durableIdentityKey   = Hash256(sha256.Sum256([]byte("go-xrpl nodestore durable identity v1")))
)

// DurableDatabase supplies the checkpoint protocol with a stable storage
// snapshot and a cancellation-safe durable write. The fingerprint tracks
// managed mutations; it cannot detect out-of-band edits that preserve the
// identity metadata.
type DurableDatabase interface {
	DurableFingerprint(context.Context) ([32]byte, error)
	StoreDurable(context.Context, *Node) error
	WithDurableSnapshot(context.Context, func([32]byte) error) error
}

// WithDurableSnapshot prevents managed destructive mutations while fn checks
// the fingerprint and publishes work derived from the current store contents.
func (d *KVDatabase) WithDurableSnapshot(ctx context.Context, fn func([32]byte) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mutationMu.RLock()
	defer d.mutationMu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	fingerprint, err := d.DurableFingerprint(ctx)
	if err != nil {
		return err
	}
	return fn(fingerprint)
}

// DurableFingerprint returns a stable identity for the current managed store generation.
func (d *KVDatabase) DurableFingerprint(ctx context.Context) ([32]byte, error) {
	if rotating, ok := d.store.(kvstore.RotationIdentityStore); ok {
		identity, err := rotating.RotationIdentity()
		if err != nil {
			return [32]byte{}, err
		}
		if identity.OwnerID == ([16]byte{}) || identity.WritableID == ([32]byte{}) ||
			identity.ArchiveID == ([32]byte{}) ||
			(identity.LastRotated == 0) != (identity.MinimumOnline == 0) ||
			identity.MinimumOnline > identity.LastRotated {
			return [32]byte{}, errors.New("nodestore: invalid rotating durable identity")
		}
		payload := make([]byte, 0, 32+16+32+32+8)
		payload = append(payload, []byte("nodestore-node-codec-v1/rotate-v1")...)
		payload = append(payload, identity.OwnerID[:]...)
		payload = append(payload, identity.WritableID[:]...)
		payload = append(payload, identity.ArchiveID[:]...)
		payload = binary.BigEndian.AppendUint32(payload, identity.LastRotated)
		payload = binary.BigEndian.AppendUint32(payload, identity.MinimumOnline)
		return sha256.Sum256(payload), nil
	}

	d.identityMu.Lock()
	defer d.identityMu.Unlock()
	data, err := d.FetchDataUncached(ctx, durableIdentityKey)
	if err != nil {
		return [32]byte{}, err
	}
	if data == nil {
		data, err = newDurableIdentity()
		if err != nil {
			return [32]byte{}, err
		}
		if err := d.StoreDurable(ctx, &Node{
			Type: NodeLedger, Hash: durableIdentityKey, Data: data, LedgerSeq: math.MaxUint32,
		}); err != nil {
			return [32]byte{}, fmt.Errorf("persist NodeStore identity: %w", err)
		}
	}
	if _, _, err := decodeDurableIdentity(data); err != nil {
		return [32]byte{}, err
	}
	payload := append([]byte("nodestore-node-codec-v1/plain-v1"), data...)
	return sha256.Sum256(payload), nil
}

func newDurableIdentity() ([]byte, error) {
	data := make([]byte, 0, durableIdentitySize)
	data = append(data, durableIdentityMagic[:]...)
	data = append(data, 1)
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, fmt.Errorf("generate NodeStore identity: %w", err)
	}
	data = append(data, id[:]...)
	data = binary.BigEndian.AppendUint64(data, 0)
	checksum := sha256.Sum256(data)
	return append(data, checksum[:]...), nil
}

func decodeDurableIdentity(data []byte) ([16]byte, uint64, error) {
	var id [16]byte
	if len(data) != durableIdentitySize {
		return id, 0, errors.New("nodestore: malformed durable identity")
	}
	if string(data[:8]) != string(durableIdentityMagic[:]) || data[8] != 1 {
		return id, 0, errors.New("nodestore: unsupported durable identity")
	}
	want := sha256.Sum256(data[:durableIdentitySize-32])
	if string(data[durableIdentitySize-32:]) != string(want[:]) {
		return id, 0, errors.New("nodestore: durable identity checksum mismatch")
	}
	copy(id[:], data[9:25])
	if id == ([16]byte{}) {
		return id, 0, errors.New("nodestore: empty durable identity")
	}
	return id, binary.BigEndian.Uint64(data[25:33]), nil
}

func (d *KVDatabase) bumpDurableMutation(ctx context.Context) error {
	if _, rotating := d.store.(kvstore.RotationIdentityStore); rotating {
		return nil
	}
	d.identityMu.Lock()
	defer d.identityMu.Unlock()
	data, err := d.FetchDataUncached(ctx, durableIdentityKey)
	if err != nil || data == nil {
		return err
	}
	id, generation, err := decodeDurableIdentity(data)
	if err != nil {
		return err
	}
	if generation == math.MaxUint64 {
		return errors.New("nodestore: durable mutation generation exhausted")
	}
	payload := make([]byte, 0, durableIdentitySize)
	payload = append(payload, durableIdentityMagic[:]...)
	payload = append(payload, 1)
	payload = append(payload, id[:]...)
	payload = binary.BigEndian.AppendUint64(payload, generation+1)
	checksum := sha256.Sum256(payload)
	payload = append(payload, checksum[:]...)
	return d.StoreDurable(context.WithoutCancel(ctx), &Node{
		Type: NodeLedger, Hash: durableIdentityKey, Data: payload, LedgerSeq: math.MaxUint32,
	})
}
