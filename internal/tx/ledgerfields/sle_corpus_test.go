package ledgerfields

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields/spec"
)

type sleCorpusManifest struct {
	Schema        int                            `json:"schema"`
	ExpectedTypes []string                       `json:"expected_types"`
	Provenance    map[string]sleCorpusProvenance `json:"provenance"`
	Vectors       []sleCorpusVector              `json:"vectors"`
}

type sleCorpusProvenance struct {
	Independent bool   `json:"independent"`
	Source      string `json:"source"`
	Detail      string `json:"detail"`
}

type sleCorpusVector struct {
	ID              string `json:"id"`
	LedgerEntryType string `json:"ledger_entry_type"`
	Coverage        string `json:"coverage"`
	Provenance      string `json:"provenance"`
	Hex             string `json:"hex"`
}

type sleCorpusCase struct {
	Vector sleCorpusVector
	Raw    []byte
	Entry  Entry
	Fields map[string]any
}

type sleCorpusEncoder interface {
	Encode() ([]byte, error)
}

type sleCorpusMapper interface {
	ToMap() map[string]any
}

func loadSLECorpus(t *testing.T) sleCorpusManifest {
	t.Helper()

	data, err := os.ReadFile("testdata/sle-corpus.json")
	if err != nil {
		t.Fatalf("read SLE corpus: %v", err)
	}
	var manifest sleCorpusManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode SLE corpus: %v", err)
	}
	return manifest
}

func decodeSLECorpusCase(t *testing.T, vector sleCorpusVector) sleCorpusCase {
	t.Helper()

	raw, err := hex.DecodeString(vector.Hex)
	if err != nil {
		t.Fatalf("decode fixed hex: %v", err)
	}
	entry := New(vector.LedgerEntryType)
	if entry == nil {
		t.Fatalf("New(%q) returned nil", vector.LedgerEntryType)
	}
	if err := entry.Decode(raw); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	mapper, ok := entry.(sleCorpusMapper)
	if !ok {
		t.Fatalf("%T does not implement ToMap", entry)
	}
	return sleCorpusCase{
		Vector: vector,
		Raw:    raw,
		Entry:  entry,
		Fields: mapper.ToMap(),
	}
}

func fullSLECorpusCases(t *testing.T) []sleCorpusCase {
	t.Helper()

	manifest := loadSLECorpus(t)
	cases := make([]sleCorpusCase, 0, len(manifest.ExpectedTypes))
	for _, vector := range manifest.Vectors {
		if vector.Coverage == "full" {
			cases = append(cases, decodeSLECorpusCase(t, vector))
		}
	}
	return cases
}

func assertSLECorpusEncoderParity(t *testing.T, corpusCase sleCorpusCase, encoder sleCorpusEncoder) {
	t.Helper()

	encoded, err := encoder.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !bytes.Equal(encoded, corpusCase.Raw) {
		t.Fatalf("raw parity mismatch:\nwant %X\n got %X", corpusCase.Raw, encoded)
	}
}

func assertSLECorpusRawParity(t *testing.T, corpusCase sleCorpusCase) {
	t.Helper()

	encoder, ok := corpusCase.Entry.(sleCorpusEncoder)
	if !ok {
		t.Fatalf("%T does not implement Encode", corpusCase.Entry)
	}
	assertSLECorpusEncoderParity(t, corpusCase, encoder)
}

func TestSLECorpusManifest(t *testing.T) {
	manifest := loadSLECorpus(t)
	if manifest.Schema != 1 {
		t.Fatalf("schema = %d, want 1", manifest.Schema)
	}
	registered := make([]string, 0, len(registry))
	for name := range registry {
		registered = append(registered, name)
	}
	slices.Sort(registered)
	if !slices.Equal(manifest.ExpectedTypes, registered) {
		t.Fatalf("expected_types do not match registered entries:\nmanifest %v\nregistry %v", manifest.ExpectedTypes, registered)
	}

	ids := make(map[string]bool, len(manifest.Vectors))
	full := make(map[string]int, len(manifest.ExpectedTypes))
	for _, vector := range manifest.Vectors {
		if vector.ID == "" || ids[vector.ID] {
			t.Fatalf("missing or duplicate vector id %q", vector.ID)
		}
		ids[vector.ID] = true
		origin, ok := manifest.Provenance[vector.Provenance]
		if !ok {
			t.Fatalf("%s: unknown provenance %q", vector.ID, vector.Provenance)
		}
		if origin.Source == "" || origin.Detail == "" {
			t.Fatalf("%s: incomplete provenance %q", vector.ID, vector.Provenance)
		}
		if !origin.Independent {
			t.Fatalf("%s: provenance %q is not independent", vector.ID, vector.Provenance)
		}
		if vector.Hex == "" || vector.Hex != strings.ToUpper(vector.Hex) {
			t.Fatalf("%s: hex must be non-empty uppercase", vector.ID)
		}
		if _, err := hex.DecodeString(vector.Hex); err != nil {
			t.Fatalf("%s: invalid fixed hex: %v", vector.ID, err)
		}
		if vector.Coverage == "full" {
			full[vector.LedgerEntryType]++
		} else if vector.Coverage != "real" {
			t.Fatalf("%s: unknown coverage %q", vector.ID, vector.Coverage)
		}
	}
	for _, name := range manifest.ExpectedTypes {
		if full[name] != 1 {
			t.Errorf("%s has %d full vectors, want 1", name, full[name])
		}
	}
}

func TestSLECorpusDecodeEncodeRawParity(t *testing.T) {
	manifest := loadSLECorpus(t)
	for _, vector := range manifest.Vectors {
		t.Run(vector.ID, func(t *testing.T) {
			corpusCase := decodeSLECorpusCase(t, vector)
			if got := corpusCase.Fields["LedgerEntryType"]; got != vector.LedgerEntryType {
				t.Fatalf("LedgerEntryType = %v, want %s", got, vector.LedgerEntryType)
			}
			assertSLECorpusRawParity(t, corpusCase)
		})
	}
}

func TestSLECorpusFullVectorsCoverSpecs(t *testing.T) {
	byType := make(map[string]sleCorpusCase, len(registry))
	for _, corpusCase := range fullSLECorpusCases(t) {
		byType[corpusCase.Vector.LedgerEntryType] = corpusCase
	}

	for _, entrySpec := range spec.Specs {
		corpusCase, ok := byType[entrySpec.Name]
		if !ok {
			t.Errorf("%s: missing full corpus vector", entrySpec.Name)
			continue
		}
		for _, field := range entrySpec.Fields {
			if field.DecodeOnly {
				continue
			}
			if _, ok := corpusCase.Fields[field.Name]; !ok && field.Style != spec.StyleDefault {
				t.Errorf("%s: full vector missing %s", entrySpec.Name, field.Name)
			}
		}
	}
}
