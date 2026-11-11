package csf

import (
	"reflect"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
)

func TestEngineConfigMatchesProductionDefaults(t *testing.T) {
	peer := NewSim().CreateGroup(1).Get(0)
	timing := consensus.DefaultTiming()
	timing.LedgerGranularity = 2 * time.Second
	if err := peer.SetParms(timing); err != nil {
		t.Fatalf("SetParms: %v", err)
	}

	got := peer.engineConfig()
	if got.Timing != timing {
		t.Fatalf("Timing = %+v, want peer timing %+v", got.Timing, timing)
	}
	if got.Clock == nil {
		t.Fatal("Clock is nil, want the peer's virtual clock")
	}
	if !got.ManualTick {
		t.Fatal("ManualTick = false, want true")
	}

	want := rcl.DefaultConfig()
	for _, cfg := range []*rcl.Config{&got, &want} {
		cfg.Timing = consensus.Timing{}
		cfg.Clock = nil
		cfg.ManualTick = false
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("csf engine config diverges from rcl.DefaultConfig outside its overrides:\n got: %+v\nwant: %+v", got, want)
	}
}
