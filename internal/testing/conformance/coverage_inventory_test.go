package conformance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
)

const (
	coverageInventorySchema = 1
	coverageOracleRepo      = "XRPLF/xrpld-private"
	coverageOracleTag       = "3.4.1"
	coverageOracleCommit    = "d147fccf54a500fce586522f28d6044c37fd8d29"
)

type coverageInventory struct {
	Schema       int                   `json:"schema"`
	Oracle       coverageOracle        `json:"oracle"`
	Counts       map[string]int        `json:"counts"`
	CommonFields coverageCommonFields  `json:"common_fields"`
	Amendments   []coverageAmendment   `json:"amendments"`
	Transactions []coverageTransaction `json:"transactions"`
}

type coverageOracle struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Commit     string `json:"commit"`
}

type coverageField struct {
	Name  string `json:"name"`
	Style string `json:"style"`
}

type coverageCommonFields struct {
	Oracle []coverageField `json:"oracle"`
	Go     []coverageField `json:"go"`
	Match  bool            `json:"match"`
}

type coverageAmendment struct {
	Name   string                 `json:"name"`
	Go     *coverageAmendmentSide `json:"go"`
	Oracle *coverageAmendmentSide `json:"oracle"`
	Status string                 `json:"status"`
}

type coverageAmendmentSide struct {
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	Supported *string `json:"supported"`
}

type coverageTransaction struct {
	Name       string                     `json:"name"`
	Code       uint16                     `json:"code"`
	GoFields   []coverageField            `json:"go_fields"`
	FieldMatch bool                       `json:"field_templates_match"`
	Status     map[string]json.RawMessage `json:"status"`
}

func readCoverageInventory(t *testing.T) coverageInventory {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed while locating coverage inventory")
	}
	path := filepath.Join(filepath.Dir(source), "../../../scripts/engine-coverage/inventory.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read coverage inventory %s: %v", path, err)
	}
	var inventory coverageInventory
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatalf("decode coverage inventory %s: %v", path, err)
	}
	return inventory
}

func TestEngineCoverageInventory(t *testing.T) {
	inventory := readCoverageInventory(t)
	if inventory.Schema != coverageInventorySchema {
		t.Fatalf("inventory schema = %d, want %d", inventory.Schema, coverageInventorySchema)
	}
	if inventory.Oracle.Repository != coverageOracleRepo || inventory.Oracle.Tag != coverageOracleTag || inventory.Oracle.Commit != coverageOracleCommit {
		t.Fatalf("inventory oracle = %s %s %s, want %s %s %s", inventory.Oracle.Repository, inventory.Oracle.Tag, inventory.Oracle.Commit, coverageOracleRepo, coverageOracleTag, coverageOracleCommit)
	}

	checkInventoryCounts(t, inventory)
	checkCommonFields(t, inventory.CommonFields)
	checkAmendmentRegistry(t, inventory.Amendments)
	checkTransactionRegistry(t, inventory.Transactions)
}

func checkInventoryCounts(t *testing.T, inventory coverageInventory) {
	t.Helper()
	want := map[string]int{
		"oracle_transactions":         82,
		"go_runtime_transaction_rows": 82,
		"go_legacy_enum_rows":         4,
		"oracle_amendments":           108,
		"go_amendments":               112,
		"go_only_amendments":          4,
		"common_fields":               20,
	}
	for name, expected := range want {
		if got := inventory.Counts[name]; got != expected {
			t.Errorf("inventory count %s = %d, want %d", name, got, expected)
		}
	}
	if len(inventory.Transactions) != inventory.Counts["oracle_transactions"] || len(inventory.Transactions) != inventory.Counts["go_runtime_transaction_rows"] {
		t.Errorf("inventory transaction rows = %d, counts report oracle/runtime = %d/%d", len(inventory.Transactions), inventory.Counts["oracle_transactions"], inventory.Counts["go_runtime_transaction_rows"])
	}
	if len(inventory.Amendments) != inventory.Counts["go_amendments"] {
		t.Errorf("inventory amendment rows = %d, count reports %d Go rows", len(inventory.Amendments), inventory.Counts["go_amendments"])
	}
}

func checkCommonFields(t *testing.T, fields coverageCommonFields) {
	t.Helper()
	if !fields.Match {
		t.Fatal("inventory marks Go/oracle common fields as different")
	}
	if len(fields.Oracle) != 20 || len(fields.Go) != 20 {
		t.Fatalf("common field counts = oracle %d/Go %d, want 20/20", len(fields.Oracle), len(fields.Go))
	}
	if !equalCoverageFields(fields.Oracle, fields.Go) {
		t.Fatal("inventory common field rows differ")
	}

	all.RegisterAll()
	actual := make([]coverageField, 0, len(tx.FormatCommonFields()))
	for _, field := range tx.FormatCommonFields() {
		actual = append(actual, coverageField{Name: field.Name, Style: coverageStyle(field.Style)})
	}
	if !equalCoverageFields(actual, fields.Go) {
		t.Fatalf("runtime common fields differ from inventory: got %v, want %v", actual, fields.Go)
	}
}

func checkAmendmentRegistry(t *testing.T, rows []coverageAmendment) {
	t.Helper()
	byName := make(map[string]coverageAmendment, len(rows))
	goCount, oracleCount, goOnlyCount := 0, 0, 0
	for _, row := range rows {
		if row.Name == "" {
			t.Error("inventory contains an amendment row without a name")
		}
		if _, exists := byName[row.Name]; exists {
			t.Errorf("duplicate amendment row %q", row.Name)
		}
		byName[row.Name] = row
		if row.Go == nil {
			t.Errorf("amendment %q has no Go registry row", row.Name)
		}
		if row.Status == "oracle_only" {
			t.Errorf("amendment %q is oracle-only", row.Name)
		}
		if row.Go != nil {
			goCount++
		}
		if row.Oracle != nil {
			oracleCount++
		}
		if row.Status == "go_only" {
			goOnlyCount++
		}
	}
	if got := goCount; got != 112 {
		t.Errorf("inventory Go amendment rows = %d, want 112", got)
	}
	if got := oracleCount; got != 108 {
		t.Errorf("inventory oracle amendment rows = %d, want 108", got)
	}
	if got := goOnlyCount; got != 4 {
		t.Errorf("inventory Go-only amendment rows = %d, want 4", got)
	}

	features := amendment.AllFeatures()
	if len(features) != len(rows) {
		t.Fatalf("runtime amendment count = %d, inventory count = %d", len(features), len(rows))
	}
	runtimeNames := make(map[string]bool, len(features))
	for _, feature := range features {
		if runtimeNames[feature.Name] {
			t.Errorf("runtime amendment %q is registered more than once", feature.Name)
		}
		runtimeNames[feature.Name] = true
		row, ok := byName[feature.Name]
		if !ok {
			t.Errorf("runtime amendment %q is absent from inventory", feature.Name)
			continue
		}
		if row.Go == nil {
			continue
		}
		if row.Go.Supported == nil {
			t.Errorf("amendment %q has no support status", feature.Name)
			continue
		}
		if *row.Go.Supported != "conditional" {
			want := "no"
			if feature.IsSupported() {
				want = "yes"
			}
			if *row.Go.Supported != want {
				t.Errorf("amendment %q support = %q, runtime is %q", feature.Name, *row.Go.Supported, want)
			}
		}
	}
}

func checkTransactionRegistry(t *testing.T, rows []coverageTransaction) {
	t.Helper()
	byCode := make(map[uint16]coverageTransaction, len(rows))
	byName := make(map[string]uint16, len(rows))
	for _, row := range rows {
		if _, exists := byCode[row.Code]; exists {
			t.Errorf("duplicate transaction code %d", row.Code)
		}
		if prior, exists := byName[row.Name]; exists {
			t.Errorf("duplicate transaction name %q at codes %d and %d", row.Name, prior, row.Code)
		}
		byCode[row.Code] = row
		byName[row.Name] = row.Code
		checkTransactionStatus(t, row)
	}

	all.RegisterAll()
	registered := tx.SupportedTypes()
	if len(registered) != len(rows) {
		t.Fatalf("runtime transaction count = %d, inventory count = %d", len(registered), len(rows))
	}
	seen := make(map[uint16]bool, len(registered))
	for _, txType := range registered {
		code := uint16(txType)
		if seen[code] {
			t.Errorf("runtime transaction code %d is registered more than once", code)
		}
		seen[code] = true
		row, ok := byCode[code]
		if !ok {
			t.Errorf("runtime transaction %s (%d) is absent from inventory", txType.String(), code)
			continue
		}
		if txType.String() != row.Name {
			t.Errorf("runtime transaction code %d name = %q, inventory = %q", code, txType.String(), row.Name)
		}
		instance, err := tx.NewFromType(txType)
		if err != nil {
			t.Errorf("construct runtime transaction %s (%d): %v", row.Name, code, err)
			continue
		}
		_, supported := instance.(tx.Appliable)
		if status := coverageStatusBool(t, row, "go_supported"); status != supported {
			t.Errorf("transaction %s go_supported = %t, runtime Appliable = %t", row.Name, status, supported)
		}
	}
	for code := range byCode {
		if !seen[code] {
			t.Errorf("inventory transaction code %d is absent from runtime registry", code)
		}
	}

	for name, fields := range tx.FormatTemplates() {
		code, ok := byName[name]
		if !ok {
			t.Errorf("runtime template %q is absent from inventory", name)
			continue
		}
		row := byCode[code]
		actual := make([]coverageField, 0, len(fields))
		for _, field := range fields {
			actual = append(actual, coverageField{Name: field.Name, Style: coverageStyle(field.Style)})
		}
		if !equalCoverageFields(actual, row.GoFields) || !row.FieldMatch {
			t.Errorf("transaction %s template differs from inventory", name)
		}
	}
}

func checkTransactionStatus(t *testing.T, row coverageTransaction) {
	t.Helper()
	for _, name := range []string{
		"oracle_registered",
		"go_registered",
		"go_supported",
		"pseudo",
		"conformance_excluded",
		"executed_in_checked_in_corpus",
		"oracle_comparison_executed",
	} {
		if _, ok := row.Status[name]; !ok {
			t.Errorf("transaction %s is missing status %q", row.Name, name)
		}
	}
	if !coverageStatusBool(t, row, "oracle_registered") || !coverageStatusBool(t, row, "go_registered") {
		t.Errorf("transaction %s is not registered on both sides", row.Name)
	}
	if coverageStatusBool(t, row, "oracle_comparison_executed") && !coverageStatusBool(t, row, "executed_in_checked_in_corpus") {
		t.Errorf("transaction %s claims an oracle comparison without executed corpus evidence", row.Name)
	}
	if coverageStatusBool(t, row, "executed_in_checked_in_corpus") || coverageStatusBool(t, row, "oracle_comparison_executed") {
		t.Errorf("base inventory transaction %s claims execution evidence", row.Name)
	}
	var exclusionReason *string
	raw, ok := row.Status["conformance_exclusion_reason"]
	if !ok {
		t.Errorf("transaction %s is missing status %q", row.Name, "conformance_exclusion_reason")
	} else if string(raw) != "null" {
		if err := json.Unmarshal(raw, &exclusionReason); err != nil {
			t.Errorf("transaction %s exclusion reason is not nullable string: %v", row.Name, err)
		}
	}
	if coverageStatusBool(t, row, "conformance_excluded") != (exclusionReason != nil && *exclusionReason != "") {
		t.Errorf("transaction %s exclusion status/reason disagree", row.Name)
	}
}

func coverageStatusBool(t *testing.T, row coverageTransaction, name string) bool {
	t.Helper()
	raw, ok := row.Status[name]
	if !ok {
		return false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Errorf("transaction %s status %s is not boolean: %v", row.Name, name, err)
	}
	return value
}

func coverageStyle(style int) string {
	switch style {
	case 0:
		return "required"
	case 1:
		return "optional"
	case 2:
		return "default"
	default:
		return fmt.Sprintf("unknown(%d)", style)
	}
}

func equalCoverageFields(left, right []coverageField) bool {
	return len(left) == len(right) && func() bool {
		for index := range left {
			if left[index] != right[index] {
				return false
			}
		}
		return true
	}()
}
