package definitions

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

type oracleFieldSpec struct {
	rawType string
	nth     int32
	flags   string
}

type expectedField struct {
	info    FieldInfo
	header  FieldHeader
	ordinal int32
}

var (
	oracleSTypeLine = regexp.MustCompile(`^STYPE\(\s*(STI_[A-Z0-9_]+)\s*,\s*(-?\d+)\s*\)\s*\\?\s*$`)
	oracleTxLine    = regexp.MustCompile(`^TRANSACTION\(\s*(tt[A-Za-z0-9_]+)\s*,\s*(-?\d+)\s*,\s*([A-Za-z_][A-Za-z0-9_]*)`)
	oracleLELine    = regexp.MustCompile(`^LEDGER_ENTRY(?:_DUPLICATE)?\(\s*(lt[A-Za-z0-9_]+)\s*,\s*(0x[0-9A-Fa-f]+|-?\d+)\s*,\s*([A-Za-z_][A-Za-z0-9_]*)\s*,\s*([A-Za-z_][A-Za-z0-9_]*)\s*,`)
	oracleEnumLine  = regexp.MustCompile(`^enum(?:\s+class)?\s+([A-Za-z_][A-Za-z0-9_]*)`)
	oracleTERLine   = regexp.MustCompile(`^(te[A-Za-z0-9_]+)(?:\s+\[\[[^]]*\]\])?\s*(?:=\s*(-?\d+))?\s*,?\s*$`)
)

func TestRC1DefinitionsMatchRippled(t *testing.T) {
	types, err := parseOracleTypes(requireRC1OracleFile(t, "include/xrpl/protocol/SField.h"))
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 31 {
		t.Fatalf("rippled v3.4.0-rc1 has %d type definitions, want 31", len(types))
	}

	fieldSpecs, err := parseOracleSFields(requireRC1OracleFile(t, "include/xrpl/protocol/detail/sfields.macro"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fieldSpecs) != 349 {
		t.Fatalf("rippled v3.4.0-rc1 has %d active SFields, want 349", len(fieldSpecs))
	}
	expectedFields, err := buildExpectedFields(types, fieldSpecs)
	if err != nil {
		t.Fatal(err)
	}
	if len(expectedFields) != 357 {
		t.Fatalf("expected %d definitions fields, want 357", len(expectedFields))
	}

	transactionTypes, err := parseOracleIDs(requireRC1OracleFile(t, "include/xrpl/protocol/detail/transactions.macro"), "TRANSACTION(", oracleTxLine)
	if err != nil {
		t.Fatal(err)
	}
	transactionTypes["Invalid"] = -1
	if len(transactionTypes) != 83 {
		t.Fatalf("rippled v3.4.0-rc1 has %d transaction definitions including Invalid, want 83", len(transactionTypes))
	}

	ledgerEntryTypes, err := parseOracleIDs(requireRC1OracleFile(t, "include/xrpl/protocol/detail/ledger_entries.macro"), "LEDGER_ENTRY", oracleLELine)
	if err != nil {
		t.Fatal(err)
	}
	ledgerEntryTypes["Invalid"] = -1
	if len(ledgerEntryTypes) != 32 {
		t.Fatalf("rippled v3.4.0-rc1 has %d ledger-entry definitions including Invalid, want 32", len(ledgerEntryTypes))
	}

	transactionResults, err := parseOracleTransactionResults(requireRC1OracleFile(t, "include/xrpl/protocol/TER.h"))
	if err != nil {
		t.Fatal(err)
	}
	if len(transactionResults) != 197 {
		t.Fatalf("rippled v3.4.0-rc1 has %d transaction results, want 197", len(transactionResults))
	}

	defs := Get()
	t.Run("FIELDS", func(t *testing.T) {
		compareFields(t, defs.Fields(), expectedFields)
	})
	t.Run("TYPES", func(t *testing.T) {
		compareIntMaps(t, "TYPES", defs.Types(), types)
	})
	t.Run("TRANSACTION_TYPES", func(t *testing.T) {
		compareIntMaps(t, "TRANSACTION_TYPES", defs.TransactionTypes(), transactionTypes)
	})
	t.Run("LEDGER_ENTRY_TYPES", func(t *testing.T) {
		compareIntMaps(t, "LEDGER_ENTRY_TYPES", defs.ledgerEntryTypes, ledgerEntryTypes)
	})
	t.Run("TRANSACTION_RESULTS", func(t *testing.T) {
		compareIntMaps(t, "TRANSACTION_RESULTS", defs.TransactionResults(), transactionResults)
	})
}

func requireRC1OracleFile(t *testing.T, relative string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve definitions test source path")
	}
	dir := filepath.Dir(file)
	for range 12 {
		candidate := filepath.Join(dir, "rippled-worktrees", "v3.4.0-rc1-oracle", filepath.FromSlash(relative))
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("required rippled v3.4.0-rc1 oracle file %q not found from %s", relative, file)
	return ""
}

func parseOracleTypes(path string) (map[string]int32, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	types := map[string]int32{"Done": -1}
	codes := map[int32]string{-1: "Done"}
	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if !strings.HasPrefix(line, "STYPE(") {
			continue
		}
		match := oracleSTypeLine.FindStringSubmatch(line)
		if match == nil {
			return nil, fmt.Errorf("%s:%d: unparsed STYPE definition %q", path, lineNumber, line)
		}
		code, err := strconv.ParseInt(match[2], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: parse type code: %w", path, lineNumber, err)
		}
		name := translateOracleType(strings.TrimPrefix(match[1], "STI_"))
		if _, found := types[name]; found {
			return nil, fmt.Errorf("%s:%d: duplicate type %q", path, lineNumber, name)
		}
		value := int32(code)
		if previous, found := codes[value]; found {
			return nil, fmt.Errorf("%s:%d: type %q reuses code %d from %q", path, lineNumber, name, value, previous)
		}
		types[name] = value
		codes[value] = name
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	return types, nil
}

func translateOracleType(raw string) string {
	if strings.Contains(raw, "UINT") {
		for _, width := range []string{"512", "384", "256", "192", "160", "128"} {
			if strings.Contains(raw, width) {
				return strings.Replace(raw, "UINT", "Hash", 1)
			}
		}
		return strings.Replace(raw, "UINT", "UInt", 1)
	}
	if name, found := map[string]string{
		"OBJECT":        "STObject",
		"ARRAY":         "STArray",
		"ACCOUNT":       "AccountID",
		"LEDGERENTRY":   "LedgerEntry",
		"NOTPRESENT":    "NotPresent",
		"PATHSET":       "PathSet",
		"VL":            "Blob",
		"XCHAIN_BRIDGE": "XChainBridge",
	}[raw]; found {
		return name
	}

	var translated strings.Builder
	for _, token := range strings.Split(raw, "_") {
		if len(token) > 1 {
			token = strings.ToLower(token)
			translated.WriteString(strings.ToUpper(token[:1]))
			translated.WriteString(token[1:])
		} else {
			translated.WriteString(token)
		}
	}
	return translated.String()
}

func parseOracleSFields(path string) (map[string]oracleFieldSpec, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	fields := make(map[string]oracleFieldSpec)
	scanner := bufio.NewScanner(file)
	var invocation strings.Builder
	depth := 0
	startLine := 0
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := scanner.Text()
		if comment := strings.Index(line, "//"); comment >= 0 {
			line = line[:comment]
		}
		line = strings.TrimSpace(line)
		if depth == 0 {
			if line == "" {
				continue
			}
			if !strings.HasPrefix(line, "TYPED_SFIELD(") && !strings.HasPrefix(line, "UNTYPED_SFIELD(") {
				if strings.Contains(line, "TYPED_SFIELD(") || strings.Contains(line, "UNTYPED_SFIELD(") {
					return nil, fmt.Errorf("%s:%d: unparsed SField definition %q", path, lineNumber, line)
				}
				continue
			}
			invocation.Reset()
			startLine = lineNumber
		} else {
			invocation.WriteByte(' ')
		}
		invocation.WriteString(line)
		depth += parenthesisBalance(line)
		if depth < 0 {
			return nil, fmt.Errorf("%s:%d: unbalanced SField definition", path, lineNumber)
		}
		if depth != 0 {
			continue
		}

		name, spec, err := parseOracleSFieldInvocation(invocation.String(), startLine, path)
		if err != nil {
			return nil, err
		}
		if _, found := fields[name]; found {
			return nil, fmt.Errorf("%s:%d: duplicate SField %q", path, startLine, name)
		}
		fields[name] = spec
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	if depth != 0 {
		return nil, fmt.Errorf("%s:%d: unterminated SField definition", path, startLine)
	}
	return fields, nil
}

func parenthesisBalance(line string) int {
	balance := 0
	for _, character := range line {
		switch character {
		case '(':
			balance++
		case ')':
			balance--
		}
	}
	return balance
}

func parseOracleSFieldInvocation(invocation string, lineNumber int, path string) (string, oracleFieldSpec, error) {
	open := strings.IndexByte(invocation, '(')
	close := strings.LastIndexByte(invocation, ')')
	if open <= 0 || close <= open {
		return "", oracleFieldSpec{}, fmt.Errorf("%s:%d: malformed SField invocation %q", path, lineNumber, invocation)
	}
	macro := strings.TrimSpace(invocation[:open])
	if macro != "TYPED_SFIELD" && macro != "UNTYPED_SFIELD" {
		return "", oracleFieldSpec{}, fmt.Errorf("%s:%d: unknown SField macro %q", path, lineNumber, macro)
	}
	args, err := splitOracleMacroArgs(invocation[open+1 : close])
	if err != nil {
		return "", oracleFieldSpec{}, fmt.Errorf("%s:%d: %w", path, lineNumber, err)
	}
	if len(args) < 3 {
		return "", oracleFieldSpec{}, fmt.Errorf("%s:%d: SField has %d arguments, want at least 3", path, lineNumber, len(args))
	}
	if !strings.HasPrefix(args[0], "sf") || len(args[0]) == 2 {
		return "", oracleFieldSpec{}, fmt.Errorf("%s:%d: invalid SField name %q", path, lineNumber, args[0])
	}
	nth, err := strconv.ParseInt(args[2], 10, 32)
	if err != nil {
		return "", oracleFieldSpec{}, fmt.Errorf("%s:%d: parse %s nth: %w", path, lineNumber, args[0], err)
	}
	return strings.TrimPrefix(args[0], "sf"), oracleFieldSpec{
		rawType: args[1],
		nth:     int32(nth),
		flags:   strings.Join(args[3:], ","),
	}, nil
}

func splitOracleMacroArgs(body string) ([]string, error) {
	args := make([]string, 0, 4)
	start := 0
	depth := 0
	for index, character := range body {
		switch character {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("unbalanced macro arguments")
			}
		case ',':
			if depth == 0 {
				args = append(args, strings.TrimSpace(body[start:index]))
				start = index + 1
			}
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("unbalanced macro arguments")
	}
	args = append(args, strings.TrimSpace(body[start:]))
	return args, nil
}

func buildExpectedFields(types map[string]int32, specs map[string]oracleFieldSpec) (map[string]expectedField, error) {
	fields := make(map[string]expectedField, len(specs)+8)
	for name, spec := range specs {
		typeName := translateOracleType(spec.rawType)
		typeCode, found := types[typeName]
		if !found {
			return nil, fmt.Errorf("SField %q uses type %q absent from TYPES", name, typeName)
		}
		fields[name] = expectedField{
			info: FieldInfo{
				Nth:            spec.nth,
				IsVLEncoded:    spec.rawType == "VL" || spec.rawType == "ACCOUNT" || spec.rawType == "VECTOR256",
				IsSerialized:   typeCode < 10000,
				IsSigningField: spec.nth < 256 && !strings.Contains(spec.flags, "kNotSigning"),
				Type:           typeName,
			},
			header:  FieldHeader{TypeCode: typeCode, FieldCode: spec.nth},
			ordinal: typeCode<<16 | spec.nth,
		}
	}

	specials := []struct {
		name                      string
		typeName                  string
		nth                       int32
		isVLEncoded, isSerialized bool
		isSigningField            bool
	}{
		{"Generic", "Unknown", 0, false, true, true},
		{"Invalid", "Unknown", -1, false, false, false},
		{"ObjectEndMarker", "STObject", 1, false, true, true},
		{"ArrayEndMarker", "STArray", 1, false, true, true},
		{"hash", "Hash256", 257, false, false, false},
		{"index", "Hash256", 258, false, false, false},
		{"taker_gets_funded", "Amount", 258, false, false, false},
		{"taker_pays_funded", "Amount", 259, false, false, false},
	}
	for _, special := range specials {
		typeCode, found := types[special.typeName]
		if !found {
			return nil, fmt.Errorf("special field %q uses type %q absent from TYPES", special.name, special.typeName)
		}
		if _, duplicate := fields[special.name]; duplicate {
			return nil, fmt.Errorf("special field %q duplicates an SField macro", special.name)
		}
		fields[special.name] = expectedField{
			info: FieldInfo{
				Nth:            special.nth,
				IsVLEncoded:    special.isVLEncoded,
				IsSerialized:   special.isSerialized,
				IsSigningField: special.isSigningField,
				Type:           special.typeName,
			},
			header:  FieldHeader{TypeCode: typeCode, FieldCode: special.nth},
			ordinal: typeCode<<16 | special.nth,
		}
	}
	headers := make(map[FieldHeader]string, len(fields))
	for name, field := range fields {
		if previous, found := headers[field.header]; found {
			return nil, fmt.Errorf("fields %q and %q reuse header %+v", previous, name, field.header)
		}
		headers[field.header] = name
	}
	return fields, nil
}

func parseOracleIDs(path, prefix string, pattern *regexp.Regexp) (map[string]int32, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	types := make(map[string]int32)
	codes := make(map[int32]string)
	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		match := pattern.FindStringSubmatch(line)
		if match == nil {
			return nil, fmt.Errorf("%s:%d: unparsed protocol ID definition %q", path, lineNumber, line)
		}
		code, err := strconv.ParseInt(match[2], 0, 32)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: parse protocol ID: %w", path, lineNumber, err)
		}
		name := match[3]
		value := int32(code)
		if _, found := types[name]; found {
			return nil, fmt.Errorf("%s:%d: duplicate protocol name %q", path, lineNumber, name)
		}
		if previous, found := codes[value]; found {
			return nil, fmt.Errorf("%s:%d: protocol name %q reuses code %d from %q", path, lineNumber, name, value, previous)
		}
		types[name] = value
		codes[value] = name
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	return types, nil
}

func parseOracleTransactionResults(path string) (map[string]int32, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	relevantEnums := map[string]struct{}{
		"TELcodes": {},
		"TEMcodes": {},
		"TEFcodes": {},
		"TERcodes": {},
		"TEScodes": {},
		"TECcodes": {},
	}
	results := make(map[string]int32)
	codes := make(map[int32]string)
	seenEnums := make(map[string]bool)
	currentValue := int32(0)
	inEnum := false
	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if comment := strings.Index(line, "//"); comment >= 0 {
			line = strings.TrimSpace(line[:comment])
		}
		if !inEnum {
			match := oracleEnumLine.FindStringSubmatch(line)
			if match != nil {
				if _, relevant := relevantEnums[match[1]]; relevant {
					inEnum = true
					seenEnums[match[1]] = true
					currentValue = 0
				}
			}
			continue
		}
		if strings.HasPrefix(line, "};") {
			inEnum = false
			continue
		}
		if line == "" {
			continue
		}
		match := oracleTERLine.FindStringSubmatch(line)
		if match == nil {
			return nil, fmt.Errorf("%s:%d: unparsed TER definition %q", path, lineNumber, line)
		}
		value := currentValue + 1
		if match[2] != "" {
			parsed, err := strconv.ParseInt(match[2], 10, 32)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: parse TER code: %w", path, lineNumber, err)
			}
			value = int32(parsed)
		}
		name := match[1]
		if _, found := results[name]; found {
			return nil, fmt.Errorf("%s:%d: duplicate TER %q", path, lineNumber, name)
		}
		if previous, found := codes[value]; found {
			return nil, fmt.Errorf("%s:%d: TER %q reuses code %d from %q", path, lineNumber, name, value, previous)
		}
		results[name] = value
		codes[value] = name
		currentValue = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	if inEnum {
		return nil, fmt.Errorf("%s: unterminated TER enum", path)
	}
	for enumName := range relevantEnums {
		if !seenEnums[enumName] {
			return nil, fmt.Errorf("%s: missing TER enum %s", path, enumName)
		}
	}
	return results, nil
}

func compareFields(t *testing.T, got map[string]*FieldInstance, want map[string]expectedField) {
	t.Helper()
	actual := make(map[string]expectedField, len(got))
	for name, field := range got {
		if field == nil || field.FieldInfo == nil || field.FieldHeader == nil {
			t.Errorf("field %q is missing decoded metadata", name)
			continue
		}
		assert.Equal(t, name, field.FieldName)
		actual[name] = expectedField{info: *field.FieldInfo, header: *field.FieldHeader, ordinal: field.Ordinal}
	}
	assert.Equal(t, want, actual, "FIELDS")
}

func compareIntMaps(t *testing.T, label string, got, want map[string]int32) {
	t.Helper()
	assert.Equal(t, want, got, label)
}
