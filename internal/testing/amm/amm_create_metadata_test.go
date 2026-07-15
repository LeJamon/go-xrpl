package amm_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/testing/metadata"
)

func TestAMMCreateMetadataOmitsDefaultFields(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.FundWithIOUs(30_000, 0)
	env.Close()

	result := env.Submit(amm.AMMCreate(
		env.Alice,
		amm.XRPAmount(10_000),
		amm.IOUAmount(env.GW, "USD", 10_000),
	).Build())
	if !result.Success {
		t.Fatalf("AMMCreate: %s - %s", result.Code, result.Message)
	}

	ammNode := metadata.FindNode(result.Metadata, "CreatedNode", "AMM")
	if ammNode == nil {
		t.Fatal("AMMCreate metadata has no created AMM node")
	}
	if _, present := ammNode.NewFields["Asset"]; present {
		t.Error("default XRP Asset must be omitted from AMM NewFields")
	}
	if _, present := ammNode.NewFields["Asset2"]; !present {
		t.Error("non-default Asset2 must be present in AMM NewFields")
	}

	lines := metadata.FindNodes(result.Metadata, "CreatedNode", "RippleState")
	if len(lines) != 2 {
		t.Fatalf("created RippleState nodes = %d, want 2", len(lines))
	}
	for _, line := range lines {
		if _, present := line.NewFields["LowNode"]; present {
			t.Errorf("RippleState %s includes default LowNode", line.LedgerIndex)
		}
		if _, present := line.NewFields["HighNode"]; present {
			t.Errorf("RippleState %s includes default HighNode", line.LedgerIndex)
		}
	}
}
