// Command ledgerfieldsgen emits one typed-decoder Go file per ledger entry
// type listed in ledger/entry/schema.Specs. For each spec entry it
// looks up every field's XRPL type and ordinal in
// codec/binarycodec/definitions and writes a struct + Decode + emit methods
// that match the runtime contract in ledger/entry.Entry.
//
// Invocation: from the repo root,
//
//	go run ./ledger/entry/cmd/entrygen
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
	"github.com/LeJamon/go-xrpl/ledger/entry/schema"
	"github.com/LeJamon/go-xrpl/protocol"
)

func main() {
	outDir := "ledger/entry"
	if len(os.Args) > 1 {
		outDir = os.Args[1]
	}
	defs := definitions.Get()
	if path, content, err := generateInnerValues(defs, outDir); err != nil {
		log.Fatalf("generate inner values: %v", err)
	} else {
		formatted, err := format.Source(content)
		if err != nil {
			_ = os.WriteFile(path+".broken", content, 0o644) //nolint:gosec // generated source artifact, world-readable by intent
			log.Fatalf("gofmt %s: %v (wrote %s.broken)", path, err, path)
		}
		if err := os.WriteFile(path, formatted, 0o644); err != nil { //nolint:gosec // G306: generated source artifact, world-readable by intent
			log.Fatalf("write %s: %v", path, err)
		}
		fmt.Printf("wrote %s\n", path)
	}
	for _, entry := range schema.Specs {
		path, content, err := generate(defs, entry, outDir)
		if err != nil {
			log.Fatalf("generate %s: %v", entry.Name, err)
		}
		formatted, err := format.Source(content)
		if err != nil {
			// Write the un-formatted source so the user can debug.
			_ = os.WriteFile(path+".broken", content, 0o644) //nolint:gosec // G306: generated source artifact, world-readable by intent
			log.Fatalf("gofmt %s: %v (wrote %s.broken)", path, err, path)
		}
		if err := os.WriteFile(path, formatted, 0o644); err != nil { //nolint:gosec // G306: generated source artifact, world-readable by intent
			log.Fatalf("write %s: %v", path, err)
		}
		fmt.Printf("wrote %s (%d fields)\n", path, len(entry.AllFields()))
	}
}

// fieldRender carries the resolved per-field data the template needs.
type fieldRender struct {
	Name             string // canonical XRPL field name
	GoField          string // Go struct field name (mirrors XRPL name)
	BitConst         string // const name of the presence bit
	GoType           string // Go type of the slot
	XRPLType         string // XRPL type name (UInt32, Hash256, ...)
	TypeCode         int    // XRPL type code
	FieldCode        int    // XRPL field code
	Meta             schema.Meta
	Style            schema.Style
	DeferredRequired bool
	Comparer         string // "String" | "Uint32" | "Int" | "Amount" — selects emitIfChanged*
	IsAmount         bool
	IsHash           bool // hex-string default-value check
	XRPOnly          bool // Balance on AccountRoot uses readAmount (XRP-only)
	IsBaseTenUInt64  bool // UInt64 field rippled emits as decimal (sMD_BaseTen)
	ReadCall         string
	DecodeKind       string
	// DefaultExpr is a Go boolean expression (using the entry's receiver) that
	// is true when the decoded field holds its rippled type-default. It mirrors
	// STBase::isDefault() per type and gates CreatedNode.NewFields emission
	// (rippled ApplyStateTable.cpp: `!obj.isDefault()`). Empty means the type
	// has no default to filter.
	DefaultExpr       string
	SetterDefaultExpr string
	SetterGoType      string
	SetterAssignment  string
	Clearable         bool
	Compound          *compoundRender
}

type entryRender struct {
	Name                   string              // ledger-entry-type name
	StructName             string              // Go struct name (= entry name)
	Receiver               string              // single-letter receiver
	BitPrefix              string              // prefix for presence-bit constants
	Fields                 []fieldRender       // emit-ordered (creator order)
	DecodeArms             map[int][]decodeArm // typeCode -> list of dispatch arms
	DecodeOnlyArms         []decodeArm
	HasUnsupported         bool // any Amount field that may be IOU
	AllowBadCurrencyDecode bool
	EntryTypeCode          int
}

type decodeArm struct {
	TypeCode        int
	FieldCode       int
	FieldName       string
	XRPLType        string
	GoField         string
	BitConst        string
	GoType          string
	XRPOnly         bool // for Amount fields
	IsBaseTenUInt64 bool // UInt64 sMD_BaseTen field — decode as decimal not hex
	Meta            schema.Meta
}

type innerFieldRender struct {
	Name        string
	GoField     string
	XRPLType    string
	GoType      string
	Style       uint8
	BitConst    string
	BaseTen     bool
	NestedType  string
	SetterError bool
}

type innerObjectRender struct {
	Name   string
	Type   string
	Fields []innerFieldRender
}

type compoundRender struct {
	Template string
	Type     string
	Array    bool
}

func generate(defs *definitions.Definitions, entry schema.Entry, outDir string) (string, []byte, error) {
	entryTypeInfo, ok := protocol.LedgerEntryTypeByName(entry.Name)
	if !ok || entryTypeInfo.Deprecated {
		return "", nil, fmt.Errorf("ledger entry type %s is not registered", entry.Name)
	}
	er := entryRender{
		Name:                   entry.Name,
		StructName:             entry.Name,
		Receiver:               strings.ToLower(entry.Name[:1]),
		BitPrefix:              bitPrefixFor(entry.Name),
		DecodeArms:             map[int][]decodeArm{},
		AllowBadCurrencyDecode: entry.AllowBadCurrencyDecode,
		EntryTypeCode:          int(entryTypeInfo.Type),
	}
	fields := entry.AllFields()
	fieldsByName := make(map[string]schema.Field, len(fields))
	fieldInstances := make(map[string]*definitions.FieldInstance, len(fields))
	for _, f := range fields {
		if _, exists := fieldsByName[f.Name]; exists {
			return "", nil, fmt.Errorf("field %s: duplicate specification", f.Name)
		}
		fi, err := defs.FieldInstanceByName(f.Name)
		if err != nil {
			return "", nil, fmt.Errorf("field %s: %w", f.Name, err)
		}
		fieldsByName[f.Name] = f
		fieldInstances[f.Name] = fi
	}
	for _, f := range fields {
		if f.DecodeAlias == "" {
			continue
		}
		if !f.DecodeOnly {
			return "", nil, fmt.Errorf("field %s: decode alias requires DecodeOnly", f.Name)
		}
		target, ok := fieldsByName[f.DecodeAlias]
		if !ok {
			return "", nil, fmt.Errorf("field %s: decode alias target %s is not specified", f.Name, f.DecodeAlias)
		}
		if target.DecodeOnly {
			return "", nil, fmt.Errorf("field %s: decode alias target %s is decode-only", f.Name, f.DecodeAlias)
		}
		if fieldInstances[f.Name].Type != fieldInstances[f.DecodeAlias].Type {
			return "", nil, fmt.Errorf("field %s: decode alias target %s has XRPL type %s, want %s", f.Name, f.DecodeAlias, fieldInstances[f.DecodeAlias].Type, fieldInstances[f.Name].Type)
		}
	}

	// Every ledger entry carries LedgerEntryType (UInt16 fieldCode 1) as its
	// first serialized field; it's sMD_Never (never in metadata) but the
	// streaming decoder must still consume those two bytes. Inject a
	// synthetic discard-only arm so it lives in the same typeCode-1 switch
	// as any spec'd UInt16 fields (e.g. AMM.TradingFee).
	er.DecodeArms[1] = []decodeArm{{
		TypeCode:  1,
		FieldCode: 1,
		FieldName: "LedgerEntryType",
		XRPLType:  "UInt16",
		GoField:   "LedgerEntryType",
		BitConst:  "",
		GoType:    "int",
		Meta:      schema.MetaNever,
	}}

	for _, f := range fields {
		if f.Style < schema.StyleRequired || f.Style > schema.StyleDefault {
			return "", nil, fmt.Errorf("field %s: serialization style is not set", f.Name)
		}
		if f.DeferredRequired && f.Style != schema.StyleRequired {
			return "", nil, fmt.Errorf("field %s: only required fields may be deferred", f.Name)
		}
		fi := fieldInstances[f.Name]

		// DecodeOnly fields are tolerated on the wire but never emitted under
		// their legacy name. By default their values are discarded; an explicit
		// alias stores the value in the canonical target field.
		if f.DecodeOnly {
			arm := decodeArm{
				TypeCode:  int(fi.FieldHeader.TypeCode),
				FieldCode: int(fi.Nth),
				FieldName: f.Name,
				XRPLType:  fi.Type,
				GoField:   f.Name,
				BitConst:  "",
				GoType:    "",
				Meta:      schema.MetaNever,
			}
			if f.DecodeAlias != "" {
				target := fieldsByName[f.DecodeAlias]
				targetRender, err := makeFieldRender(target, fieldInstances[f.DecodeAlias], entry.Name, er.BitPrefix, er.Receiver)
				if err != nil {
					return "", nil, fmt.Errorf("render decode alias target %s: %w", f.DecodeAlias, err)
				}
				arm.GoField = targetRender.GoField
				arm.BitConst = targetRender.BitConst
				arm.GoType = targetRender.GoType
				arm.XRPOnly = targetRender.XRPOnly
				arm.IsBaseTenUInt64 = targetRender.IsBaseTenUInt64
				arm.Meta = target.Meta
				if arm.XRPLType == "Amount" && !arm.XRPOnly {
					er.HasUnsupported = true
				}
			}
			er.DecodeOnlyArms = append(er.DecodeOnlyArms, arm)
			er.DecodeArms[int(fi.FieldHeader.TypeCode)] = append(
				er.DecodeArms[int(fi.FieldHeader.TypeCode)],
				arm)
			continue
		}

		fr, err := makeFieldRender(f, fi, entry.Name, er.BitPrefix, er.Receiver)
		if err != nil {
			return "", nil, fmt.Errorf("render %s: %w", f.Name, err)
		}
		if err := attachCompound(&fr); err != nil {
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
			FieldName:       f.Name,
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

func makeFieldRender(f schema.Field, fi *definitions.FieldInstance, entryName, bitPrefix, receiver string) (fieldRender, error) {
	fr := fieldRender{
		Name:             f.Name,
		GoField:          f.Name,
		BitConst:         bitPrefix + "Bit" + f.Name,
		XRPLType:         fi.Type,
		TypeCode:         int(fi.FieldHeader.TypeCode),
		FieldCode:        int(fi.Nth),
		Meta:             f.Meta,
		Style:            f.Style,
		DeferredRequired: f.DeferredRequired,
		Clearable:        f.Style != schema.StyleRequired,
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
	case "Int32":
		fr.GoType = "int"
		fr.Comparer = "Int"
		fr.DecodeKind = "int32"
	case "Currency":
		// Used by sfAsset / similar — same shape as Hash160.
		fr.GoType = "string"
		fr.Comparer = "String"
		fr.DecodeKind = "hash20"
		fr.IsHash = true
	default:
		return fr, fmt.Errorf("unsupported XRPL type %q for field %s", fi.Type, f.Name)
	}

	fr.DefaultExpr = defaultExprFor(fr, receiver+"."+fr.GoField)
	fr.SetterDefaultExpr = setterDefaultExprFor(fr)
	fr.SetterGoType = fr.GoType
	fr.SetterAssignment = "value"
	switch fr.XRPLType {
	case "UInt8":
		fr.SetterGoType = "uint8"
		fr.SetterAssignment = "int(value)"
	case "UInt16":
		fr.SetterGoType = "uint16"
		fr.SetterAssignment = "int(value)"
	case "Int32":
		fr.SetterGoType = "int32"
		fr.SetterAssignment = "int(value)"
	}
	return fr, nil
}

func attachCompound(fr *fieldRender) error {
	switch fr.XRPLType {
	case "STObject":
		if _, ok := schema.InnerObjectTemplateByName(fr.Name); !ok {
			return fmt.Errorf("STObject field %s has no inner-object template", fr.Name)
		}
		fr.Compound = &compoundRender{Template: fr.Name, Type: innerTypeName(fr.Name)}
	case "STArray":
		templateName, ok := schema.ArrayElementTemplate(fr.Name)
		if !ok {
			return fmt.Errorf("STArray field %s has no inner-object template", fr.Name)
		}
		if _, ok := schema.InnerObjectTemplateByName(templateName); !ok {
			return fmt.Errorf("STArray field %s references missing template %s", fr.Name, templateName)
		}
		fr.Compound = &compoundRender{Template: templateName, Type: innerTypeName(templateName), Array: true}
	case "Vector256", "Issue", "XChainBridge", "Number":
		fr.Compound = &compoundRender{Type: fr.XRPLType}
	default:
		return nil
	}
	return nil
}

func innerTypeName(template string) string { return template + "Value" }

func generateInnerValues(defs *definitions.Definitions, outDir string) (string, []byte, error) {
	renders := make([]innerObjectRender, 0)
	for _, name := range schema.InnerObjectTemplateNames() {
		spec, ok := schema.InnerObjectTemplateByName(name)
		if !ok {
			return "", nil, fmt.Errorf("inner object template %s disappeared", name)
		}
		fields := make([]innerFieldRender, 0, len(spec.Fields))
		for fieldName, fieldSpec := range spec.Fields {
			fi, err := defs.FieldInstanceByName(fieldName)
			if err != nil {
				return "", nil, fmt.Errorf("inner field %s.%s: %w", name, fieldName, err)
			}
			fr, err := makeInnerFieldRender(fieldName, fieldSpec, fi)
			if err != nil {
				return "", nil, fmt.Errorf("inner field %s.%s: %w", name, fieldName, err)
			}
			fr.BitConst = "inner" + innerTypeName(name) + "Bit" + fr.Name
			fields = append(fields, fr)
		}
		sort.Slice(fields, func(i, j int) bool {
			left, _ := defs.FieldInstanceByName(fields[i].Name)
			right, _ := defs.FieldInstanceByName(fields[j].Name)
			return left.Nth < right.Nth
		})
		renders = append(renders, innerObjectRender{Name: name, Type: innerTypeName(name), Fields: fields})
	}
	var buf strings.Builder
	if err := innerValuesTemplate.Execute(&buf, renders); err != nil {
		return "", nil, err
	}
	return filepath.Join(outDir, "inner_values_gen.go"), []byte(buf.String()), nil
}

func makeInnerFieldRender(name string, spec schema.InnerFieldTemplate, fi *definitions.FieldInstance) (innerFieldRender, error) {
	xrplType := fi.Type
	if spec.Kind == schema.InnerPermissionValue {
		xrplType = "PermissionValue"
	}
	fr := innerFieldRender{
		Name:     name,
		GoField:  name,
		XRPLType: xrplType,
		Style:    uint8(spec.Style - schema.InnerRequired),
		BitConst: "",
		BaseTen:  definitions.IsBaseTenUInt64FieldName(name),
	}
	switch xrplType {
	case "AccountID", "Amount", "Issue", "XChainBridge", "Number", "STArray", "STObject":
		fr.SetterError = true
	}
	switch xrplType {
	case "UInt8":
		fr.GoType = "uint8"
	case "UInt16":
		fr.GoType = "uint16"
	case "UInt32", "PermissionValue":
		fr.GoType = "uint32"
	case "UInt64":
		fr.GoType = "uint64"
	case "Hash128":
		fr.GoType = "[16]byte"
	case "Hash160":
		fr.GoType = "[20]byte"
	case "Hash192":
		fr.GoType = "[24]byte"
	case "Hash256":
		fr.GoType = "[32]byte"
	case "AccountID":
		fr.GoType = "[20]byte"
	case "Blob":
		fr.GoType = "[]byte"
	case "Amount":
		fr.GoType = "AmountValue"
	case "Vector256":
		fr.GoType = "Vector256Value"
	case "Issue":
		fr.GoType = "IssueValue"
	case "XChainBridge":
		fr.GoType = "XChainBridgeValue"
	case "Number":
		fr.GoType = "NumberValue"
	case "Currency":
		fr.GoType = "string"
	case "STArray":
		templateName, ok := schema.ArrayElementTemplate(name)
		if !ok {
			return fr, fmt.Errorf("STArray field has no element template")
		}
		fr.NestedType = innerTypeName(templateName)
		fr.GoType = "[]" + fr.NestedType
	case "STObject":
		if _, ok := schema.InnerObjectTemplateByName(name); !ok {
			return fr, fmt.Errorf("STObject field has no template")
		}
		fr.NestedType = innerTypeName(name)
		fr.GoType = fr.NestedType
	default:
		return fr, fmt.Errorf("unsupported XRPL type %q", xrplType)
	}
	return fr, nil
}

// defaultExprFor builds the per-field "is type-default" predicate used to gate
// CreatedNode.NewFields. It mirrors rippled's STBase::isDefault() overrides
// (STAmount: zero XRP; STIssue: xrpIssue; STNumber: zero; STBitString/UInt*:
// zero; STBlob/STAccount: empty; STVector256/STArray/STObject/STXChainBridge:
// empty/default) so a field present in the canonical SLE blob but equal to its
// type default is dropped from NewFields, exactly as rippled does.
func defaultExprFor(fr fieldRender, value string) string {
	g := value
	switch {
	case fr.IsAmount:
		// STAmount::isDefault(): zero XRP only (IOU/MPT decode to a map and are
		// never default).
		return "amountIsDefault(" + g + ")"
	case fr.XRPLType == "Issue":
		return "issueIsDefault(" + g + ")"
	case fr.XRPLType == "Number":
		return "numberIsDefault(" + g + ")"
	case fr.XRPLType == "XChainBridge":
		return "xchainBridgeIsDefault(" + g + ")"
	case fr.GoType == "[]string" || fr.GoType == "[]any" || fr.GoType == "map[string]any":
		// STVector256 / STArray / STObject: default == empty.
		return "len(" + g + ") == 0"
	case fr.GoType == "uint32" || fr.GoType == "int":
		return g + " == 0"
	case fr.XRPLType == "Blob":
		return g + ` == ""`
	case fr.IsHash:
		// Hash128/160/192/256, UInt64 (hex or sMD_BaseTen decimal), Currency:
		// default == all-zero, which canonicalizes to "0" or an all-'0' string.
		return "isZeroHexString(" + g + ")"
	case fr.GoType == "string":
		// AccountID: a default (zero) account serializes as a 0-length VL and
		// decodes to "".
		return g + ` == ""`
	}
	return ""
}

func setterDefaultExprFor(fr fieldRender) string {
	expr := defaultExprFor(fr, "value")
	switch {
	case fr.GoType == "any":
		return "value == nil || (" + expr + ")"
	case fr.IsHash && fr.XRPLType != "Blob":
		return `value == "" || (` + expr + ")"
	default:
		return expr
	}
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

const headerComment = `// Code generated by entrygen; DO NOT EDIT.
//
// Source: ledger/entry/schema/spec.go
// Regenerate: go generate ./ledger/entry/...
`

var tmpl = template.Must(template.New("entry").Funcs(template.FuncMap{
	"isZero": func(s string) bool { return s == "" },
	"lowerFirst": func(s string) string {
		if s == "" {
			return s
		}
		return strings.ToLower(s[:1]) + s[1:]
	},
	"zeroValue": func(goType string) string {
		switch goType {
		case "any", "map[string]any", "[]any", "[]string":
			return "nil"
		case "string":
			return `""`
		default:
			return "0"
		}
	},
}).Parse(headerComment + `
package entry

import (
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/protocol"
)

// {{ .StructName }} is the typed representation of a {{ .Name }} ledger entry.
// The present bitset tracks which fields appear on the decoded blob so the
// emit methods only write entries that actually exist. The struct carries
// every canonical field declared in the spec — including those excluded from
// metadata (sMD_Never) — so decoding and re-encoding does not drop them.
type {{ .StructName }} struct {
	present uint64
	decoded bool
	dirty   bool
{{- if .AllowBadCurrencyDecode }}
	decodedBinary []byte
{{- end }}
{{ range .Fields }}	{{ .GoField }} {{ .GoType }}{{ if eq .XRPLType "AccountID" }} // AccountID (base58){{ else if eq .XRPLType "Amount" }} // Amount (XRP string | IOU map){{ else if eq .XRPLType "Hash256" }} // Hash256 (uppercase hex){{ else if eq .XRPLType "Hash160" }} // Hash160 (uppercase hex){{ else if eq .XRPLType "Hash128" }} // Hash128 (uppercase hex){{ else if eq .XRPLType "Blob" }} // Blob (uppercase hex){{ else if eq .XRPLType "UInt64" }}{{ if .IsBaseTenUInt64 }} // UInt64 (decimal string, sMD_BaseTen){{ else }} // UInt64 (lowercase hex, no leading zeros){{ end }}{{ end }}
{{ end }}}

// Type returns the concrete ledger-entry type.
func (*{{ .StructName }}) Type() Type {
	return Type({{ .EntryTypeCode }})
}

const (
{{ range $i, $f := .Fields }}{{ if eq $i 0 }}	{{ $f.BitConst }} uint64 = 1 << iota
{{ else }}	{{ $f.BitConst }}
{{ end }}{{ end }})

{{ range .Fields }}// Set{{ .GoField }} assigns {{ .Name }} and updates its serialized presence.
func ({{ $.Receiver }} *{{ $.StructName }}) Set{{ .GoField }}(value {{ .SetterGoType }}) {
	{{ $.Receiver }}.{{ .GoField }} = {{ .SetterAssignment }}
	{{ $.Receiver }}.dirty = true
{{- if eq .Style 3 }}
	if {{ .SetterDefaultExpr }} {
		{{ $.Receiver }}.present &^= {{ .BitConst }}
		return
	}
{{- end }}
	{{ $.Receiver }}.present |= {{ .BitConst }}
}

{{ end }}{{ range .Fields }}// Has{{ .GoField }} reports whether {{ .Name }} is present.
func ({{ $.Receiver }} *{{ $.StructName }}) Has{{ .GoField }}() bool {
	return {{ $.Receiver }} != nil && {{ $.Receiver }}.present&{{ .BitConst }} != 0
}

{{ if .Clearable }}// Clear{{ .GoField }} removes {{ .Name }} from the serialized entry.
func ({{ $.Receiver }} *{{ $.StructName }}) Clear{{ .GoField }}() {
	if {{ $.Receiver }} == nil {
		return
	}
	{{ $.Receiver }}.{{ .GoField }} = {{ zeroValue .GoType }}
	{{ $.Receiver }}.present &^= {{ .BitConst }}
	{{ $.Receiver }}.dirty = true
}

{{ end }}{{ if eq .XRPLType "Amount" }}// Get{{ .GoField }} returns the typed Amount value.
func ({{ $.Receiver }} *{{ $.StructName }}) Get{{ .GoField }}() (AmountValue, error) {
	if {{ $.Receiver }} == nil {
		return AmountValue{}, nil
	}
	return amountValueFromAny({{ $.Receiver }}.{{ .GoField }}, {{ printf "%q" (printf "%s.%s" $.Name .Name) }}, {{ .XRPOnly }})
}

// Set{{ .GoField }}Value assigns a typed Amount value.
func ({{ $.Receiver }} *{{ $.StructName }}) Set{{ .GoField }}Value(value AmountValue) error {
	encoded, err := amountValueToAny(value, {{ printf "%q" (printf "%s.%s" $.Name .Name) }}, {{ .XRPOnly }})
	if err != nil {
		return err
	}
	{{ $.Receiver }}.Set{{ .GoField }}(encoded)
	return nil
}

{{ else if eq .XRPLType "Vector256" }}// Get{{ .GoField }} returns the typed Vector256 values.
func ({{ $.Receiver }} *{{ $.StructName }}) Get{{ .GoField }}() (Vector256Value, error) {
	if {{ $.Receiver }} == nil {
		return nil, nil
	}
	if !{{ $.Receiver }}.Has{{ .GoField }}() {
		return nil, nil
	}
	return vector256ValueFromStrings({{ $.Receiver }}.{{ .GoField }}, {{ printf "%q" (printf "%s.%s" $.Name .Name) }})
}

// Set{{ .GoField }}Value assigns typed Vector256 values.
func ({{ $.Receiver }} *{{ $.StructName }}) Set{{ .GoField }}Value(value Vector256Value) {
	{{ $.Receiver }}.Set{{ .GoField }}(vector256ValueToStrings(value))
}

{{ else if eq .XRPLType "Issue" }}// Get{{ .GoField }} returns the typed Issue value.
func ({{ $.Receiver }} *{{ $.StructName }}) Get{{ .GoField }}() (IssueValue, error) {
	if {{ $.Receiver }} == nil {
		return IssueValue{}, nil
	}
	if !{{ $.Receiver }}.Has{{ .GoField }}() {
		return IssueValue{}, nil
	}
	return issueValueFromAny({{ $.Receiver }}.{{ .GoField }}, {{ printf "%q" (printf "%s.%s" $.Name .Name) }})
}

// Set{{ .GoField }}Value assigns a typed Issue value.
func ({{ $.Receiver }} *{{ $.StructName }}) Set{{ .GoField }}Value(value IssueValue) error {
	encoded, err := issueValueToAny(value, {{ printf "%q" (printf "%s.%s" $.Name .Name) }})
	if err != nil {
		return err
	}
	{{ $.Receiver }}.Set{{ .GoField }}(encoded)
	return nil
}

{{ else if eq .XRPLType "XChainBridge" }}// Get{{ .GoField }} returns the typed XChainBridge value.
func ({{ $.Receiver }} *{{ $.StructName }}) Get{{ .GoField }}() (XChainBridgeValue, error) {
	if {{ $.Receiver }} == nil {
		return XChainBridgeValue{}, nil
	}
	if !{{ $.Receiver }}.Has{{ .GoField }}() {
		return XChainBridgeValue{}, nil
	}
	return xchainBridgeValueFromAny({{ $.Receiver }}.{{ .GoField }}, {{ printf "%q" (printf "%s.%s" $.Name .Name) }})
}

// Set{{ .GoField }}Value assigns a typed XChainBridge value.
func ({{ $.Receiver }} *{{ $.StructName }}) Set{{ .GoField }}Value(value XChainBridgeValue) error {
	encoded, err := xchainBridgeValueToAny(value, {{ printf "%q" (printf "%s.%s" $.Name .Name) }})
	if err != nil {
		return err
	}
	{{ $.Receiver }}.Set{{ .GoField }}(encoded)
	return nil
}

{{ else if eq .XRPLType "Number" }}// Get{{ .GoField }} returns the exact decoded Number text.
func ({{ $.Receiver }} *{{ $.StructName }}) Get{{ .GoField }}() (NumberValue, error) {
	if {{ $.Receiver }} == nil {
		return "0", nil
	}
	if !{{ $.Receiver }}.Has{{ .GoField }}() {
		return "0", nil
	}
	return numberValueFromAny({{ $.Receiver }}.{{ .GoField }}, {{ printf "%q" (printf "%s.%s" $.Name .Name) }})
}

// Set{{ .GoField }}Value assigns an exact Number text value.
func ({{ $.Receiver }} *{{ $.StructName }}) Set{{ .GoField }}Value(value NumberValue) error {
	encoded, err := numberValueToAny(value, {{ printf "%q" (printf "%s.%s" $.Name .Name) }})
	if err != nil {
		return err
	}
	{{ $.Receiver }}.Set{{ .GoField }}(encoded)
	return nil
}

{{ else if and (eq .XRPLType "STObject") .Compound }}// Get{{ .GoField }} returns the typed nested object.
func ({{ $.Receiver }} *{{ $.StructName }}) Get{{ .GoField }}() ({{ .Compound.Type }}, error) {
	if {{ $.Receiver }} == nil {
		return {{ .Compound.Type }}{}, nil
	}
	if !{{ $.Receiver }}.Has{{ .GoField }}() {
		return {{ .Compound.Type }}{}, nil
	}
	return {{ lowerFirst .Compound.Type }}FromAny({{ $.Receiver }}.{{ .GoField }}, {{ printf "%q" (printf "%s.%s" $.Name .Name) }})
}

// Set{{ .GoField }}Value assigns the typed nested object.
func ({{ $.Receiver }} *{{ $.StructName }}) Set{{ .GoField }}Value(value {{ .Compound.Type }}) error {
	encoded, err := {{ lowerFirst .Compound.Type }}ToAny(value, {{ printf "%q" (printf "%s.%s" $.Name .Name) }})
	if err != nil {
		return err
	}
	{{ $.Receiver }}.Set{{ .GoField }}(encoded)
	return nil
}

{{ else if and (eq .XRPLType "STArray") .Compound }}// Get{{ .GoField }} returns typed nested objects.
func ({{ $.Receiver }} *{{ $.StructName }}) Get{{ .GoField }}() ([]{{ .Compound.Type }}, error) {
	if {{ $.Receiver }} == nil {
		return nil, nil
	}
	if !{{ $.Receiver }}.Has{{ .GoField }}() {
		return nil, nil
	}
	return {{ lowerFirst .Compound.Type }}SliceFromAny({{ $.Receiver }}.{{ .GoField }}, {{ printf "%q" (printf "%s.%s" $.Name .Name) }})
}

// Set{{ .GoField }}Value assigns typed nested objects.
func ({{ $.Receiver }} *{{ $.StructName }}) Set{{ .GoField }}Value(value []{{ .Compound.Type }}) error {
	encoded, err := {{ lowerFirst .Compound.Type }}SliceToAny(value, {{ printf "%q" (printf "%s.%s" $.Name .Name) }})
	if err != nil {
		return err
	}
	{{ $.Receiver }}.Set{{ .GoField }}(encoded)
	return nil
}

{{ else if eq .XRPLType "Hash128" }}// Get{{ .GoField }} returns the typed 128-bit hash.
func ({{ $.Receiver }} *{{ $.StructName }}) Get{{ .GoField }}() ([16]byte, error) {
	var result [16]byte
	if {{ $.Receiver }} == nil {
		return result, nil
	}
	raw, err := hashValueFromString({{ $.Receiver }}.{{ .GoField }}, {{ printf "%q" (printf "%s.%s" $.Name .Name) }}, 16)
	copy(result[:], raw)
	return result, err
}

// Set{{ .GoField }}Value assigns a typed 128-bit hash.
func ({{ $.Receiver }} *{{ $.StructName }}) Set{{ .GoField }}Value(value [16]byte) {
	{{ $.Receiver }}.Set{{ .GoField }}(hashValueToString(value[:]))
}

{{ else if or (eq .XRPLType "Hash160") (eq .XRPLType "Currency") }}// Get{{ .GoField }} returns the typed 160-bit hash.
func ({{ $.Receiver }} *{{ $.StructName }}) Get{{ .GoField }}() ([20]byte, error) {
	var result [20]byte
	if {{ $.Receiver }} == nil {
		return result, nil
	}
	raw, err := hashValueFromString({{ $.Receiver }}.{{ .GoField }}, {{ printf "%q" (printf "%s.%s" $.Name .Name) }}, 20)
	copy(result[:], raw)
	return result, err
}

// Set{{ .GoField }}Value assigns a typed 160-bit hash.
func ({{ $.Receiver }} *{{ $.StructName }}) Set{{ .GoField }}Value(value [20]byte) {
	{{ $.Receiver }}.Set{{ .GoField }}(hashValueToString(value[:]))
}

{{ else if eq .XRPLType "Hash192" }}// Get{{ .GoField }} returns the typed 192-bit hash.
func ({{ $.Receiver }} *{{ $.StructName }}) Get{{ .GoField }}() ([24]byte, error) {
	var result [24]byte
	if {{ $.Receiver }} == nil {
		return result, nil
	}
	raw, err := hashValueFromString({{ $.Receiver }}.{{ .GoField }}, {{ printf "%q" (printf "%s.%s" $.Name .Name) }}, 24)
	copy(result[:], raw)
	return result, err
}

// Set{{ .GoField }}Value assigns a typed 192-bit hash.
func ({{ $.Receiver }} *{{ $.StructName }}) Set{{ .GoField }}Value(value [24]byte) {
	{{ $.Receiver }}.Set{{ .GoField }}(hashValueToString(value[:]))
}

{{ else if eq .XRPLType "Hash256" }}// Get{{ .GoField }} returns the typed 256-bit hash.
func ({{ $.Receiver }} *{{ $.StructName }}) Get{{ .GoField }}() ([32]byte, error) {
	var result [32]byte
	if {{ $.Receiver }} == nil {
		return result, nil
	}
	raw, err := hashValueFromString({{ $.Receiver }}.{{ .GoField }}, {{ printf "%q" (printf "%s.%s" $.Name .Name) }}, 32)
	copy(result[:], raw)
	return result, err
}

// Set{{ .GoField }}Value assigns a typed 256-bit hash.
func ({{ $.Receiver }} *{{ $.StructName }}) Set{{ .GoField }}Value(value [32]byte) {
	{{ $.Receiver }}.Set{{ .GoField }}(hashValueToString(value[:]))
}

{{ else if eq .XRPLType "Blob" }}// Get{{ .GoField }} returns the raw bytes of the Blob field.
func ({{ $.Receiver }} *{{ $.StructName }}) Get{{ .GoField }}() ([]byte, error) {
	if {{ $.Receiver }} == nil {
		return nil, nil
	}
	return blobValueFromString({{ $.Receiver }}.{{ .GoField }}, {{ printf "%q" (printf "%s.%s" $.Name .Name) }})
}

// Set{{ .GoField }}Value assigns a Blob from raw bytes.
func ({{ $.Receiver }} *{{ $.StructName }}) Set{{ .GoField }}Value(value []byte) {
	{{ $.Receiver }}.Set{{ .GoField }}(blobValueToString(value))
}

{{ else if eq .XRPLType "AccountID" }}// Get{{ .GoField }} returns the 20-byte AccountID.
func ({{ $.Receiver }} *{{ $.StructName }}) Get{{ .GoField }}() ([20]byte, error) {
	if {{ $.Receiver }} == nil {
		return [20]byte{}, nil
	}
	return accountIDValueFromString({{ $.Receiver }}.{{ .GoField }}, {{ printf "%q" (printf "%s.%s" $.Name .Name) }})
}

// Set{{ .GoField }}Value assigns a 20-byte AccountID.
func ({{ $.Receiver }} *{{ $.StructName }}) Set{{ .GoField }}Value(value [20]byte) error {
	address, err := accountIDValueToString(value)
	if err != nil {
		return err
	}
	{{ $.Receiver }}.Set{{ .GoField }}(address)
	return nil
}

{{ else if eq .XRPLType "UInt8" }}// Get{{ .GoField }} returns the typed UInt8 value.
func ({{ $.Receiver }} *{{ $.StructName }}) Get{{ .GoField }}() (uint8, error) {
	if {{ $.Receiver }} == nil {
		return 0, nil
	}
	if {{ $.Receiver }}.{{ .GoField }} < 0 || {{ $.Receiver }}.{{ .GoField }} > 255 {
		return 0, fmt.Errorf("ledgerfields: {{ $.Name }}.{{ .Name }}: value %d is out of range for UInt8", {{ $.Receiver }}.{{ .GoField }})
	}
	return uint8({{ $.Receiver }}.{{ .GoField }}), nil
}

{{ else if eq .XRPLType "UInt16" }}// Get{{ .GoField }} returns the typed UInt16 value.
func ({{ $.Receiver }} *{{ $.StructName }}) Get{{ .GoField }}() (uint16, error) {
	if {{ $.Receiver }} == nil {
		return 0, nil
	}
	if {{ $.Receiver }}.{{ .GoField }} < 0 || {{ $.Receiver }}.{{ .GoField }} > 65535 {
		return 0, fmt.Errorf("ledgerfields: {{ $.Name }}.{{ .Name }}: value %d is out of range for UInt16", {{ $.Receiver }}.{{ .GoField }})
	}
	return uint16({{ $.Receiver }}.{{ .GoField }}), nil
}

// Set{{ .GoField }}Value assigns a typed UInt16 value.
func ({{ $.Receiver }} *{{ $.StructName }}) Set{{ .GoField }}Value(value uint16) {
	{{ $.Receiver }}.Set{{ .GoField }}(value)
}

{{ else if eq .XRPLType "UInt32" }}// Get{{ .GoField }} returns the typed UInt32 value.
func ({{ $.Receiver }} *{{ $.StructName }}) Get{{ .GoField }}() (uint32, error) {
	if {{ $.Receiver }} == nil {
		return 0, nil
	}
	return {{ $.Receiver }}.{{ .GoField }}, nil
}

// Set{{ .GoField }}Value assigns a typed UInt32 value.
func ({{ $.Receiver }} *{{ $.StructName }}) Set{{ .GoField }}Value(value uint32) {
	{{ $.Receiver }}.Set{{ .GoField }}(value)
}

{{ else if eq .XRPLType "UInt64" }}// Get{{ .GoField }} returns the typed UInt64 value.
func ({{ $.Receiver }} *{{ $.StructName }}) Get{{ .GoField }}() (uint64, error) {
	if {{ $.Receiver }} == nil {
		return 0, nil
	}
	return uint64ValueFromString({{ $.Receiver }}.{{ .GoField }}, {{ printf "%q" (printf "%s.%s" $.Name .Name) }}, {{ .IsBaseTenUInt64 }})
}

// Set{{ .GoField }}Value assigns a typed UInt64 value.
func ({{ $.Receiver }} *{{ $.StructName }}) Set{{ .GoField }}Value(value uint64) {
	{{ $.Receiver }}.Set{{ .GoField }}(uint64ValueToString(value, {{ .IsBaseTenUInt64 }}))
}

{{ end }}{{ end }}func ({{ .Receiver }} *{{ .StructName }}) validateRequired() error {
	if {{ .Receiver }}.decoded && !{{ .Receiver }}.dirty {
		return nil
	}
{{- range .Fields }}{{ if and (eq .Style 1) (not .DeferredRequired) }}
	if {{ $.Receiver }}.present&{{ .BitConst }} == 0 {
		return errors.New({{ printf "%q" (printf "ledgerfields: %s: required field %s is not set" $.Name .Name) }})
	}
{{- end }}{{ end }}
	return nil
}

func ({{ .Receiver }} *{{ .StructName }}) validateDecoded() error {
{{- range .Fields }}{{ if eq .Style 1 }}
	if {{ $.Receiver }}.present&{{ .BitConst }} == 0 {
		return errors.New({{ printf "%q" (printf "ledgerfields: %s: required field %s is missing" $.Name .Name) }})
	}
{{- else if eq .Style 3 }}
	if {{ $.Receiver }}.present&{{ .BitConst }} != 0 && {{ .DefaultExpr }} {
		return errors.New({{ printf "%q" (printf "ledgerfields: %s: default field %s is explicitly set" $.Name .Name) }})
	}
{{- end }}{{ end }}
	return nil
}

// Decode populates the struct from binary ledger-entry data via a streaming
// reader and enforces the current rippled ledger template.
func ({{ .Receiver }} *{{ .StructName }}) Decode(data []byte) error {
	return {{ .Receiver }}.decode(data, false)
}

func ({{ .Receiver }} *{{ .StructName }}) decodeLegacy(data []byte) error {
	return {{ .Receiver }}.decode(data, true)
}

func ({{ .Receiver }} *{{ .StructName }}) decode(data []byte, legacy bool) error {
	*{{ .Receiver }} = {{ .StructName }}{}
	sr := newStreamReader(data)
	seenFields := make(map[[2]int]struct{})
	sawLedgerEntryType := false
{{- if .AllowBadCurrencyDecode }}
	preserveDecodedBinary := false
{{- end }}
	for sr.hasMore() {
		typeCode, fieldCode, err := sr.readFieldHeader()
		if err != nil {
			return err
		}
		fieldID := [2]int{typeCode, fieldCode}
		if _, exists := seenFields[fieldID]; exists {
			return fmt.Errorf("ledgerfields: {{ .Name }}: duplicate field type=%d field=%d", typeCode, fieldCode)
		}
		seenFields[fieldID] = struct{}{}
{{- if .DecodeOnlyArms }}
		if !legacy {
			switch {
{{- range .DecodeOnlyArms }}
			case typeCode == {{ .TypeCode }} && fieldCode == {{ .FieldCode }}:
				return fmt.Errorf("ledgerfields: {{ $.Name }}: field type=%d field=%d is not allowed", typeCode, fieldCode)
{{- end }}
			}
		}
{{- end }}
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
{{- if eq $arm.GoField "LedgerEntryType" }}
				// {{ $arm.GoField }} is synthetic; discard
{{- else }}
				// {{ $arm.GoField }} decoded for tolerance only; discard
{{- end }}
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
{{- else if $.AllowBadCurrencyDecode }}
			val, badCurrency, err := sr.readAmountAnyAllowBadCurrency()
			preserveDecodedBinary = preserveDecodedBinary || badCurrency
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
{{- else if eq $first.XRPLType "Int32" }}
			i32Val, err := sr.readInt32()
			if err != nil {
				return err
			}
			val := int(i32Val)
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
			val, err := sr.readSTObject({{ printf "%q" $first.FieldName }})
			if err != nil {
				return err
			}
{{- else if eq $first.XRPLType "STArray" }}
			val, err := sr.readSTArray({{ printf "%q" $first.FieldName }})
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
{{- if eq $arm.GoField "LedgerEntryType" }}
				if val != {{ $.EntryTypeCode }} {
					return fmt.Errorf("ledgerfields: {{ $.Name }}: LedgerEntryType is %d, want {{ $.EntryTypeCode }}", val)
				}
				sawLedgerEntryType = true
{{- else }}
				_ = val // {{ $arm.GoField }} decoded for tolerance only; discard
{{- end }}
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
	if !sawLedgerEntryType {
		return errors.New({{ printf "%q" (printf "ledgerfields: %s: missing LedgerEntryType" .Name) }})
	}
{{- if .AllowBadCurrencyDecode }}
	if preserveDecodedBinary {
		{{ .Receiver }}.decodedBinary = append([]byte(nil), data...)
	}
{{- end }}
	{{ .Receiver }}.decoded = true
	if !legacy {
		return {{ .Receiver }}.validateDecoded()
	}
	return nil
}

// emitAll writes every present default-meta field. skipDefault filters the
// "zero" value for CreatedNode.NewFields to match rippled, which omits
// defaulted fields from NewFields.
func ({{ .Receiver }} *{{ .StructName }}) emitAll(out map[string]any, skipDefault bool) {
{{- range .Fields }}{{ if eq .Meta 0 }}
	if {{ $.Receiver }}.present&{{ .BitConst }} != 0{{ if .DefaultExpr }} && !(skipDefault && {{ .DefaultExpr }}){{ end }} {
		out[{{ printf "%q" .Name }}] = {{ $.Receiver }}.{{ .GoField }}
	}
{{- end }}{{ end }}
{{- range .Fields }}{{ if eq .Meta 1 }}
	if {{ $.Receiver }}.present&{{ .BitConst }} != 0{{ if .DefaultExpr }} && !(skipDefault && {{ .DefaultExpr }}){{ end }} {
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
	prv, ok := prev.(*{{ .StructName }})
	if !ok || prv == nil {
		return
	}
{{- range .Fields }}{{ if eq .Meta 0 }}
	emitIfChanged{{ .Comparer }}(out, {{ printf "%q" .Name }}, prv.{{ .GoField }}, {{ $.Receiver }}.{{ .GoField }}, prv.present&{{ .BitConst }}, {{ $.Receiver }}.present&{{ .BitConst }})
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

// Encode serializes the receiver to canonical XRPL binary. Legacy decode
// aliases and non-canonical input ordering are emitted in canonical form.
func ({{ .Receiver }} *{{ .StructName }}) Encode() ([]byte, error) {
	if err := {{ .Receiver }}.validateRequired(); err != nil {
		return nil, err
	}
{{- range .Fields }}{{ if eq .XRPLType "STArray" }}
	if {{ $.Receiver }}.present&{{ .BitConst }} != 0 {
		if err := validateSTArrayForEncode({{ printf "%q" .Name }}, {{ $.Receiver }}.{{ .GoField }}); err != nil {
			return nil, err
		}
	}
{{- else if eq .XRPLType "STObject" }}
	if {{ $.Receiver }}.present&{{ .BitConst }} != 0 {
		if err := validateInnerObjectForEncode({{ printf "%q" .Name }}, {{ $.Receiver }}.{{ .GoField }}); err != nil {
			return nil, err
		}
	}
{{- end }}{{ end }}
{{- if .AllowBadCurrencyDecode }}
	if {{ .Receiver }}.decoded && !{{ .Receiver }}.dirty && len({{ .Receiver }}.decodedBinary) != 0 {
		return append([]byte(nil), {{ .Receiver }}.decodedBinary...), nil
	}
{{- end }}
	out := {{ .Receiver }}.ToMap()
{{- range .Fields }}{{ if .DeferredRequired }}
	if {{ $.Receiver }}.present&{{ .BitConst }} == 0 {
{{- if eq .Name "PreviousTxnID" }}
		out[{{ printf "%q" .Name }}] = "0000000000000000000000000000000000000000000000000000000000000000"
{{- else }}
		out[{{ printf "%q" .Name }}] = uint32(0)
{{- end }}
	}
{{- end }}{{ end }}
	return binarycodec.EncodeBytes(out)
}

// Hash returns the SHAMap account-state leaf hash for this entry,
// sha512Half(HashPrefixLeafNode || encoded || index). index is the
// 32-byte keylet under which the entry is stored.
func ({{ .Receiver }} *{{ .StructName }}) Hash(index [32]byte) ([32]byte, error) {
	data, err := {{ .Receiver }}.Encode()
	if err != nil {
		return [32]byte{}, err
	}
	prefix := protocol.HashPrefixLeafNode()
	return sha512half.Sum(prefix[:], data, index[:]), nil
}
`))

var innerValuesTemplate = template.Must(template.New("innerValues").Funcs(template.FuncMap{
	"lowerFirst": func(s string) string {
		if s == "" {
			return s
		}
		return strings.ToLower(s[:1]) + s[1:]
	},
	"hasPrefix": strings.HasPrefix,
	"zeroValue": func(goType string) string {
		switch {
		case strings.HasPrefix(goType, "[]"):
			return "nil"
		case goType == "string":
			return `""`
		case strings.HasPrefix(goType, "[") || strings.HasSuffix(goType, "Value"):
			return goType + "{}"
		default:
			return "0"
		}
	},
	"cloneExpr": func(goType, nested, value string) string {
		if goType == "[]byte" {
			return "innerCloneBytes(" + value + ")"
		}
		if strings.HasPrefix(goType, "[]") && nested != "" {
			return "clone" + nested + "Slice(" + value + ")"
		}
		return value
	},
	"isRequired": func(style uint8) bool { return style == uint8(schema.StyleRequired-1) },
	"isOptional": func(style uint8) bool { return style != uint8(schema.StyleRequired-1) },
}).Parse(`// Code generated by entrygen; DO NOT EDIT.
//
// Source: ledger/entry/schema/inner.go and codec definitions.
// Regenerate: go generate ./ledger/entry/...

package entry

import (
	"encoding/hex"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
)

// NumberValue preserves the exact JSON text of an XRPL Number.
type NumberValue = string

// Vector256Value is a typed vector of 256-bit hashes.
type Vector256Value = [][32]byte

// IssueValue is the typed JSON representation of an XRPL Issue.
type IssueValue struct {
	Currency      string
	Issuer        string
	MPTIssuanceID string
}

// XChainBridgeValue is the typed representation of an XRPL XChainBridge.
type XChainBridgeValue struct {
	LockingChainDoor  [20]byte
	LockingChainIssue IssueValue
	IssuingChainDoor  [20]byte
	IssuingChainIssue IssueValue
	lockingChainDoorEmpty bool
	issuingChainDoorEmpty bool
}

func vector256ValueFromStrings(values []string, field string) (Vector256Value, error) {
	if values == nil {
		return nil, nil
	}
	result := make(Vector256Value, len(values))
	for i, value := range values {
		raw, err := hashValueFromString(value, fmt.Sprintf("%s[%d]", field, i), 32)
		if err != nil {
			return nil, err
		}
		copy(result[i][:], raw)
	}
	return result, nil
}

func vector256ValueToStrings(values Vector256Value) []string {
	if values == nil {
		return nil
	}
	result := make([]string, len(values))
	for i := range values {
		result[i] = hashValueToString(values[i][:])
	}
	return result
}

func issueValueFromAny(value any, field string) (IssueValue, error) {
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return IssueValue{}, fmt.Errorf("ledgerfields: %s: issue has type %T, want object", field, value)
	}
	result := IssueValue{}
	if raw, ok := object["mpt_issuance_id"]; ok {
		id, ok := raw.(string)
		if !ok || id == "" {
			return result, fmt.Errorf("ledgerfields: %s: MPT issuance ID has type %T, want non-empty string", field, raw)
		}
		if decoded, err := hex.DecodeString(id); err != nil || len(decoded) != 24 {
			return result, fmt.Errorf("ledgerfields: %s: invalid MPT issuance ID", field)
		}
		if _, ok := object["currency"]; ok {
			return result, fmt.Errorf("ledgerfields: %s: MPT issue cannot carry currency", field)
		}
		if _, ok := object["issuer"]; ok {
			return result, fmt.Errorf("ledgerfields: %s: MPT issue cannot carry issuer", field)
		}
		result.MPTIssuanceID = id
		return result, nil
	}
	currency, ok := object["currency"].(string)
	if !ok || currency == "" {
		return result, fmt.Errorf("ledgerfields: %s: issue currency is missing or invalid", field)
	}
	result.Currency = currency
	if currency == "XRP" {
		if _, ok := object["issuer"]; ok {
			return IssueValue{}, fmt.Errorf("ledgerfields: %s: XRP issue cannot carry issuer", field)
		}
		return result, nil
	}
	issuer, ok := object["issuer"].(string)
	if !ok || issuer == "" {
		return IssueValue{}, fmt.Errorf("ledgerfields: %s: issue issuer is missing or invalid", field)
	}
	result.Issuer = issuer
	return result, nil
}

func issueValueToAny(value IssueValue, field string) (any, error) {
	if value.MPTIssuanceID != "" {
		if value.Currency != "" || value.Issuer != "" {
			return nil, fmt.Errorf("ledgerfields: %s: MPT issue cannot carry currency or issuer", field)
		}
		decoded, err := hex.DecodeString(value.MPTIssuanceID)
		if err != nil || len(decoded) != 24 {
			return nil, fmt.Errorf("ledgerfields: %s: invalid MPT issuance ID", field)
		}
		return map[string]any{"mpt_issuance_id": strings.ToUpper(value.MPTIssuanceID)}, nil
	}
	currency := value.Currency
	if currency == "" {
		currency = "XRP"
	}
	if currency == "XRP" {
		if value.Issuer != "" {
			return nil, fmt.Errorf("ledgerfields: %s: XRP issue cannot carry issuer", field)
		}
		return map[string]any{"currency": currency}, nil
	}
	if value.Issuer == "" {
		return nil, fmt.Errorf("ledgerfields: %s: issued issue requires issuer", field)
	}
	return map[string]any{"currency": currency, "issuer": value.Issuer}, nil
}

func xchainBridgeValueFromAny(value any, field string) (XChainBridgeValue, error) {
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return XChainBridgeValue{}, fmt.Errorf("ledgerfields: %s: bridge has type %T, want object", field, value)
	}
	var result XChainBridgeValue
	var err error
	if raw, ok := object["LockingChainDoor"].(string); ok && raw == "" {
		result.lockingChainDoorEmpty = true
	}
	if result.LockingChainDoor, err = accountIDValueFromStringValue(object["LockingChainDoor"], field+".LockingChainDoor"); err != nil {
		return result, err
	}
	if result.LockingChainIssue, err = issueValueFromAny(object["LockingChainIssue"], field+".LockingChainIssue"); err != nil {
		return result, err
	}
	if result.IssuingChainDoor, err = accountIDValueFromStringValue(object["IssuingChainDoor"], field+".IssuingChainDoor"); err != nil {
		return result, err
	}
	if raw, ok := object["IssuingChainDoor"].(string); ok && raw == "" {
		result.issuingChainDoorEmpty = true
	}
	if result.IssuingChainIssue, err = issueValueFromAny(object["IssuingChainIssue"], field+".IssuingChainIssue"); err != nil {
		return result, err
	}
	return result, nil
}

func xchainBridgeValueToAny(value XChainBridgeValue, field string) (any, error) {
	lockingDoor, err := innerAccountValueToAny(value.LockingChainDoor, value.lockingChainDoorEmpty, field+".LockingChainDoor")
	if err != nil {
		return nil, err
	}
	issuingDoor, err := innerAccountValueToAny(value.IssuingChainDoor, value.issuingChainDoorEmpty, field+".IssuingChainDoor")
	if err != nil {
		return nil, err
	}
	lockingIssue, err := issueValueToAny(value.LockingChainIssue, field+".LockingChainIssue")
	if err != nil {
		return nil, err
	}
	issuingIssue, err := issueValueToAny(value.IssuingChainIssue, field+".IssuingChainIssue")
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"LockingChainDoor":  lockingDoor,
		"LockingChainIssue": lockingIssue,
		"IssuingChainDoor":  issuingDoor,
		"IssuingChainIssue": issuingIssue,
	}, nil
}

func accountIDValueFromStringValue(value any, field string) ([20]byte, error) {
	s, ok := value.(string)
	if !ok {
		return [20]byte{}, fmt.Errorf("ledgerfields: %s: account has type %T, want string", field, value)
	}
	return accountIDValueFromString(s, field)
}

func innerAccountValueToAny(value [20]byte, empty bool, field string) (any, error) {
	if empty && value == [20]byte{} {
		return "", nil
	}
	return accountIDValueToString(value)
}

func numberValueFromAny(value any, field string) (NumberValue, error) {
	if value == nil {
		return "0", nil
	}
	s, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("ledgerfields: %s: Number has type %T, want string", field, value)
	}
	return s, nil
}

func numberValueToAny(value NumberValue, field string) (any, error) {
	if value == "" {
		return "0", nil
	}
	return string(value), nil
}

func innerUnsigned(value any, field string, max uint64) (uint64, error) {
	var n uint64
	switch v := value.(type) {
	case int:
		if v < 0 { return 0, fmt.Errorf("ledgerfields: %s: negative integer", field) }; n = uint64(v)
	case int32:
		if v < 0 { return 0, fmt.Errorf("ledgerfields: %s: negative integer", field) }; n = uint64(v)
	case int64:
		if v < 0 { return 0, fmt.Errorf("ledgerfields: %s: negative integer", field) }; n = uint64(v)
	case uint8: n = uint64(v)
	case uint16: n = uint64(v)
	case uint32: n = uint64(v)
	case uint64: n = v
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > float64(max) || v != math.Trunc(v) { return 0, fmt.Errorf("ledgerfields: %s: invalid integer", field) }; n = uint64(v)
	default:
		return 0, fmt.Errorf("ledgerfields: %s: integer has type %T", field, value)
	}
	if n > max { return 0, fmt.Errorf("ledgerfields: %s: integer %d is out of range", field, n) }
	return n, nil
}

func innerValueFromAny(value any, field, xrplType string, baseTen bool) (any, error) {
	switch xrplType {
	case "UInt8": n, err := innerUnsigned(value, field, 1<<8-1); return uint8(n), err
	case "UInt16": n, err := innerUnsigned(value, field, 1<<16-1); return uint16(n), err
	case "UInt32": n, err := innerUnsigned(value, field, 1<<32-1); return uint32(n), err
	case "UInt64":
		s, ok := value.(string); if !ok { return nil, fmt.Errorf("ledgerfields: %s: UInt64 has type %T", field, value) }
		n, err := uint64ValueFromString(s, field, baseTen); return n, err
	case "Hash128":
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("ledgerfields: %s: Hash128 has type %T, want string", field, value)
		}
		raw, err := hashValueFromString(s, field, 16)
		var result [16]byte
		copy(result[:], raw)
		return result, err
	case "Hash160", "Currency":
		s, ok := value.(string); if !ok { return nil, fmt.Errorf("ledgerfields: %s: %T, want string", field, value) }
		if xrplType == "Currency" { return s, nil }
		raw, err := hashValueFromString(s, field, 20); var result [20]byte; copy(result[:], raw); return result, err
	case "Hash192": s, ok := value.(string); if !ok { return nil, fmt.Errorf("ledgerfields: %s: %T, want string", field, value) }; raw, err := hashValueFromString(s, field, 24); var result [24]byte; copy(result[:], raw); return result, err
	case "Hash256": s, ok := value.(string); if !ok { return nil, fmt.Errorf("ledgerfields: %s: %T, want string", field, value) }; raw, err := hashValueFromString(s, field, 32); var result [32]byte; copy(result[:], raw); return result, err
	case "AccountID": return accountIDValueFromStringValue(value, field)
	case "Blob": s, ok := value.(string); if !ok { return nil, fmt.Errorf("ledgerfields: %s: Blob has type %T", field, value) }; return blobValueFromString(s, field)
	case "Amount": return amountValueFromAny(value, field, false)
	case "Issue": return issueValueFromAny(value, field)
	case "XChainBridge": return xchainBridgeValueFromAny(value, field)
	case "Number": return numberValueFromAny(value, field)
	case "PermissionValue":
		s, ok := value.(string); if !ok { return nil, fmt.Errorf("ledgerfields: %s: PermissionValue has type %T", field, value) }
		if n, err := definitions.Get().DelegatablePermissionValue(s); err == nil { return uint32(n), nil }
		n, err := strconv.ParseUint(s, 10, 32); if err != nil { return nil, fmt.Errorf("ledgerfields: %s: invalid permission %q", field, s) }; return uint32(n), nil
	default: return nil, fmt.Errorf("ledgerfields: %s: unsupported inner type %s", field, xrplType)
	}
}

func innerValueToAny(value any, field, xrplType string, baseTen bool) (any, error) {
	switch xrplType {
	case "UInt8": n, err := innerUnsigned(value, field, 1<<8-1); return uint8(n), err
	case "UInt16": n, err := innerUnsigned(value, field, 1<<16-1); return uint16(n), err
	case "UInt32": n, err := innerUnsigned(value, field, 1<<32-1); return uint32(n), err
	case "UInt64": n, ok := value.(uint64); if !ok { return nil, fmt.Errorf("ledgerfields: %s: want uint64, got %T", field, value) }; return uint64ValueToString(n, baseTen), nil
	case "Hash128": v, ok := value.([16]byte); if !ok { return nil, fmt.Errorf("ledgerfields: %s: want [16]byte, got %T", field, value) }; return hashValueToString(v[:]), nil
	case "Hash160": v, ok := value.([20]byte); if !ok { return nil, fmt.Errorf("ledgerfields: %s: want [20]byte, got %T", field, value) }; return hashValueToString(v[:]), nil
	case "Hash192": v, ok := value.([24]byte); if !ok { return nil, fmt.Errorf("ledgerfields: %s: want [24]byte, got %T", field, value) }; return hashValueToString(v[:]), nil
	case "Hash256": v, ok := value.([32]byte); if !ok { return nil, fmt.Errorf("ledgerfields: %s: want [32]byte, got %T", field, value) }; return hashValueToString(v[:]), nil
	case "Currency": s, ok := value.(string); if !ok { return nil, fmt.Errorf("ledgerfields: %s: want string, got %T", field, value) }; return s, nil
	case "AccountID": v, ok := value.([20]byte); if !ok { return nil, fmt.Errorf("ledgerfields: %s: want [20]byte, got %T", field, value) }; return accountIDValueToString(v)
	case "Blob": v, ok := value.([]byte); if !ok { return nil, fmt.Errorf("ledgerfields: %s: want []byte, got %T", field, value) }; return blobValueToString(v), nil
	case "Amount": v, ok := value.(AmountValue); if !ok { return nil, fmt.Errorf("ledgerfields: %s: want AmountValue, got %T", field, value) }; return amountValueToAny(v, field, false)
	case "Issue": v, ok := value.(IssueValue); if !ok { return nil, fmt.Errorf("ledgerfields: %s: want IssueValue, got %T", field, value) }; return issueValueToAny(v, field)
	case "XChainBridge": v, ok := value.(XChainBridgeValue); if !ok { return nil, fmt.Errorf("ledgerfields: %s: want XChainBridgeValue, got %T", field, value) }; return xchainBridgeValueToAny(v, field)
	case "Number": v, ok := value.(NumberValue); if !ok { return nil, fmt.Errorf("ledgerfields: %s: want NumberValue, got %T", field, value) }; return numberValueToAny(v, field)
	case "PermissionValue": n, ok := value.(uint32); if !ok { return nil, fmt.Errorf("ledgerfields: %s: want uint32, got %T", field, value) }; if name, err := definitions.Get().DelegatablePermissionName(int32(n)); err == nil { return name, nil }; return strconv.FormatUint(uint64(n), 10), nil
	default: return nil, fmt.Errorf("ledgerfields: %s: unsupported inner type %s", field, xrplType)
	}
}

func innerTypedValueIsDefault(value any, xrplType string) bool {
	switch xrplType {
	case "Number": return numberIsDefault(value)
	case "Amount": return amountIsDefault(value)
	case "Blob": v, _ := value.([]byte); return len(v) == 0
	case "Vector256": v, _ := value.(Vector256Value); return len(v) == 0
	case "Currency": v, _ := value.(string); return v == ""
	case "AccountID": v, _ := value.([20]byte); return v == [20]byte{}
	case "Hash128": v, _ := value.([16]byte); return v == [16]byte{}
	case "Hash160": v, _ := value.([20]byte); return v == [20]byte{}
	case "Hash192": v, _ := value.([24]byte); return v == [24]byte{}
	case "Hash256": v, _ := value.([32]byte); return v == [32]byte{}
	case "UInt8": v, _ := value.(uint8); return v == 0
	case "UInt16": v, _ := value.(uint16); return v == 0
	case "UInt32": v, _ := value.(uint32); return v == 0
	case "UInt64": v, _ := value.(uint64); return v == 0
	}
	return reflect.ValueOf(value).IsZero()
}

func innerCloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	return append([]byte(nil), value...)
}

{{ range . }}
func clone{{ .Type }}(value {{ .Type }}) {{ .Type }} {
{{ range .Fields }}{{ if eq .GoType "[]byte" }}	value.{{ .GoField }} = innerCloneBytes(value.{{ .GoField }})
{{ else if .NestedType }}	value.{{ .GoField }} = clone{{ .NestedType }}Slice(value.{{ .GoField }})
{{ end }}{{ end }}	return value
}

func clone{{ .Type }}Slice(value []{{ .Type }}) []{{ .Type }} {
	if value == nil {
		return nil
	}
	result := make([]{{ .Type }}, len(value))
	for i := range value {
		result[i] = clone{{ .Type }}(value[i])
	}
	return result
}
{{ end }}

{{ range . }}{{ $inner := . }}
// {{ .Type }} is the typed representation of the {{ .Name }} nested object.
type {{ .Type }} struct {
	present uint64
	emptyAccounts uint64
{{ range .Fields }}	{{ .GoField }} {{ .GoType }}
{{ end }}}

const (
{{ range $i, $f := .Fields }}{{ if eq $i 0 }}	{{ $f.BitConst }} uint64 = 1 << iota
{{ else }}	{{ $f.BitConst }}
{{ end }}{{ end }})

{{ range .Fields }}func (v {{ $inner.Type }}) Has{{ .GoField }}() bool { return v.present&{{ .BitConst }} != 0 }
{{ if isOptional .Style }}func (v *{{ $inner.Type }}) Clear{{ .GoField }}() { if v == nil { return }; v.{{ .GoField }} = {{ if eq .GoType "[]byte" }}nil{{ else if hasPrefix .GoType "[]" }}nil{{ else if eq .GoType "string" }}""{{ else }}{{ zeroValue .GoType }}{{ end }}; v.present &^= {{ .BitConst }} }
{{ end }}func (v {{ $inner.Type }}) Get{{ .GoField }}() ({{ .GoType }}, error) { return {{ cloneExpr .GoType .NestedType (printf "v.%s" .GoField) }}, nil }
{{ if .SetterError }}func (v *{{ $inner.Type }}) Set{{ .GoField }}(value {{ .GoType }}) error { if v == nil { return fmt.Errorf("ledgerfields: nil {{ $inner.Type }}") }; v.{{ .GoField }} = {{ cloneExpr .GoType .NestedType "value" }}; v.present |= {{ .BitConst }}; {{ if eq .XRPLType "AccountID" }}v.emptyAccounts &^= {{ .BitConst }}; {{ end }}{{ if eq .Style 2 }}if innerTypedValueIsDefault(value, "{{ .XRPLType }}") { v.present &^= {{ .BitConst }} }; {{ end }}return nil }
func (v *{{ $inner.Type }}) Set{{ .GoField }}Value(value {{ .GoType }}) error { return v.Set{{ .GoField }}(value) }
{{ else }}func (v *{{ $inner.Type }}) Set{{ .GoField }}(value {{ .GoType }}) { v.{{ .GoField }} = {{ cloneExpr .GoType .NestedType "value" }}; v.present |= {{ .BitConst }}; {{ if eq .XRPLType "AccountID" }}v.emptyAccounts &^= {{ .BitConst }}; {{ end }}{{ if eq .Style 2 }}if innerTypedValueIsDefault(value, "{{ .XRPLType }}") { v.present &^= {{ .BitConst }} }{{ end }} }
func (v *{{ $inner.Type }}) Set{{ .GoField }}Value(value {{ .GoType }}) { v.Set{{ .GoField }}(value) }
{{ end }}

{{ end }}func {{ lowerFirst .Type }}FromAny(value any, field string) ({{ .Type }}, error) {
	object, ok := value.(map[string]any)
	if !ok || object == nil { return {{ .Type }}{}, fmt.Errorf("ledgerfields: %s: nested object has type %T", field, value) }
	var result {{ .Type }}
	for key := range object {
		switch key {
{{ range $inner.Fields }}		case "{{ .Name }}":
{{ end }}		default:
			return result, fmt.Errorf("ledgerfields: %s: unknown nested field %q", field, key)
		}
	}
{{ range .Fields }}	if raw, ok := object["{{ .Name }}"]; ok {
{{ if .NestedType }}		decoded, err := {{ lowerFirst .NestedType }}SliceFromAny(raw, field+".{{ .Name }}")
		if err != nil { return result, err }
		result.{{ .GoField }} = decoded
{{ else }}		decoded, err := innerValueFromAny(raw, field+".{{ .Name }}", "{{ .XRPLType }}", {{ .BaseTen }})
		if err != nil { return result, err }
		value, ok := decoded.({{ .GoType }})
		if !ok { return result, fmt.Errorf("ledgerfields: %s.{{ .Name }}: decoded value has type %T", field, decoded) }
		result.{{ .GoField }} = value
{{ end }}
		{{ if eq .XRPLType "AccountID" }}if rawString, ok := raw.(string); ok && rawString == "" { result.emptyAccounts |= {{ .BitConst }} }{{ end }}
		result.present |= {{ .BitConst }}
{{ if eq .Style 0 }}	} else {
		return result, fmt.Errorf("ledgerfields: %s: required field {{ .Name }} is missing", field)
{{ end }}	}
{{ end }}	return result, nil
}

func {{ lowerFirst .Type }}ToAny(value {{ .Type }}, field string) (map[string]any, error) {
	result := make(map[string]any)
{{ range .Fields }}{{ if eq .Style 0 }}	if value.present&{{ .BitConst }} == 0 {
		return nil, fmt.Errorf("ledgerfields: %s: required field {{ .Name }} is not set", field)
	} else {
{{ else }}	if value.present&{{ .BitConst }} != 0 {
{{ end }}
{{ if .NestedType }}		raw, err := {{ lowerFirst .NestedType }}SliceToAny(value.{{ .GoField }}, field+".{{ .Name }}")
{{ else if eq .XRPLType "AccountID" }}		raw, err := innerAccountValueToAny(value.{{ .GoField }}, value.emptyAccounts&{{ .BitConst }} != 0, field+".{{ .Name }}")
{{ else }}		raw, err := innerValueToAny(value.{{ .GoField }}, field+".{{ .Name }}", "{{ .XRPLType }}", {{ .BaseTen }})
{{ end }}		if err != nil { return nil, err }
		result["{{ .Name }}"] = raw
	}
{{ end }}	return result, nil
}

func (v {{ .Type }}) ToMap() (map[string]any, error) { return {{ lowerFirst .Type }}ToAny(v, "{{ .Name }}") }

func {{ lowerFirst .Type }}SliceFromAny(value any, field string) ([]{{ .Type }}, error) {
	array, ok := normalizeInnerArray(value)
	if !ok { return nil, fmt.Errorf("ledgerfields: %s: expected array, got %T", field, value) }
	result := make([]{{ .Type }}, 0, len(array))
	for i, item := range array {
		object, ok := item.(map[string]any)
		if !ok { return nil, fmt.Errorf("ledgerfields: %s[%d]: expected wrapped object, got %T", field, i, item) }
		if len(object) != 1 { return nil, fmt.Errorf("ledgerfields: %s[%d]: expected one wrapper", field, i) }
		raw, ok := object["{{ .Name }}"]
		if !ok { return nil, fmt.Errorf("ledgerfields: %s[%d]: expected {{ .Name }} wrapper", field, i) }
		decoded, err := {{ lowerFirst .Type }}FromAny(raw, fmt.Sprintf("%s[%d].{{ .Name }}", field, i))
		if err != nil { return nil, err }
		result = append(result, decoded)
	}
	return result, nil
}

func {{ lowerFirst .Type }}SliceToAny(value []{{ .Type }}, field string) ([]any, error) {
	result := make([]any, 0, len(value))
	for i, item := range value {
		object, err := {{ lowerFirst .Type }}ToAny(item, fmt.Sprintf("%s[%d].{{ .Name }}", field, i))
		if err != nil { return nil, err }
		result = append(result, map[string]any{"{{ .Name }}": object})
	}
	return result, nil
}
{{ end }}`))
