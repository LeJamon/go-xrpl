package entry

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/ledger/entry/schema"
	"github.com/LeJamon/go-xrpl/protocol"
)

func TestTypeRegistryIsBidirectionalAndGenerated(t *testing.T) {
	infos := protocol.LedgerEntryTypes()
	embedded := definitions.Get().LedgerEntryTypes()
	seenCodes := make(map[Type]string, len(infos))
	seenNames := make(map[string]Type, len(infos))
	seenRPCNames := make(map[string]Type, len(infos))
	canonical := 0

	for _, info := range infos {
		if previous, ok := seenCodes[info.Type]; ok {
			t.Errorf("type code 0x%04X is shared by %s and %s", uint16(info.Type), previous, info.Name)
		}
		if previous, ok := seenNames[info.Name]; ok {
			t.Errorf("type name %s is shared by 0x%04X and 0x%04X", info.Name, uint16(previous), uint16(info.Type))
		}
		seenCodes[info.Type] = info.Name
		seenNames[info.Name] = info.Type

		byCode, ok := protocol.LedgerEntryTypeByCode(info.Type)
		if !ok || byCode != info {
			t.Errorf("code lookup for %s did not round-trip", info.Name)
		}
		byName, ok := protocol.LedgerEntryTypeByName(info.Name)
		if !ok || byName != info {
			t.Errorf("name lookup for %s did not round-trip", info.Name)
		}
		if got := info.Type.String(); got != info.Name {
			t.Errorf("Type(0x%04X).String() = %q, want %q", uint16(info.Type), got, info.Name)
		}

		if info.Deprecated {
			if New(info.Type) != nil {
				t.Errorf("deprecated type %s has a generated constructor", info.Name)
			}
			continue
		}
		canonical++
		if info.RPCName == "" {
			t.Errorf("canonical type %s has no RPC name", info.Name)
		} else if previous, ok := seenRPCNames[info.RPCName]; ok {
			t.Errorf("RPC name %s is shared by %s and %s", info.RPCName, previous, info.Type)
		} else {
			seenRPCNames[info.RPCName] = info.Type
		}
		model := New(info.Type)
		if model == nil {
			t.Errorf("canonical type %s has no generated constructor", info.Name)
		} else if model.Type() != info.Type {
			t.Errorf("%s constructor reports type 0x%04X", info.Name, uint16(model.Type()))
		}
		if got := embedded[info.Name]; got != int32(info.Type) {
			t.Errorf("embedded definition %s = %d, want %d", info.Name, got, info.Type)
		}
	}

	if canonical != len(schema.Specs) {
		t.Fatalf("registry has %d canonical types, generated schema has %d", canonical, len(schema.Specs))
	}
	if canonical+1 != len(embedded) || embedded["Invalid"] != -1 {
		t.Fatalf("registry has %d canonical types, embedded definitions have %d entries including Invalid", canonical, len(embedded))
	}
}

func TestTypeStringUnknown(t *testing.T) {
	for _, code := range []uint16{0x0000, 0x0001, 0x00ff, 0xffff} {
		want := fmt.Sprintf("Unknown(0x%04x)", code)
		if got := Type(code).String(); got != want {
			t.Errorf("Type(0x%04X).String() = %q, want %q", code, got, want)
		}
	}
}

func TestSponsorshipLedgerFlags(t *testing.T) {
	if LsfSponsorshipRequireSignForFee != 0x00010000 {
		t.Errorf("LsfSponsorshipRequireSignForFee = 0x%08X", LsfSponsorshipRequireSignForFee)
	}
	if LsfSponsorshipRequireSignForReserve != 0x00020000 {
		t.Errorf("LsfSponsorshipRequireSignForReserve = 0x%08X", LsfSponsorshipRequireSignForReserve)
	}
}
