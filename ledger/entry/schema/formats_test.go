package schema

import (
	"testing"

	"github.com/LeJamon/go-xrpl/protocol"
)

func TestFormatsCoverCanonicalRegistryAndGeneratedSchema(t *testing.T) {
	specs := make(map[string]Entry, len(Specs))
	for _, spec := range Specs {
		specs[spec.Name] = spec
	}
	formats := Formats()

	canonical := 0
	for _, info := range protocol.LedgerEntryTypes() {
		if info.Deprecated {
			continue
		}
		canonical++
		spec, hasSpec := specs[info.Name]
		format, hasFormat := formats[info.Name]
		if !hasSpec || !hasFormat {
			t.Errorf("%s: spec=%t format=%t", info.Name, hasSpec, hasFormat)
			continue
		}

		formatFields := make(map[string]int, len(format))
		for _, field := range format {
			if _, duplicate := formatFields[field.Name]; duplicate {
				t.Errorf("%s.%s appears twice in format", info.Name, field.Name)
			}
			formatFields[field.Name] = field.Style
		}
		for _, field := range spec.Fields {
			if field.Name == "Flags" || field.DecodeOnly {
				continue
			}
			style, ok := formatFields[field.Name]
			if !ok {
				t.Errorf("%s.%s is absent from format", info.Name, field.Name)
				continue
			}
			if style != int(field.Style)-1 {
				t.Errorf("%s.%s format style=%d, schema style=%d", info.Name, field.Name, style, field.Style)
			}
			delete(formatFields, field.Name)
		}
		for name := range formatFields {
			t.Errorf("%s.%s is absent from generated schema", info.Name, name)
		}
	}

	if canonical != len(Specs) || canonical != len(formats) {
		t.Fatalf("canonical=%d specs=%d formats=%d", canonical, len(Specs), len(formats))
	}
}
