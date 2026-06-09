// Package feetrack implements the local-node load-fee tracker, mirroring the
// behaviour of rippled's LoadFeeTrack.
//
// The local server raises its load fee under sustained job-queue overload,
// then decays it back to the normal reference fee as the queue drains. Remote
// and cluster fees are set by peers and the cluster announcement path.
// ScaleFeeLoad applies the resulting factor at every fee-quoting boundary.
package feetrack

import (
	"errors"
	"math/big"
	"sync"
)

const (
	// LoadBase is the normal/minimum load factor. All load factors are
	// expressed as multiples of this base.
	LoadBase uint32 = 256

	// FeeIncFraction controls how fast the local fee ramps up on a raise:
	// fee += fee / FeeIncFraction.
	FeeIncFraction uint32 = 4

	// FeeDecFraction controls the decay step on a lower:
	// fee -= fee / FeeDecFraction. Symmetric with FeeIncFraction.
	FeeDecFraction uint32 = 4

	// FeeMax caps the local fee at LoadBase * 1_000_000.
	FeeMax uint32 = 256 * 1_000_000
)

// ErrOverflow indicates ScaleFeeLoad multiplication overflowed uint64.
var ErrOverflow = errors.New("feetrack: scaleFeeLoad overflow")

// LoadFeeTrack tracks the local-node fee factor and accepts remote / cluster
// reports. Safe for concurrent access.
type LoadFeeTrack struct {
	mu         sync.RWMutex
	localFee   uint32
	remoteFee  uint32
	clusterFee uint32
	raiseCount uint32
}

// New returns a LoadFeeTrack initialised to the normal fee with no pending
// raises.
func New() *LoadFeeTrack {
	return &LoadFeeTrack{
		localFee:   LoadBase,
		remoteFee:  LoadBase,
		clusterFee: LoadBase,
	}
}

// SetRemoteFee records a remote-reported fee factor.
func (t *LoadFeeTrack) SetRemoteFee(f uint32) {
	t.mu.Lock()
	t.remoteFee = f
	t.mu.Unlock()
}

// GetRemoteFee returns the last remote-reported fee factor.
func (t *LoadFeeTrack) GetRemoteFee() uint32 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.remoteFee
}

// SetClusterFee records the cluster-aggregated fee factor.
func (t *LoadFeeTrack) SetClusterFee(f uint32) {
	t.mu.Lock()
	t.clusterFee = f
	t.mu.Unlock()
}

// GetClusterFee returns the last cluster-reported fee factor.
func (t *LoadFeeTrack) GetClusterFee() uint32 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.clusterFee
}

// GetLocalFee returns the current local load factor.
func (t *LoadFeeTrack) GetLocalFee() uint32 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.localFee
}

// GetLoadBase returns the reference (normal) fee factor.
func (t *LoadFeeTrack) GetLoadBase() uint32 { return LoadBase }

// GetLoadFactor returns max(cluster, local, remote).
func (t *LoadFeeTrack) GetLoadFactor() uint32 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return maxU32(t.clusterFee, maxU32(t.localFee, t.remoteFee))
}

// GetScalingFactors returns (max(local,remote), max(remote,cluster)), the pair
// consumed by ScaleFeeLoad.
func (t *LoadFeeTrack) GetScalingFactors() (feeFactor, remFee uint32) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return maxU32(t.localFee, t.remoteFee), maxU32(t.remoteFee, t.clusterFee)
}

// IsLoadedLocal reports whether the local node is currently inflating its fee.
func (t *LoadFeeTrack) IsLoadedLocal() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.raiseCount != 0 || t.localFee != LoadBase
}

// IsLoadedCluster reports whether either the local node or the cluster is
// inflating its fee.
func (t *LoadFeeTrack) IsLoadedCluster() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.raiseCount != 0 || t.localFee != LoadBase || t.clusterFee != LoadBase
}

// RaiseLocalFee bumps the local fee factor and reports whether the stored
// factor actually changed. The first call only arms raiseCount; the second and
// subsequent calls scale the local fee up toward FeeMax, tracking the remote
// fee floor.
func (t *LoadFeeTrack) RaiseLocalFee() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.raiseCount++
	if t.raiseCount < 2 {
		return false
	}

	orig := t.localFee
	if t.localFee < t.remoteFee {
		t.localFee = t.remoteFee
	}
	t.localFee += t.localFee / FeeIncFraction
	if t.localFee > FeeMax {
		t.localFee = FeeMax
	}
	return orig != t.localFee
}

// LowerLocalFee decays the local fee back toward LoadBase and reports whether
// the stored factor actually changed. It clears the raiseCount latch, so the
// next RaiseLocalFee again needs two ticks to take effect (hysteresis).
func (t *LoadFeeTrack) LowerLocalFee() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	orig := t.localFee
	t.raiseCount = 0
	t.localFee -= t.localFee / FeeDecFraction
	if t.localFee < LoadBase {
		t.localFee = LoadBase
	}
	return orig != t.localFee
}

// ScaleFeeLoad scales fee by the current local/remote/cluster load.
//
// When unlimited is true and local-only load is moderate (less than 4x the
// remote fee), the privileged caller pays the remote-rate factor instead of
// the local one; local load at or above 4x remote still applies in full.
//
// fee == 0 short-circuits to 0. Overflow surfaces as ErrOverflow.
func ScaleFeeLoad(fee uint64, t *LoadFeeTrack, unlimited bool) (uint64, error) {
	if fee == 0 {
		return 0, nil
	}
	if t == nil {
		return fee, nil
	}
	feeFactor, remFee := t.GetScalingFactors()
	if unlimited && feeFactor > remFee && feeFactor < 4*remFee {
		feeFactor = remFee
	}

	// fee * feeFactor / LoadBase in big.Int to avoid uint64 overflow and keep
	// exact integer truncation.
	num := new(big.Int).Mul(new(big.Int).SetUint64(fee), new(big.Int).SetUint64(uint64(feeFactor)))
	num.Quo(num, new(big.Int).SetUint64(uint64(LoadBase)))
	if !num.IsUint64() {
		return 0, ErrOverflow
	}
	return num.Uint64(), nil
}

func maxU32(a, b uint32) uint32 {
	if a > b {
		return a
	}
	return b
}
