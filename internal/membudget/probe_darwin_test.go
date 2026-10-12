//go:build darwin

package membudget

import "testing"

func TestProbeDarwinSmoke(t *testing.T) {
	headroom, err := ProbeWithOptions(ProbeOptions{DisableRuntime: true})
	if err != nil {
		t.Fatal(err)
	}
	if headroom.Available == 0 || headroom.Source != "darwin:vm_stat" {
		t.Fatalf("unexpected Darwin headroom: %+v", headroom)
	}
}

func TestParseVMStatAvailable(t *testing.T) {
	data := []byte("Mach Virtual Memory Statistics: (page size of 4096 bytes)\nPages free: 2.\nPages active: 10.\nPages inactive: 3.\nPages speculative: 1.\n")
	got, err := parseVMStatAvailable(data)
	if err != nil {
		t.Fatal(err)
	}
	if got != 6*4096 {
		t.Fatalf("unexpected vm_stat result: %d", got)
	}
}
