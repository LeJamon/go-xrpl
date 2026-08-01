package main

import (
	"bytes"
	"go/format"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/ledger/entry/schema"
)

func TestGenerateDeterministicAndCurrent(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	generatedDir := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	defs := definitions.Get()

	for _, entry := range schema.Specs {
		t.Run(entry.Name, func(t *testing.T) {
			_, first, err := generate(defs, entry, t.TempDir())
			if err != nil {
				t.Fatalf("first generate: %v", err)
			}
			_, second, err := generate(defs, entry, t.TempDir())
			if err != nil {
				t.Fatalf("second generate: %v", err)
			}
			if !bytes.Equal(first, second) {
				t.Fatal("successive generator runs produced different source")
			}

			formatted, err := format.Source(first)
			if err != nil {
				t.Fatalf("format generated source: %v", err)
			}
			path := filepath.Join(generatedDir, snake(entry.Name)+"_gen.go")
			current, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read generated file: %v", err)
			}
			if !bytes.Equal(formatted, current) {
				t.Fatalf("%s is stale; run go generate ./ledger/entry/...", path)
			}
		})
	}
}

func TestGenerateRejectsInvalidStyles(t *testing.T) {
	defs := definitions.Get()
	tests := []struct {
		name  string
		field schema.Field
	}{
		{name: "unset", field: schema.Field{Name: "Account"}},
		{name: "deferred optional", field: schema.Field{Name: "Account", Style: schema.StyleOptional, DeferredRequired: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := schema.Entry{Name: "AccountRoot", Fields: []schema.Field{test.field}}
			if _, _, err := generate(defs, entry, t.TempDir()); err == nil {
				t.Fatal("generate accepted invalid serialization style")
			}
		})
	}
}

func TestGenerateRejectsInvalidDecodeAliases(t *testing.T) {
	defs := definitions.Get()
	tests := []struct {
		name   string
		fields []schema.Field
		want   string
	}{
		{
			name: "requires decode-only",
			fields: []schema.Field{
				{Name: "Account", Style: schema.StyleOptional, DecodeAlias: "Owner"},
				{Name: "Owner", Style: schema.StyleOptional},
			},
			want: "requires DecodeOnly",
		},
		{
			name: "missing target",
			fields: []schema.Field{
				{Name: "Account", Style: schema.StyleOptional, DecodeOnly: true, DecodeAlias: "Owner"},
			},
			want: "target Owner is not specified",
		},
		{
			name: "mismatched type",
			fields: []schema.Field{
				{Name: "Account", Style: schema.StyleOptional, DecodeOnly: true, DecodeAlias: "Flags"},
				{Name: "Flags", Style: schema.StyleRequired},
			},
			want: "has XRPL type UInt32, want AccountID",
		},
		{
			name: "decode-only target",
			fields: []schema.Field{
				{Name: "Account", Style: schema.StyleOptional, DecodeOnly: true, DecodeAlias: "Owner"},
				{Name: "Owner", Style: schema.StyleOptional, DecodeOnly: true},
			},
			want: "target Owner is decode-only",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := schema.Entry{Name: "NFTokenOffer", Fields: test.fields}
			_, _, err := generate(defs, entry, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("generate error = %v, want %q", err, test.want)
			}
		})
	}
}
