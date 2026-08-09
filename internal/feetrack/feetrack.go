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
	"math"
	"math/bits"
	"sync"
)

const (
	// LoadBase is the normal/minimum load factor. All load factors are
	// expressed as multiples of this base.
	LoadBase uint32 = 256

	feeIncFraction uint32 = 4
	feeDecFraction uint32 = 4
	feeMax         uint32 = LoadBase * 1_000_000
)

// ErrOverflow indicates ScaleFeeLoad exceeded the signed XRP amount range.
var ErrOverflow = errors.New("feetrack: scaleFeeLoad overflow")

// LoadFeeTrack tracks the local-node fee factor and accepts remote / cluster
// reports. Safe for concurrent access.
type LoadFeeTrack struct {
	mu         sync.RWMutex
	localFee   uint32
	remoteFee  uint32
	clusterFee uint32
	raiseCount uint32
	onChange   func()
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
	changed := t.remoteFee != f
	t.remoteFee = f
	onChange := t.onChange
	t.mu.Unlock()
	if changed && onChange != nil {
		onChange()
	}
}

// RemoteFee returns the last remote-reported fee factor.
func (t *LoadFeeTrack) RemoteFee() uint32 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.remoteFee
}

// SetClusterFee records the cluster-aggregated fee factor.
func (t *LoadFeeTrack) SetClusterFee(f uint32) {
	t.mu.Lock()
	changed := t.clusterFee != f
	t.clusterFee = f
	onChange := t.onChange
	t.mu.Unlock()
	if changed && onChange != nil {
		onChange()
	}
}

// SetOnChange installs a callback for effective fee-factor changes.
func (t *LoadFeeTrack) SetOnChange(fn func()) {
	t.mu.Lock()
	t.onChange = fn
	t.mu.Unlock()
}

// ClusterFee returns the last cluster-reported fee factor.
func (t *LoadFeeTrack) ClusterFee() uint32 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.clusterFee
}

// LocalFee returns the current local load factor.
func (t *LoadFeeTrack) LocalFee() uint32 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.localFee
}

// LoadFactor returns max(cluster, local, remote).
func (t *LoadFeeTrack) LoadFactor() uint32 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return max(t.clusterFee, t.localFee, t.remoteFee)
}

func (t *LoadFeeTrack) scalingFactors() (feeFactor, remFee uint32) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return max(t.localFee, t.remoteFee), max(t.remoteFee, t.clusterFee)
}

// IsLoadedLocal reports whether the local node is currently inflating its fee.
func (t *LoadFeeTrack) IsLoadedLocal() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.raiseCount != 0 || t.localFee != LoadBase
}

func (t *LoadFeeTrack) IsLoadedCluster() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.raiseCount != 0 || t.localFee != LoadBase || t.clusterFee != LoadBase
}

// RaiseLocalFee bumps the local fee factor and reports whether the stored
// factor actually changed. The first call only arms raiseCount; the second and
// subsequent calls scale the local fee up toward its cap, tracking the remote
// fee floor.
func (t *LoadFeeTrack) RaiseLocalFee() bool {
	t.mu.Lock()
	t.raiseCount++
	if t.raiseCount < 2 {
		t.mu.Unlock()
		return false
	}

	orig := t.localFee
	base := max(t.localFee, t.remoteFee)
	raised := uint64(base) + uint64(base)/uint64(feeIncFraction)
	if raised > uint64(feeMax) {
		raised = uint64(feeMax)
	}
	t.localFee = uint32(raised)
	changed := orig != t.localFee
	onChange := t.onChange
	t.mu.Unlock()
	if changed && onChange != nil {
		onChange()
	}
	return changed
}

// LowerLocalFee decays the local fee back toward LoadBase and reports whether
// the stored factor actually changed. It clears the raiseCount latch, so the
// next RaiseLocalFee again needs two ticks to take effect (hysteresis).
func (t *LoadFeeTrack) LowerLocalFee() bool {
	t.mu.Lock()
	orig := t.localFee
	t.raiseCount = 0
	t.localFee -= t.localFee / feeDecFraction
	if t.localFee < LoadBase {
		t.localFee = LoadBase
	}
	changed := orig != t.localFee
	onChange := t.onChange
	t.mu.Unlock()
	if changed && onChange != nil {
		onChange()
	}
	return changed
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
	if fee > math.MaxInt64 {
		return 0, ErrOverflow
	}
	if t == nil {
		return fee, nil
	}
	feeFactor, remFee := t.scalingFactors()
	if unlimited && feeFactor > remFee && feeFactor < 4*remFee {
		feeFactor = remFee
	}

	productHi, productLo := bits.Mul64(fee, uint64(feeFactor))
	if productHi >= uint64(LoadBase) {
		return 0, ErrOverflow
	}
	scaled, _ := bits.Div64(productHi, productLo, uint64(LoadBase))
	if scaled > math.MaxInt64 {
		return 0, ErrOverflow
	}
	return scaled, nil
}
