// Command ledgerfieldsgen emits one typed-decoder Go file per ledger entry
// type listed in internal/tx/ledgerfields/spec.Specs. For each spec entry it
// looks up every field's XRPL type and ordinal in
// codec/binarycodec/definitions and writes a struct + Decode + emit methods
// that match the runtime contract in internal/tx/ledgerfields.Entry.
//
// Invocation: from the repo root,
//
//	go run ./internal/tx/ledgerfields/cmd/ledgerfieldsgen
//
// The package itself carries a //go:generate directive so `go generate
// ./...` picks it up.
package main

import (
	"fmt"
	"go/format"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"
	"github.com/LeJamon/goXRPLd/internal/tx/ledgerfields/spec"
)

func main() {
	outDir := "internal/tx/ledgerfields"
	if len(os.Args) > 1 {
		outDir = os.Args[1]
	}
	defs := definitions.Get()
	for _, entry := range spec.Specs {
		path, content, err := generate(defs, entry, outDir)
		if err != nil {
			log.Fatalf("generate %s: %v", entry.Name, err)
		}
		formatted, err := format.Source(content)
		if err != nil {
			// Write the un-formatted source so the user can debug.
			_ = os.WriteFile(path+".broken", content, 0o644)
			log.Fatalf("gofmt %s: %v (wrote %s.broken)", path, err, path)
		}
		if err := os.WriteFile(path, formatted, 0o644); err != nil {
			log.Fatalf("write %s: %v", path, err)
		}
		fmt.Printf("wrote %s (%d fields)\n", path, len(entry.Fields))
	}
}

// fieldRender carries the resolved per-field data the template needs.
type fieldRender struct {
	Name        string // canonical XRPL field name
	GoField     string // Go struct field name (mirrors XRPL name)
	BitConst    string // const name of the presence bit
	GoType      string // Go type of the slot
	XRPLType    string // XRPL type name (UInt32, Hash256, ...)
	TypeCode    int    // XRPL type code
	FieldCode   int    // XRPL field code
	Meta        spec.Meta
	Comparer    string // "String" | "Uint32" | "Int" | "Amount" — selects emitIfChanged*
	IsAmount    bool
	IsHash      bool   // hex-string default-value check
	XRPOnly     bool   // Balance on AccountRoot uses readAmount (XRP-only)
	ReadCall    string // expression that reads from streamReader
	DecodeKind  string // "uint32" | "int" | "string" | "amount" — drives the switch arm
}

type entryRender struct {
	Name           string                  // ledger-entry-type name
	StructName     string                  // Go struct name (= entry name)
	Receiver       string                  // single-letter receiver
	BitPrefix      string                  // prefix for presence-bit constants
	Fields         []fieldRender           // emit-ordered (creator order)
	DecodeArms     map[int][]decodeArm     // typeCode -> list of dispatch arms
	HasUnsupported bool                    // any Amount field that may be IOU
}

type decodeArm struct {
	TypeCode  int
	FieldCode int
	XRPLType  string
	GoField   string
	BitConst  string
	GoType    string
	XRPOnly   bool // for Amount fields
}

// generate returns (path, source).
func generate(defs *definitions.Definitions, entry spec.Entry, outDir string) (string, []byte, error) {
	er := entryRender{
		Name:       entry.Name,
		StructName: entry.Name,
		Receiver:   strings.ToLower(entry.Name[:1]),
		BitPrefix:  bitPrefixFor(entry.Name),
		DecodeArms: map[int][]decodeArm{},
	}

	for _, f := range entry.Fields {
		fi, err := defs.GetFieldInstanceByFieldName(f.Name)
		if err != nil {
			return "", nil, fmt.Errorf("field %s: %w", f.Name, err)
		}
		fr, err := makeFieldRender(f, fi, entry.Name, er.BitPrefix)
		if err != nil {
			return "", nil, fmt.Errorf("render %s: %w", f.Name, err)
		}
		er.Fields = append(er.Fields, fr)

		if f.Meta == spec.MetaNever {
			continue // decoded only to advance the parser; no slot
		}
		arm := decodeArm{
			TypeCode:  int(fi.FieldHeader.TypeCode),
			FieldCode: int(fi.Nth),
			XRPLType:  fi.Type,
			GoField:   fr.GoField,
			BitConst:  fr.BitConst,
			GoType:    fr.GoType,
			XRPOnly:   fr.XRPOnly,
		}
		if arm.XRPLType == "Amount" && !arm.XRPOnly {
			er.HasUnsupported = true
		}
		er.DecodeArms[arm.TypeCode] = append(er.DecodeArms[arm.TypeCode], arm)
	}

	// Sort decode arms per type by fieldCode for stable output.
	for tc, arms := range er.DecodeArms {
		sort.Slice(arms, func(i, j int) bool { return arms[i].FieldCode < arms[j].FieldCode })
		er.DecodeArms[tc] = arms
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, er); err != nil {
		return "", nil, err
	}
	path := filepath.Join(outDir, snake(entry.Name)+"_gen.go")
	return path, []byte(buf.String()), nil
}

func makeFieldRender(f spec.Field, fi *definitions.FieldInstance, entryName, bitPrefix string) (fieldRender, error) {
	fr := fieldRender{
		Name:      f.Name,
		GoField:   f.Name,
		BitConst:  bitPrefix + "Bit" + f.Name,
		XRPLType:  fi.Type,
		TypeCode:  int(fi.FieldHeader.TypeCode),
		FieldCode: int(fi.Nth),
		Meta:      f.Meta,
	}
	// Balance on AccountRoot is always XRP. Other Amount fields may be IOU.
	if entryName == "AccountRoot" && f.Name == "Balance" {
		fr.XRPOnly = true
	}
	switch fi.Type {
	case "UInt8":
		fr.GoType = "int"
		fr.Comparer = "Int"
		fr.DecodeKind = "int8"
	case "UInt16":
		fr.GoType = "int"
		fr.Comparer = "Int"
		fr.DecodeKind = "int16"
	case "UInt32":
		fr.GoType = "uint32"
		fr.Comparer = "Uint32"
		fr.DecodeKind = "uint32"
	case "UInt64":
		fr.GoType = "string"
		fr.Comparer = "String"
		fr.DecodeKind = "uint64hex"
		fr.IsHash = true
	case "Hash128":
		fr.GoType = "string"
		fr.Comparer = "String"
		fr.DecodeKind = "hash16"
		fr.IsHash = true
	case "Hash160":
		fr.GoType = "string"
		fr.Comparer = "String"
		fr.DecodeKind = "hash20"
		fr.IsHash = true
	case "Hash256":
		fr.GoType = "string"
		fr.Comparer = "String"
		fr.DecodeKind = "hash32"
		fr.IsHash = true
	case "AccountID":
		fr.GoType = "string"
		fr.Comparer = "String"
		fr.DecodeKind = "accountid"
	case "Blob":
		fr.GoType = "string"
		fr.Comparer = "String"
		fr.DecodeKind = "blob"
		fr.IsHash = true // hex-encoded blob; "0" treated as zero only via isZeroHexString
	case "Amount":
		fr.GoType = "any"
		fr.Comparer = "Amount"
		fr.IsAmount = true
		if fr.XRPOnly {
			fr.DecodeKind = "amountxrp"
		} else {
			fr.DecodeKind = "amountany"
		}
	case "Vector256":
		fr.GoType = "[]string"
		fr.Comparer = "StringSlice"
		fr.DecodeKind = "vector256"
	default:
		return fr, fmt.Errorf("unsupported XRPL type %q for field %s", fi.Type, f.Name)
	}
	return fr, nil
}

func bitPrefixFor(entryName string) string {
	// Keep the short forms used by the hand-written files so diffs stay
	// small when migrating those over: AccountRoot→ar, Offer→off,
	// DirectoryNode→dn, RippleState→rs.
	switch entryName {
	case "AccountRoot":
		return "ar"
	case "Offer":
		return "off"
	case "DirectoryNode":
		return "dn"
	case "RippleState":
		return "rs"
	}
	lower := strings.ToLower(entryName)
	if len(lower) > 3 {
		return lower[:3]
	}
	return lower
}

func snake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r + ('a' - 'A'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

const headerComment = `// Code generated by ledgerfieldsgen; DO NOT EDIT.
//
// Source: internal/tx/ledgerfields/spec/spec.go
// Regenerate: go generate ./internal/tx/ledgerfields/...
`

var tmpl = template.Must(template.New("entry").Funcs(template.FuncMap{
	"isZero": func(s string) bool { return s == "" },
}).Parse(headerComment + `
package ledgerfields

func init() {
	Register({{ printf "%q" .Name }}, func() Entry { return new({{ .StructName }}) })
}

// {{ .StructName }} is the typed representation of a {{ .Name }} ledger entry
// on the metadata hot path. present tracks which fields appear on the
// decoded blob so the emit methods only write entries that actually exist.
type {{ .StructName }} struct {
	present uint64
{{ range .Fields }}{{ if ne .Meta 3 }}	{{ .GoField }} {{ .GoType }}{{ if eq .XRPLType "AccountID" }} // AccountID (base58){{ else if eq .XRPLType "Amount" }} // Amount (XRP string | IOU map){{ else if eq .XRPLType "Hash256" }} // Hash256 (uppercase hex){{ else if eq .XRPLType "Hash160" }} // Hash160 (uppercase hex){{ else if eq .XRPLType "Hash128" }} // Hash128 (uppercase hex){{ else if eq .XRPLType "Blob" }} // Blob (uppercase hex){{ else if eq .XRPLType "UInt64" }} // UInt64 (uppercase hex){{ end }}
{{ end }}{{ end }}}

const (
{{ range $i, $f := .Fields }}{{ if eq $i 0 }}	{{ $f.BitConst }} uint64 = 1 << iota
{{ else }}	{{ $f.BitConst }}
{{ end }}{{ end }})

// Decode populates the struct from binary ledger-entry data via a streaming
// reader. Unknown / sMD_Never fields are skipped without allocation.
func ({{ .Receiver }} *{{ .StructName }}) Decode(data []byte) error {
	*{{ .Receiver }} = {{ .StructName }}{}
	sr := newStreamReader(data)
	for sr.hasMore() {
		typeCode, fieldCode, err := sr.readFieldHeader()
		if err != nil {
			return err
		}
		switch typeCode {
		case 1: // UInt16 — LedgerEntryType is the only AccountRoot/Offer/etc. UInt16 and is sMD_Never; discard.
			if _, err := sr.readUint16(); err != nil {
				return err
			}
{{- range $tc, $arms := .DecodeArms }}
{{- if ne $tc 1 }}
		case {{ $tc }}: // {{ (index $arms 0).XRPLType }}
{{- $first := index $arms 0 }}
{{- if eq $first.XRPLType "Amount" }}
{{- if $first.XRPOnly }}
			v, err := sr.readAmount()
{{- else }}
			v, err := sr.readAmountAny()
{{- end }}
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "UInt8" }}
			b, err := sr.readUint8()
			if err != nil {
				return err
			}
			v := int(b)
{{- else if eq $first.XRPLType "UInt32" }}
			v, err := sr.readUint32()
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "UInt64" }}
			v, err := sr.readUint64Hex()
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "Hash128" }}
			v, err := sr.readHash(16)
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "Hash160" }}
			v, err := sr.readHash(20)
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "Hash256" }}
			v, err := sr.readHash(32)
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "AccountID" }}
			v, err := sr.readAccountID()
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "Blob" }}
			v, err := sr.readBlobHex()
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "Vector256" }}
			v, err := sr.readVector256()
			if err != nil {
				return err
			}
{{- end }}
			switch fieldCode {
{{- range $arm := $arms }}
			case {{ $arm.FieldCode }}:
{{- if and (eq $arm.XRPLType "Amount") $arm.XRPOnly }}
				if s, ok := v.(string); ok {
					{{ $.Receiver }}.{{ $arm.GoField }} = s
					{{ $.Receiver }}.present |= {{ $arm.BitConst }}
				}
{{- else }}
				{{ $.Receiver }}.{{ $arm.GoField }} = v
				{{ $.Receiver }}.present |= {{ $arm.BitConst }}
{{- end }}
{{- end }}
			default:
				_ = v
			}
{{- end }}
{{- end }}
		default:
			if err := sr.skipField(typeCode); err != nil {
				return err
			}
		}
	}
	return nil
}

// emitAll writes every present default-meta field. skipDefault filters the
// "zero" value for CreatedNode.NewFields to match rippled, which omits
// defaulted fields from NewFields.
func ({{ .Receiver }} *{{ .StructName }}) emitAll(out map[string]any, skipDefault bool) {
{{- range .Fields }}{{ if eq .Meta 0 }}
	if {{ $.Receiver }}.present&{{ .BitConst }} != 0 {{ if eq .GoType "uint32" }}&& !(skipDefault && {{ $.Receiver }}.{{ .GoField }} == 0){{ else if eq .GoType "int" }}&& !(skipDefault && {{ $.Receiver }}.{{ .GoField }} == 0){{ else if .IsHash }}&& !(skipDefault && {{ if eq .XRPLType "Blob" }}{{ $.Receiver }}.{{ .GoField }} == ""{{ else }}isZeroHexString({{ $.Receiver }}.{{ .GoField }}){{ end }}){{ else if eq .GoType "string" }}&& !(skipDefault && {{ $.Receiver }}.{{ .GoField }} == ""){{ end }}{{ "" }} {
		out[{{ printf "%q" .Name }}] = {{ $.Receiver }}.{{ .GoField }}
	}
{{- end }}{{ end }}
{{- range .Fields }}{{ if eq .Meta 1 }}
	if {{ $.Receiver }}.present&{{ .BitConst }} != 0 {
		out[{{ printf "%q" .Name }}] = {{ $.Receiver }}.{{ .GoField }}
	}
{{- end }}{{ end }}
}

// EmitNewFields emits fields for a CreatedNode (sMD_Create | sMD_Always),
// filtering out default values to match rippled.
func ({{ .Receiver }} *{{ .StructName }}) EmitNewFields(out map[string]any) {
	{{ .Receiver }}.emitAll(out, true)
}

// EmitFinalFields emits fields for ModifiedNode.FinalFields (sMD_Always |
// sMD_ChangeNew), no default-value filter.
func ({{ .Receiver }} *{{ .StructName }}) EmitFinalFields(out map[string]any) {
	{{ .Receiver }}.emitAll(out, false)
}

// EmitPreviousFields emits the original values of fields that changed
// between prev and the receiver (sMD_ChangeOrig).
func ({{ .Receiver }} *{{ .StructName }}) EmitPreviousFields(prev Entry, out map[string]any) {
	p, ok := prev.(*{{ .StructName }})
	if !ok || p == nil {
		return
	}
{{- range .Fields }}{{ if or (eq .Meta 0) (eq .Meta 1) }}
	emitIfChanged{{ .Comparer }}(out, {{ printf "%q" .Name }}, p.{{ .GoField }}, {{ $.Receiver }}.{{ .GoField }}, p.present&{{ .BitConst }}, {{ $.Receiver }}.present&{{ .BitConst }})
{{- end }}{{ end }}
}

// EmitDeleteFinalFields emits fields for DeletedNode.FinalFields
// (sMD_Always | sMD_DeleteFinal), including PreviousTxn* which are
// otherwise hidden.
func ({{ .Receiver }} *{{ .StructName }}) EmitDeleteFinalFields(out map[string]any) {
	{{ .Receiver }}.emitAll(out, false)
{{- range .Fields }}{{ if eq .Meta 2 }}
	if {{ $.Receiver }}.present&{{ .BitConst }} != 0 {
		out[{{ printf "%q" .Name }}] = {{ $.Receiver }}.{{ .GoField }}
	}
{{- end }}{{ end }}
}

// EmitDeletePreviousFields mirrors EmitPreviousFields for DeletedNode.
func ({{ .Receiver }} *{{ .StructName }}) EmitDeletePreviousFields(prev Entry, out map[string]any) {
	{{ .Receiver }}.EmitPreviousFields(prev, out)
}

// PreviousTxn returns the threading values from the receiver. Empty id /
// zero seq mean the corresponding field is absent.
func ({{ .Receiver }} *{{ .StructName }}) PreviousTxn() (string, uint32) {
	var id string
	var seq uint32
{{- range .Fields }}{{ if eq .Name "PreviousTxnID" }}
	if {{ $.Receiver }}.present&{{ .BitConst }} != 0 {
		id = {{ $.Receiver }}.{{ .GoField }}
	}
{{- end }}{{ if eq .Name "PreviousTxnLgrSeq" }}
	if {{ $.Receiver }}.present&{{ .BitConst }} != 0 {
		seq = {{ $.Receiver }}.{{ .GoField }}
	}
{{- end }}{{ end }}
	return id, seq
}
`))
