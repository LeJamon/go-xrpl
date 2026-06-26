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

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields/spec"
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
			_ = os.WriteFile(path+".broken", content, 0o644) //nolint:gosec // G306: generated dev source artifact, 0644 intentional
			log.Fatalf("gofmt %s: %v (wrote %s.broken)", path, err, path)
		}
		if err := os.WriteFile(path, formatted, 0o644); err != nil { //nolint:gosec // G306: generated source file, 0644 intentional
			log.Fatalf("write %s: %v", path, err)
		}
		fmt.Printf("wrote %s (%d fields)\n", path, len(entry.Fields))
	}
}

// fieldRender carries the resolved per-field data the template needs.
type fieldRender struct {
	Name            string // canonical XRPL field name
	GoField         string // Go struct field name (mirrors XRPL name)
	BitConst        string // const name of the presence bit
	GoType          string // Go type of the slot
	XRPLType        string // XRPL type name (UInt32, Hash256, ...)
	TypeCode        int    // XRPL type code
	FieldCode       int    // XRPL field code
	Meta            spec.Meta
	Comparer        string // "String" | "Uint32" | "Int" | "Amount" — selects emitIfChanged*
	IsAmount        bool
	IsHash          bool // hex-string default-value check
	XRPOnly         bool // Balance on AccountRoot uses readAmount (XRP-only)
	IsBaseTenUInt64 bool // UInt64 field rippled emits as decimal (sMD_BaseTen)
	ReadCall        string
	DecodeKind      string
}

type entryRender struct {
	Name           string              // ledger-entry-type name
	StructName     string              // Go struct name (= entry name)
	Receiver       string              // single-letter receiver
	BitPrefix      string              // prefix for presence-bit constants
	Fields         []fieldRender       // emit-ordered (creator order)
	DecodeArms     map[int][]decodeArm // typeCode -> list of dispatch arms
	HasUnsupported bool                // any Amount field that may be IOU
}

type decodeArm struct {
	TypeCode        int
	FieldCode       int
	XRPLType        string
	GoField         string
	BitConst        string
	GoType          string
	XRPOnly         bool // for Amount fields
	IsBaseTenUInt64 bool // UInt64 sMD_BaseTen field — decode as decimal not hex
	Meta            spec.Meta
}

func generate(defs *definitions.Definitions, entry spec.Entry, outDir string) (string, []byte, error) {
	er := entryRender{
		Name:       entry.Name,
		StructName: entry.Name,
		Receiver:   strings.ToLower(entry.Name[:1]),
		BitPrefix:  bitPrefixFor(entry.Name),
		DecodeArms: map[int][]decodeArm{},
	}

	// Every ledger entry carries LedgerEntryType (UInt16 fieldCode 1) as its
	// first serialized field; it's sMD_Never (never in metadata) but the
	// streaming decoder must still consume those two bytes. Inject a
	// synthetic discard-only arm so it lives in the same typeCode-1 switch
	// as any spec'd UInt16 fields (e.g. AMM.TradingFee).
	er.DecodeArms[1] = []decodeArm{{
		TypeCode:  1,
		FieldCode: 1,
		XRPLType:  "UInt16",
		GoField:   "LedgerEntryType",
		BitConst:  "",
		GoType:    "int",
		Meta:      spec.MetaNever,
	}}

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

		// Include even MetaNever fields in DecodeArms: the parser still has
		// to consume their bytes, and the generator emits a discard-only arm
		// for them so the fail-fast outer default doesn't trip on a field
		// the spec already declared.
		arm := decodeArm{
			TypeCode:        int(fi.FieldHeader.TypeCode),
			FieldCode:       int(fi.Nth),
			XRPLType:        fi.Type,
			GoField:         fr.GoField,
			BitConst:        fr.BitConst,
			GoType:          fr.GoType,
			XRPOnly:         fr.XRPOnly,
			IsBaseTenUInt64: fr.IsBaseTenUInt64,
			Meta:            f.Meta,
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
		fr.IsBaseTenUInt64 = definitions.IsBaseTenUInt64FieldName(f.Name)
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
	case "Hash192":
		fr.GoType = "string"
		fr.Comparer = "String"
		fr.DecodeKind = "hash24"
		fr.IsHash = true
	case "STObject":
		fr.GoType = "map[string]any"
		fr.Comparer = "Deep"
		fr.DecodeKind = "stobject"
	case "STArray":
		fr.GoType = "[]any"
		fr.Comparer = "Deep"
		fr.DecodeKind = "starray"
	case "Issue":
		fr.GoType = "any"
		fr.Comparer = "Deep"
		fr.DecodeKind = "issue"
	case "XChainBridge":
		fr.GoType = "any"
		fr.Comparer = "Deep"
		fr.DecodeKind = "xchainbridge"
	case "Number":
		fr.GoType = "any"
		fr.Comparer = "Deep"
		fr.DecodeKind = "number"
	case "Currency":
		// Used by sfAsset / similar — same shape as Hash160.
		fr.GoType = "string"
		fr.Comparer = "String"
		fr.DecodeKind = "hash20"
		fr.IsHash = true
	default:
		return fr, fmt.Errorf("unsupported XRPL type %q for field %s", fi.Type, f.Name)
	}
	return fr, nil
}

func bitPrefixFor(entryName string) string {
	// Use the full lowercased entry name as prefix to guarantee uniqueness
	// across the ~28 entry types (MPToken vs MPTokenIssuance, NFTokenOffer
	// vs NFTokenPage, XChainOwnedClaimID vs XChainOwnedCreateAccountClaimID
	// would collide on any abbreviated scheme).
	return strings.ToLower(entryName)
}

// snake converts a CamelCase identifier to snake_case, treating runs of
// consecutive uppercase letters as one acronym. So "AccountRoot" →
// "account_root", "NFTokenOffer" → "nf_token_offer", "DID" → "did",
// "XChainOwnedClaimID" → "x_chain_owned_claim_id".
func snake(s string) string {
	bytes := []byte(s)
	var b strings.Builder
	for i, c := range bytes {
		if i > 0 && isUpperByte(c) {
			prev := bytes[i-1]
			var next byte
			if i+1 < len(bytes) {
				next = bytes[i+1]
			}
			// Split on lower→upper or on upper→upper-followed-by-lower
			// (acronym boundary into a new word).
			if isLowerByte(prev) || (isUpperByte(prev) && isLowerByte(next)) {
				b.WriteByte('_')
			}
		}
		if isUpperByte(c) {
			b.WriteByte(c + ('a' - 'A'))
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func isUpperByte(b byte) bool { return b >= 'A' && b <= 'Z' }
func isLowerByte(b byte) bool { return b >= 'a' && b <= 'z' }

const headerComment = `// Code generated by ledgerfieldsgen; DO NOT EDIT.
//
// Source: internal/tx/ledgerfields/spec/spec.go
// Regenerate: go generate ./internal/tx/ledgerfields/...
`

var tmpl = template.Must(template.New("entry").Funcs(template.FuncMap{
	"isZero": func(s string) bool { return s == "" },
}).Parse(headerComment + `
package ledgerfields

import (
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/protocol"
)

func init() {
	Register({{ printf "%q" .Name }}, func() Entry { return new({{ .StructName }}) })
}

// {{ .StructName }} is the typed representation of a {{ .Name }} ledger entry.
// The present bitset tracks which fields appear on the decoded blob so the
// emit methods only write entries that actually exist. The struct carries
// every on-wire field — including those excluded from metadata
// (sMD_Never) — so Decode → Encode is byte-identical.
type {{ .StructName }} struct {
	present uint64
{{ range .Fields }}	{{ .GoField }} {{ .GoType }}{{ if eq .XRPLType "AccountID" }} // AccountID (base58){{ else if eq .XRPLType "Amount" }} // Amount (XRP string | IOU map){{ else if eq .XRPLType "Hash256" }} // Hash256 (uppercase hex){{ else if eq .XRPLType "Hash160" }} // Hash160 (uppercase hex){{ else if eq .XRPLType "Hash128" }} // Hash128 (uppercase hex){{ else if eq .XRPLType "Blob" }} // Blob (uppercase hex){{ else if eq .XRPLType "UInt64" }}{{ if .IsBaseTenUInt64 }} // UInt64 (decimal string, sMD_BaseTen){{ else }} // UInt64 (lowercase hex, no leading zeros){{ end }}{{ end }}
{{ end }}}

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
{{- range $tc, $arms := .DecodeArms }}
		case {{ $tc }}: // {{ (index $arms 0).XRPLType }}
{{- $first := index $arms 0 }}
{{- if eq $first.XRPLType "UInt64" }}
			switch fieldCode {
{{- range $arm := $arms }}
			case {{ $arm.FieldCode }}:
{{- if and (eq $arm.Meta 3) (isZero $arm.BitConst) }}
				if _, err := sr.readUint64Raw(); err != nil {
					return err
				}
				// {{ $arm.GoField }} is synthetic LedgerEntryType; discard
{{- else if $arm.IsBaseTenUInt64 }}
				val, err := sr.readUint64Decimal()
				if err != nil {
					return err
				}
				{{ $.Receiver }}.{{ $arm.GoField }} = val
				{{ $.Receiver }}.present |= {{ $arm.BitConst }}
{{- else }}
				val, err := sr.readUint64Hex()
				if err != nil {
					return err
				}
				{{ $.Receiver }}.{{ $arm.GoField }} = val
				{{ $.Receiver }}.present |= {{ $arm.BitConst }}
{{- end }}
{{- end }}
			default:
				return newErrUnknownField({{ printf "%q" $.Name }}, typeCode, fieldCode)
			}
{{- else }}
{{- if eq $first.XRPLType "Amount" }}
{{- if $first.XRPOnly }}
			val, err := sr.readAmount()
{{- else }}
			val, err := sr.readAmountAny()
{{- end }}
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "UInt8" }}
			byteVal, err := sr.readUint8()
			if err != nil {
				return err
			}
			val := int(byteVal)
{{- else if eq $first.XRPLType "UInt16" }}
			u16Val, err := sr.readUint16()
			if err != nil {
				return err
			}
			val := int(u16Val)
{{- else if eq $first.XRPLType "UInt32" }}
			val, err := sr.readUint32()
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "Hash128" }}
			val, err := sr.readHash(16)
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "Hash160" }}
			val, err := sr.readHash(20)
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "Hash192" }}
			val, err := sr.readHash(24)
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "Hash256" }}
			val, err := sr.readHash(32)
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "AccountID" }}
			val, err := sr.readAccountID()
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "Blob" }}
			val, err := sr.readBlobHex()
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "Vector256" }}
			val, err := sr.readVector256()
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "STObject" }}
			val, err := sr.readSTObject()
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "STArray" }}
			val, err := sr.readSTArray()
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "Issue" }}
			val, err := sr.readIssue()
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "XChainBridge" }}
			val, err := sr.readXChainBridge()
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "Number" }}
			val, err := sr.readNumber()
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "Currency" }}
			val, err := sr.readHash(20)
			if err != nil {
				return err
			}
{{- end }}
			switch fieldCode {
{{- range $arm := $arms }}
			case {{ $arm.FieldCode }}:
{{- if and (eq $arm.Meta 3) (isZero $arm.BitConst) }}
				_ = val // synthetic LedgerEntryType; discard
{{- else if and (eq $arm.XRPLType "Amount") $arm.XRPOnly }}
				if s, ok := val.(string); ok {
					{{ $.Receiver }}.{{ $arm.GoField }} = s
					{{ $.Receiver }}.present |= {{ $arm.BitConst }}
				}
{{- else }}
				{{ $.Receiver }}.{{ $arm.GoField }} = val
				{{ $.Receiver }}.present |= {{ $arm.BitConst }}
{{- end }}
{{- end }}
			default:
				return newErrUnknownField({{ printf "%q" $.Name }}, typeCode, fieldCode)
			}
{{- end }}
{{- end }}
		default:
			return newErrUnknownField({{ printf "%q" $.Name }}, typeCode, fieldCode)
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
// between prev and the receiver (sMD_ChangeOrig — MetaDefault only).
func ({{ .Receiver }} *{{ .StructName }}) EmitPreviousFields(prev Entry, out map[string]any) {
	p, ok := prev.(*{{ .StructName }})
	if !ok || p == nil {
		return
	}
{{- range .Fields }}{{ if eq .Meta 0 }}
	emitIfChanged{{ .Comparer }}(out, {{ printf "%q" .Name }}, p.{{ .GoField }}, {{ $.Receiver }}.{{ .GoField }}, p.present&{{ .BitConst }}, {{ $.Receiver }}.present&{{ .BitConst }})
{{- end }}{{ end }}
}

// EmitChangeOrigFields writes the names of every present field carrying
// sMD_ChangeOrig (MetaDefault). The empty-PreviousFields heuristic uses
// this to scope its orig-vs-cur presence comparison so MetaAlways fields
// (which appear in FinalFields but lack sMD_ChangeOrig at the rippled
// level) cannot trip a spurious STI_NOTPRESENT emission.
func ({{ .Receiver }} *{{ .StructName }}) EmitChangeOrigFields(out map[string]any) {
{{- range .Fields }}{{ if eq .Meta 0 }}
	if {{ $.Receiver }}.present&{{ .BitConst }} != 0 {
		out[{{ printf "%q" .Name }}] = {{ $.Receiver }}.{{ .GoField }}
	}
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

// ToMap returns the canonical JSON-map representation of the receiver,
// suitable for binarycodec.EncodeBytes. Includes every present field —
// metadata-excluded fields (sMD_Never) too — plus the LedgerEntryType
// header that every SLE blob carries.
func ({{ .Receiver }} *{{ .StructName }}) ToMap() map[string]any {
	out := map[string]any{
		"LedgerEntryType": {{ printf "%q" .Name }},
	}
{{- range .Fields }}
	if {{ $.Receiver }}.present&{{ .BitConst }} != 0 {
		out[{{ printf "%q" .Name }}] = {{ $.Receiver }}.{{ .GoField }}
	}
{{- end }}
	return out
}

// Encode serializes the receiver to canonical XRPL binary. Round-trip
// invariant: Decode(data); Encode() == data for any byte sequence that
// Decode accepts.
func ({{ .Receiver }} *{{ .StructName }}) Encode() ([]byte, error) {
	return binarycodec.EncodeBytes({{ .Receiver }}.ToMap())
}

// Hash returns the SHAMap account-state leaf hash for this entry,
// sha512Half(HashPrefixLeafNode || encoded || index). index is the
// 32-byte keylet under which the entry is stored.
func ({{ .Receiver }} *{{ .StructName }}) Hash(index [32]byte) ([32]byte, error) {
	data, err := {{ .Receiver }}.Encode()
	if err != nil {
		return [32]byte{}, err
	}
	prefix := protocol.HashPrefixLeafNode
	return common.Sha512Half(prefix[:], data, index[:]), nil
}
`))
