package state

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestMPTAmountParse(t *testing.T) {
	data := []byte(`{"value": "9223372036854775807", "mpt_issuance_id": "00000004ae123a8556f3cf91154711376afb0f894f832b3d"}`)
	
	var amt Amount
	err := json.Unmarshal(data, &amt)
	if err != nil {
		t.Fatalf("ERROR: %v", err)
	}
	
	fmt.Printf("IsMPT: %v\n", amt.IsMPT())
	fmt.Printf("IsNative: %v\n", amt.IsNative())
	fmt.Printf("MPTIssuanceID: %s\n", amt.MPTIssuanceID())
	fmt.Printf("Value: %s\n", amt.Value())
	fmt.Printf("IsZero: %v\n", amt.IsZero())
	fmt.Printf("IsNegative: %v\n", amt.IsNegative())
	
	raw, ok := amt.MPTRaw()
	fmt.Printf("MPTRaw: %d, ok: %v\n", raw, ok)
	
	// Test marshaling  
	b, _ := json.Marshal(amt)
	fmt.Printf("JSON: %s\n", string(b))
}
