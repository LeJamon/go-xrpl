package manifest

import (
	"encoding/hex"
	"testing"
)

const signedExplicitDefaultVersionManifestHex = "1010000024000000027121ED2C9B9A42D57ADFFD50E6EB1DA543DE93AD97D36D976D2A2B9735E7142F3FB8597321EDC853AD0F0CD2B619AEA92CEEC4FD56A24D6499D584CE79257E45CFD8139B60A77640881D72322E21901DC480158AA2F2B9268EC578FBA8282D5015BF3BFFCF192D43ABF229130020A215AC2621632F8003AEC5B48C20EF081983B002466C8F5ED20A701240D218885EF5EF843D7EABAC583B4AC0845816FF5B4C4DFF9EB3391CC1FE9C73952B0A7DA170377D6D4AEA3274A8FD0D8834BE5B29B519E94036E02B13A2C6CB02"

func TestCacheRejectsExplicitDefaultVersion(t *testing.T) {
	serialized, err := hex.DecodeString(signedExplicitDefaultVersionManifestHex)
	if err != nil {
		t.Fatal(err)
	}

	cache := NewCache()
	if got := cache.ApplyManifest(&Manifest{serialized: serialized}); got != Invalid {
		t.Fatalf("ApplyManifest: got %s, want invalid", got)
	}
	if got := cache.Sequence(); got != 0 {
		t.Fatalf("cache sequence: got %d, want 0", got)
	}
	if len(cache.byMaster) != 0 || len(cache.signingToMaster) != 0 {
		t.Fatal("cache stored an explicitly defaulted manifest")
	}
}
