package conformance

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFinalOutcomeRegressionSnapshots(t *testing.T) {
	cases := map[string]string{
		"loan-broker-cover-withdraw-legacy-freeze.json":        "6b7b4c166282f1ef927cd6cd36c3f87efb038ed5c9c86dbfd8bfa2a61290c133",
		"amm-withdraw-reversed-owner-directory-boundary.json":  "73c9bb0e31c2524bdf851c79eec46e007def1b376a1c7a8efcc1d31ee61bb9e9",
		"amm-create-two-iou-directory-boundary.json":           "8d54f572c5285ad9fe29bcbdf3c4daf5decddc8697ce027080b96910af5a7026",
		"amm-deposit-freeze-before-auth.json":                  "86bc3110adf5b73286dcf6173eb75d2016ea0090bcd426f836b64905b09db57d",
		"amm-withdraw-canonical-asset-precedence.json":         "493f733571dd061c3adfe22c5856ce770defecb4ac2e7d231b29ab7f85b71978",
		"permissioned-domain-ticket-prefx.fixture.json":        "7589aa68f787e3069979300412bbd9a299814d541fb78a9c511e97a466f6e3e7",
		"permissioned-domain-ticket-second-prefx.fixture.json": "1dfe061618525411a1685bcc46fedd14a1d9768110e23381817779fb1dc4874a",
	}
	for name, digest := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "final-outcomes-3.4.1", name))
			if err != nil {
				t.Fatal(err)
			}
			if sha256Hex(data) != digest {
				t.Fatal("oracle fixture digest mismatch")
			}
			fixture, err := decodeSnapshotFixture(data)
			if err != nil {
				t.Fatal(err)
			}
			if err := runSnapshotFixture(fixture); err != nil {
				t.Fatal(err)
			}
		})
	}
}
