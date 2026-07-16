package oracle

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/protocol"
)

func TestValidateOracleUpdateTimeUsesApplicationCloseTime(t *testing.T) {
	config := tx.EngineConfig{
		ParentCloseTime:         1000,
		ApplicationCloseTime:    0,
		ApplicationCloseTimeSet: true,
	}
	lastUpdateTime := uint32(protocol.RippleEpochUnix) + 1000
	if got := validateOracleUpdateTime(config, lastUpdateTime); got != ter.TecINTERNAL {
		t.Fatalf("wrapped application close time: got %s want tecINTERNAL", got)
	}

	config.ApplicationCloseTimeSet = false
	if got := validateOracleUpdateTime(config, lastUpdateTime); got != ter.TesSUCCESS {
		t.Fatalf("parent close time fallback: got %s want tesSUCCESS", got)
	}
}
