// Package loadfeetrack ports rippled's LoadFeeTrack subsystem
// (src/xrpld/app/misc/detail/LoadFeeTrack.cpp).
//
// LoadFeeTrack manages the server-load fee multiplier: a transient,
// per-node fee bump driven by job-queue saturation (raiseLocalFee /
// lowerLocalFee) plus mirrored remote- and cluster-wide bumps
// (setRemoteFee / setClusterFee). The current factor is fed into
// scaleFeeLoad (ScaleFee here) to inflate per-tx fees when the local
// or wider XRPL network reports load.
//
// Production drivers in goxrpl:
//
//   - RaiseLocalFee / LowerLocalFee — driven on every consensus close
//     by Service.driveLoadFeeTrackLocked: raises when the just-closed
//     ledger showed open-ledger escalation (TxQ.OpenLedgerFeeLevel >
//     ReferenceFeeLevel), decays otherwise. Stands in for rippled's
//     JobQueue-saturation driver: goxrpl has no JobQueue port, and the
//     on-chain escalation signal is the symptom rippled's JobQueue
//     would cause anyway.
//   - SetRemoteFee — not yet wired. Rippled sources this from the
//     median of sfLoadFee fields carried in trusted validations,
//     aggregated in LedgerMaster::checkAccept (LedgerMaster.cpp:977-
//     1006). STValidation already carries sfLoadFee in goxrpl, so the
//     hook is reachable without protobuf changes — see follow-up
//     issue.
//   - SetClusterFee — not yet wired. Rippled sources this from the
//     TMCluster peer-protocol message (ClusterNode.getLoadFee
//     median; PeerImp.cpp:1175-1193). Requires the cluster subsystem
//     which goxrpl does not port today.
//
// server_info reads the per-source fees via the rpc/types.LoadFactorFees
// hook so the load_factor_local / load_factor_net / load_factor_cluster
// fields surface when active.
package loadfeetrack

import (
	"math/bits"
	"sync"
)

// Constants mirror rippled LoadFeeTrack.h:141-147. NormalFee is the
// load-factor scale unit; a tracker reporting NormalFee means "no
// load". FeeMax caps how high RaiseLocalFee can drive the local fee
// (1_000_000× the base load); the inc/dec fractions reflect the 1/4
// step rippled uses on each raise / lower call.
const (
	NormalFee      uint32 = 256
	FeeIncFraction uint32 = 4
	FeeDecFraction uint32 = 4
	FeeMax         uint32 = NormalFee * 1_000_000
	raiseThreshold        = 2 // raiseLocalFee returns false on the first call
)

// Tracker is the concurrent-safe port of rippled's LoadFeeTrack.
// The zero value is NOT ready for use — construct one with New().
type Tracker struct {
	mu sync.Mutex

	localFee   uint32 // localTxnLoadFee_
	remoteFee  uint32 // remoteTxnLoadFee_
	clusterFee uint32 // clusterTxnLoadFee_
	raiseCount uint32
}

// New returns a Tracker initialised to the no-load state: all three
// per-source fees equal NormalFee and raiseCount is zero. Mirrors
// rippled LoadFeeTrack.h:47-55.
func New() *Tracker {
	return &Tracker{
		localFee:   NormalFee,
		remoteFee:  NormalFee,
		clusterFee: NormalFee,
	}
}

// SetRemoteFee mirrors rippled setRemoteFee (LoadFeeTrack.h:59-65) —
// the network-aggregate remote load fee, updated from peer pings.
func (t *Tracker) SetRemoteFee(f uint32) {
	t.mu.Lock()
	t.remoteFee = f
	t.mu.Unlock()
}

// GetRemoteFee mirrors rippled getRemoteFee (LoadFeeTrack.h:67-72).
func (t *Tracker) GetRemoteFee() uint32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.remoteFee
}

// GetLocalFee mirrors rippled getLocalFee (LoadFeeTrack.h:74-79).
func (t *Tracker) GetLocalFee() uint32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.localFee
}

// GetClusterFee mirrors rippled getClusterFee (LoadFeeTrack.h:81-86).
func (t *Tracker) GetClusterFee() uint32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.clusterFee
}

// SetClusterFee mirrors rippled setClusterFee (LoadFeeTrack.h:112-118).
func (t *Tracker) SetClusterFee(f uint32) {
	t.mu.Lock()
	t.clusterFee = f
	t.mu.Unlock()
}

// GetLoadBase mirrors rippled getLoadBase (LoadFeeTrack.h:88-92) and
// returns NormalFee. Callers should compare scaling factors against
// this base — equality means "no load contribution from this source".
func (t *Tracker) GetLoadBase() uint32 {
	return NormalFee
}

// GetLoadFactor mirrors rippled getLoadFactor (LoadFeeTrack.h:94-100):
// the max of the three per-source fees, used for the consolidated
// load_factor / load_factor_server emit.
func (t *Tracker) GetLoadFactor() uint32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return max3(t.clusterFee, t.localFee, t.remoteFee)
}

// GetScalingFactors mirrors rippled getScalingFactors
// (LoadFeeTrack.h:102-110): returns the (feeFactor, uRemFee) pair
// ScaleFee uses to scale the per-tx fee.
//   - feeFactor: max(local, remote) drives the user-visible scaling
//   - uRemFee:   max(remote, cluster) is the privileged-user floor
func (t *Tracker) GetScalingFactors() (feeFactor, uRemFee uint32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return max2(t.localFee, t.remoteFee), max2(t.remoteFee, t.clusterFee)
}

// IsLoadedLocal mirrors rippled isLoadedLocal (LoadFeeTrack.h:125-130).
func (t *Tracker) IsLoadedLocal() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.raiseCount != 0 || t.localFee != NormalFee
}

// IsLoadedCluster mirrors rippled isLoadedCluster (LoadFeeTrack.h:132-138).
func (t *Tracker) IsLoadedCluster() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.raiseCount != 0 || t.localFee != NormalFee || t.clusterFee != NormalFee
}

// RaiseLocalFee mirrors rippled LoadFeeTrack::raiseLocalFee
// (LoadFeeTrack.cpp:32-58). The first call bumps raiseCount only; the
// second and later calls grow localFee by ~25% (subject to a
// remoteFee floor and the FeeMax ceiling). Returns true when the
// effective localFee changes.
func (t *Tracker) RaiseLocalFee() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.raiseCount++
	if t.raiseCount < raiseThreshold {
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

// LowerLocalFee mirrors rippled LoadFeeTrack::lowerLocalFee
// (LoadFeeTrack.cpp:60-79). Clears the raiseCount and shrinks
// localFee toward NormalFee in 1/FeeDecFraction (25%) steps; never
// dips below NormalFee. Returns true when the effective localFee
// changes.
func (t *Tracker) LowerLocalFee() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	orig := t.localFee
	t.raiseCount = 0
	t.localFee -= t.localFee / FeeDecFraction
	if t.localFee < NormalFee {
		t.localFee = NormalFee
	}
	return orig != t.localFee
}

// ScaleFee mirrors rippled scaleFeeLoad (LoadFeeTrack.cpp:84-111):
// fee * feeFactor / loadBase, with the bUnlimited privileged-user
// clamp that lets identified callers pay the remote floor unless
// local load exceeds 4× the remote. Returns the scaled fee; the
// caller is responsible for any subsequent ceiling check. ok=false
// signals an overflow; the caller should reject the transaction.
func ScaleFee(fee uint64, t *Tracker, bUnlimited bool) (scaled uint64, ok bool) {
	if fee == 0 {
		return 0, true
	}

	feeFactor, uRemFee := t.GetScalingFactors()
	if bUnlimited && feeFactor > uRemFee && feeFactor < 4*uRemFee {
		feeFactor = uRemFee
	}

	scaled, ok = mulDiv64(fee, uint64(feeFactor), uint64(t.GetLoadBase()))
	return scaled, ok
}

// mulDiv64 computes (a * b) / c using a 128-bit intermediate so the
// product can grow beyond uint64. Mirrors rippled's mulDiv overflow
// contract: ok=false on overflow or c==0.
func mulDiv64(a, b, c uint64) (uint64, bool) {
	if c == 0 {
		return 0, false
	}
	hi, lo := bits.Mul64(a, b)
	if hi >= c {
		return 0, false
	}
	q, _ := bits.Div64(hi, lo, c)
	return q, true
}

func max2(a, b uint32) uint32 {
	if a >= b {
		return a
	}
	return b
}

func max3(a, b, c uint32) uint32 {
	return max2(max2(a, b), c)
}
