package loadfeetrack_test

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/loadfeetrack"
	"github.com/stretchr/testify/require"
)

// TestNew_NoLoad pins the constructor initial state: all three
// per-source fees equal NormalFee, no raise pending, neither
// IsLoadedLocal nor IsLoadedCluster fires.
func TestNew_NoLoad(t *testing.T) {
	tr := loadfeetrack.New()
	require.Equal(t, loadfeetrack.NormalFee, tr.GetLocalFee())
	require.Equal(t, loadfeetrack.NormalFee, tr.GetRemoteFee())
	require.Equal(t, loadfeetrack.NormalFee, tr.GetClusterFee())
	require.Equal(t, loadfeetrack.NormalFee, tr.GetLoadFactor())
	require.False(t, tr.IsLoadedLocal())
	require.False(t, tr.IsLoadedCluster())
}

// TestScaleFee_NoLoad mirrors LoadFeeTrack_test.cpp:36-86 — under a
// no-load tracker, scaleFeeLoad is the identity for any input fee.
func TestScaleFee_NoLoad(t *testing.T) {
	tr := loadfeetrack.New()
	for _, f := range []uint64{0, 1, 10, 10_000, 1 << 32} {
		scaled, ok := loadfeetrack.ScaleFee(f, tr, false)
		require.True(t, ok)
		require.Equal(t, f, scaled, "fee=%d should be identity under no-load", f)
	}
}

// TestRaiseLocalFee_FirstCallIsNoOp pins
// LoadFeeTrack.cpp:37-38 — the first RaiseLocalFee call increments
// raiseCount but does not change localFee.
func TestRaiseLocalFee_FirstCallIsNoOp(t *testing.T) {
	tr := loadfeetrack.New()
	changed := tr.RaiseLocalFee()
	require.False(t, changed, "first raise must not bump localFee")
	require.Equal(t, loadfeetrack.NormalFee, tr.GetLocalFee())
	// raiseCount > 0 → IsLoadedLocal flips even though the fee hasn't grown.
	require.True(t, tr.IsLoadedLocal())
}

// TestRaiseLocalFee_SecondCallBumps pins
// LoadFeeTrack.cpp:42-47 — the second call raises localFee by ~25%
// (subject to the remoteFee floor).
func TestRaiseLocalFee_SecondCallBumps(t *testing.T) {
	tr := loadfeetrack.New()
	tr.RaiseLocalFee()
	changed := tr.RaiseLocalFee()
	require.True(t, changed)
	// 256 + 256/4 = 320
	require.Equal(t, uint32(320), tr.GetLocalFee())
}

// TestRaiseLocalFee_RemoteFloor pins the
// LoadFeeTrack.cpp:43-44 floor: localFee gets pulled up to remoteFee
// before the 25% bump when remote currently outpaces local.
func TestRaiseLocalFee_RemoteFloor(t *testing.T) {
	tr := loadfeetrack.New()
	tr.SetRemoteFee(1024)
	tr.RaiseLocalFee()
	tr.RaiseLocalFee()
	// localFee was bumped to 1024 then grown by 1024/4 = 1280.
	require.Equal(t, uint32(1280), tr.GetLocalFee())
}

// TestRaiseLocalFee_CappedAtMax pins the
// LoadFeeTrack.cpp:49-50 ceiling: localFee never exceeds FeeMax.
func TestRaiseLocalFee_CappedAtMax(t *testing.T) {
	tr := loadfeetrack.New()
	// Drive far past FeeMax via the remote-fee floor (which only
	// requires a single second-call raise to overshoot the cap).
	tr.SetRemoteFee(loadfeetrack.FeeMax)
	tr.RaiseLocalFee()
	tr.RaiseLocalFee()
	require.Equal(t, loadfeetrack.FeeMax, tr.GetLocalFee(),
		"FeeMax ceiling must clamp localFee")
}

// TestLowerLocalFee_StepsDown pins
// LoadFeeTrack.cpp:67-71 — LowerLocalFee shrinks localFee by 1/4 each
// call and clears raiseCount.
func TestLowerLocalFee_StepsDown(t *testing.T) {
	tr := loadfeetrack.New()
	tr.RaiseLocalFee()
	tr.RaiseLocalFee()
	require.Equal(t, uint32(320), tr.GetLocalFee())
	require.True(t, tr.LowerLocalFee())
	// 320 - 320/4 = 240, clamped up to NormalFee=256.
	require.Equal(t, uint32(256), tr.GetLocalFee())
	require.False(t, tr.IsLoadedLocal(), "raiseCount cleared and localFee==NormalFee")
}

// TestLowerLocalFee_AtNormalIsNoOp pins
// LoadFeeTrack.cpp:73-74 — LowerLocalFee at NormalFee returns false.
func TestLowerLocalFee_AtNormalIsNoOp(t *testing.T) {
	tr := loadfeetrack.New()
	require.False(t, tr.LowerLocalFee())
	require.Equal(t, loadfeetrack.NormalFee, tr.GetLocalFee())
}

// TestScaleFee_LocalLoad verifies fee inflation when local load
// exceeds the base: ScaleFee(fee) = fee * feeFactor / loadBase.
func TestScaleFee_LocalLoad(t *testing.T) {
	tr := loadfeetrack.New()
	tr.RaiseLocalFee()
	tr.RaiseLocalFee() // localFee=320, base=256
	scaled, ok := loadfeetrack.ScaleFee(10, tr, false)
	require.True(t, ok)
	require.Equal(t, uint64(12), scaled) // 10*320/256 = 12 (truncated)
}

// TestScaleFee_UnlimitedClampsToRemoteFloor pins
// LoadFeeTrack.cpp:98-100 — when bUnlimited is true and the local
// factor exceeds the remote floor but not by 4×, the privileged
// caller pays only the remote-floor scaling.
func TestScaleFee_UnlimitedClampsToRemoteFloor(t *testing.T) {
	tr := loadfeetrack.New()
	tr.SetRemoteFee(300)
	tr.RaiseLocalFee()
	tr.RaiseLocalFee() // local jumps to 300 then 375 (<1200=4*300)
	require.Equal(t, uint32(375), tr.GetLocalFee())

	// Without the unlimited clamp the user pays 10*375/256 = 14.
	gen, ok := loadfeetrack.ScaleFee(10, tr, false)
	require.True(t, ok)
	require.Equal(t, uint64(14), gen)

	// With the unlimited clamp the user pays 10*max(remote,cluster)/256 = 10*300/256 = 11.
	priv, ok := loadfeetrack.ScaleFee(10, tr, true)
	require.True(t, ok)
	require.Equal(t, uint64(11), priv)
}

// TestScaleFee_OverflowSafe pins the mulDiv overflow contract:
// (maxUint64 / loadBase) * loadBase fits, but maxUint64 * 2 doesn't.
func TestScaleFee_OverflowSafe(t *testing.T) {
	tr := loadfeetrack.New()
	// Drive local way up so feeFactor > loadBase.
	tr.SetRemoteFee(2 * loadfeetrack.NormalFee)
	tr.RaiseLocalFee()
	tr.RaiseLocalFee()
	_, ok := loadfeetrack.ScaleFee(^uint64(0), tr, false)
	require.False(t, ok, "scaling MaxUint64 by a >1 factor must overflow-flag")
}
