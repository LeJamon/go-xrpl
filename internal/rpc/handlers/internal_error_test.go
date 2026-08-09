package handlers

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	gotypes "go/types"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

func TestRPCInternalErrorSanitizesCause(t *testing.T) {
	logs := captureRpcErrorLogs(t)

	rpcErr := rpcInternalError("test operation failed", errors.New("backend detail"))

	require.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	require.Equal(t, "Internal error.", rpcErr.Message)
	require.NotContains(t, rpcErr.Message, "backend detail")
	require.Contains(t, logs.String(), "test operation failed")
	require.Contains(t, logs.String(), "backend detail")
	require.Contains(t, logs.String(), xrpllog.PartitionRPC)
}

func TestRPCInternalInvariantErrorIsCanonicalAndLogged(t *testing.T) {
	logs := captureRpcErrorLogs(t)

	rpcErr := rpcInternalInvariantError("test invariant failed")

	require.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	require.Equal(t, "internal", rpcErr.ErrorString)
	require.Equal(t, "Internal error.", rpcErr.Message)
	require.Contains(t, logs.String(), "test invariant failed")
}

func TestRPCTransactionSubmissionErrorPreservesFixedMessage(t *testing.T) {
	logs := captureRpcErrorLogs(t)
	cause := errors.New("backend submission detail")

	rpcErr := rpcTransactionSubmissionError("test submission failed", cause)

	require.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	require.Equal(t, "internal", rpcErr.ErrorString)
	require.Equal(t, "Exception occurred during transaction submission.", rpcErr.Message)
	require.NotContains(t, rpcErr.Message, cause.Error())
	require.Contains(t, logs.String(), "test submission failed")
	require.Contains(t, logs.String(), cause.Error())
}

func TestRPCDBDeserializationErrorIsCanonicalAndLogged(t *testing.T) {
	logs := captureRpcErrorLogs(t)
	cause := errors.New("corrupt stored transaction")

	rpcErr := rpcDBDeserializationError("test transaction decode failed", cause)

	require.Equal(t, types.RpcDB_DESERIALIZATION, rpcErr.Code)
	require.Equal(t, "dbDeserialization", rpcErr.ErrorString)
	require.Equal(t, "Database deserialization error.", rpcErr.Message)
	require.NotContains(t, rpcErr.Message, cause.Error())
	require.Contains(t, logs.String(), "test transaction decode failed")
	require.Contains(t, logs.String(), cause.Error())
}

func captureRpcErrorLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	original := xrpllog.Root()
	t.Cleanup(func() { xrpllog.SetRoot(original) })

	logs := &bytes.Buffer{}
	cfg := &xrpllog.Config{Level: xrpllog.LevelError, Format: "json", Output: logs}
	xrpllog.SetRoot(xrpllog.New(xrpllog.NewHandler(cfg), cfg))
	return logs
}

func TestRPCInternalErrorConstructionsAreSafe(t *testing.T) {
	moduleRoot := rpcInternalAuditModuleRoot(t)
	loaded, err := loadRPCInternalAuditPackages(moduleRoot, "./...")
	require.NoError(t, err)
	require.Empty(t, auditRPCInternalErrors(loaded))
}

func TestRPCInternalErrorAuditFixtures(t *testing.T) {
	moduleRoot := rpcInternalAuditModuleRoot(t)
	tests := []struct {
		name        string
		fixture     string
		wantFinding string
	}{
		{name: "safe constructors and shadowing", fixture: "safe"},
		{name: "direct constructor", fixture: "unsafe_direct", wantFinding: "code 73 construction outside approved constructors"},
		{name: "constructor alias", fixture: "unsafe_alias", wantFinding: "NewRpcError may only be called directly"},
		{name: "constructor wrapper", fixture: "unsafe_wrapper", wantFinding: "NewRpcError code must be a compile-time constant"},
		{name: "constructor reassignment", fixture: "unsafe_reassignment", wantFinding: "NewRpcError may only be called directly"},
		{name: "composite literal", fixture: "unsafe_composite", wantFinding: "RpcError composite literal with code 73"},
		{name: "dynamic composite literal", fixture: "unsafe_dynamic_composite", wantFinding: "RpcError composite literal code must be a compile-time constant"},
		{name: "field mutation", fixture: "unsafe_mutation", wantFinding: "RpcError fields must not be mutated"},
		{name: "promoted field mutation", fixture: "unsafe_promoted_mutation", wantFinding: "RpcError fields must not be mutated"},
		{name: "range field mutation", fixture: "unsafe_range_mutation", wantFinding: "RpcError fields must not be mutated"},
		{name: "field address", fixture: "unsafe_address", wantFinding: "RpcError fields must not be addressable for mutation"},
		{name: "indirect field mutation", fixture: "unsafe_indirect_mutation", wantFinding: "RpcError fields must not be mutated"},
		{name: "whole value mutation", fixture: "unsafe_whole_value", wantFinding: "RpcError values must not be mutated"},
		{name: "defined type", fixture: "unsafe_defined_type", wantFinding: "RpcError composite literal with code 73"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pattern := "./internal/rpc/handlers/testdata/internal_error_audit/" + tc.fixture
			loaded, err := loadRPCInternalAuditPackages(moduleRoot, pattern)
			require.NoError(t, err)
			findings := auditRPCInternalErrors(loaded)
			if tc.wantFinding == "" {
				require.Empty(t, findings)
				return
			}
			require.NotEmpty(t, findings)
			require.Contains(t, strings.Join(findings, "\n"), tc.wantFinding)
			for _, finding := range findings {
				require.Contains(t, finding, "fixture.go:")
			}
		})
	}
}

func TestRPCFunctionIdentityRejectsMethods(t *testing.T) {
	pkg := gotypes.NewPackage(rpcTypesPackagePath, "types")
	params := gotypes.NewTuple(gotypes.NewVar(token.NoPos, pkg, "code", gotypes.Typ[gotypes.Int]))
	result := gotypes.NewTuple(gotypes.NewVar(token.NoPos, pkg, "result", gotypes.NewPointer(gotypes.Typ[gotypes.Int])))
	function := gotypes.NewFunc(token.NoPos, pkg, "NewRpcError", gotypes.NewSignatureType(nil, nil, nil, params, result, false))
	pkg.Scope().Insert(function)
	receiverType := gotypes.NewNamed(gotypes.NewTypeName(token.NoPos, pkg, "receiver", nil), gotypes.NewStruct(nil, nil), nil)
	receiver := gotypes.NewVar(token.NoPos, pkg, "", receiverType)
	method := gotypes.NewFunc(token.NoPos, pkg, "NewRpcError", gotypes.NewSignatureType(receiver, nil, nil, params, result, false))

	require.True(t, isRPCFunction(function, "NewRpcError"))
	require.False(t, isRPCFunction(method, "NewRpcError"))
}

const rpcTypesPackagePath = "github.com/LeJamon/go-xrpl/internal/rpc/types"

type rpcInternalErrorAuditor struct {
	findings []string
	reported map[string]struct{}
}

func rpcInternalAuditModuleRoot(t *testing.T) string {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", "..", ".."))
}

func loadRPCInternalAuditPackages(moduleRoot string, patterns ...string) ([]*packages.Package, error) {
	loaded, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo |
			packages.NeedImports | packages.NeedDeps,
		Dir:   moduleRoot,
		Tests: false,
	}, patterns...)
	if err != nil {
		return nil, err
	}
	var packageErrors []string
	for _, pkg := range loaded {
		for _, loadErr := range pkg.Errors {
			packageErrors = append(packageErrors, loadErr.Error())
		}
	}
	if len(packageErrors) > 0 {
		return nil, fmt.Errorf("load RPC packages: %s", strings.Join(packageErrors, "; "))
	}
	return loaded, nil
}

func auditRPCInternalErrors(loaded []*packages.Package) []string {
	audit := &rpcInternalErrorAuditor{
		reported: make(map[string]struct{}),
	}
	for _, pkg := range loaded {
		for _, file := range pkg.Syntax {
			audit.scanFile(pkg, file)
		}
	}
	return audit.findings
}

func (a *rpcInternalErrorAuditor) scanFile(pkg *packages.Package, file *ast.File) {
	parents := make(map[ast.Node]ast.Node)
	var stack []ast.Node
	ast.Inspect(file, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if len(stack) != 0 {
			parents[node] = stack[len(stack)-1]
		}
		stack = append(stack, node)
		return true
	})

	approvedCalls := a.approvedInternalConstructorCalls(pkg, file)
	approvedLiterals := a.approvedRpcErrorFactoryLiterals(pkg, file)
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.CallExpr:
			if !isRPCFunction(auditUsedFunction(pkg, node.Fun), "NewRpcError") {
				break
			}
			if _, approved := approvedCalls[node]; approved {
				break
			}
			if len(node.Args) == 0 || pkg.TypesInfo.Types[node.Args[0]].Value == nil {
				a.report(pkg, node, "NewRpcError code must be a compile-time constant")
				break
			}
			if isRPCInternalConstant(pkg.TypesInfo.Types[node.Args[0]].Value) {
				a.report(pkg, node, "RPC code 73 construction outside approved constructors")
			}
		case *ast.SelectorExpr:
			if isRPCFunction(auditUsedFunction(pkg, node), "NewRpcError") && !isDirectCallTarget(node, parents) {
				a.report(pkg, node, "NewRpcError may only be called directly")
			}
		case *ast.Ident:
			if selector, ok := parents[node].(*ast.SelectorExpr); ok && selector.Sel == node {
				break
			}
			if isRPCFunction(auditUsedFunction(pkg, node), "NewRpcError") && !isDirectCallTarget(node, parents) {
				a.report(pkg, node, "NewRpcError may only be called directly")
			}
		case *ast.CompositeLit:
			if !isRpcErrorType(pkg.TypesInfo.TypeOf(node)) {
				break
			}
			if _, approved := approvedLiterals[node]; approved {
				break
			}
			value := rpcErrorLiteralCode(pkg, node)
			if value == nil {
				a.report(pkg, node, "RpcError composite literal code must be a compile-time constant")
			} else if isRPCInternalConstant(value) {
				a.report(pkg, node, "RpcError composite literal with code 73 is forbidden")
			}
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if a.isRpcErrorField(pkg, lhs) {
					a.report(pkg, lhs, "RpcError fields must not be mutated")
				} else if isRpcErrorMutationTarget(pkg, lhs) {
					a.report(pkg, lhs, "RpcError values must not be mutated")
				}
			}
		case *ast.IncDecStmt:
			if a.isRpcErrorField(pkg, node.X) {
				a.report(pkg, node.X, "RpcError fields must not be mutated")
			}
		case *ast.RangeStmt:
			if node.Tok == token.ASSIGN {
				for _, field := range []ast.Expr{node.Key, node.Value} {
					if field != nil && a.isRpcErrorField(pkg, field) {
						a.report(pkg, field, "RpcError fields must not be mutated")
					}
				}
			}
		case *ast.UnaryExpr:
			if node.Op.String() == "&" && a.isRpcErrorField(pkg, node.X) {
				a.report(pkg, node.X, "RpcError fields must not be addressable for mutation")
			}
		}
		return true
	})
}

func (a *rpcInternalErrorAuditor) approvedInternalConstructorCalls(pkg *packages.Package, file *ast.File) map[*ast.CallExpr]struct{} {
	approved := make(map[*ast.CallExpr]struct{})
	if !a.isErrorsImplementation(pkg, file) {
		return approved
	}
	seen := make(map[string]bool)
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if function.Name.Name == "RpcErrorInvalidTransactionType" {
			call, exact := exactInvalidTransactionTypeConstructor(pkg, function)
			if !exact {
				a.report(pkg, function, "RpcErrorInvalidTransactionType must remain the constrained transaction-type constructor")
				continue
			}
			approved[call] = struct{}{}
			continue
		}
		message, fixed := fixedInternalConstructorMessage(function.Name.Name)
		if !fixed {
			continue
		}
		seen[function.Name.Name] = true
		call, exact := exactInternalConstructorCall(pkg, function, message)
		if !exact {
			a.report(pkg, function, function.Name.Name+" must remain an exact no-argument fixed RPC error constructor")
			continue
		}
		approved[call] = struct{}{}
	}
	for _, name := range []string{"RpcErrorInternal", "RpcErrorTransactionSubmission"} {
		if !seen[name] {
			a.report(pkg, file, name+" must remain an exact no-argument fixed RPC error constructor")
		}
	}
	return approved
}

func exactInvalidTransactionTypeConstructor(pkg *packages.Package, function *ast.FuncDecl) (*ast.CallExpr, bool) {
	object, _ := pkg.TypesInfo.Defs[function.Name].(*gotypes.Func)
	if object == nil {
		return nil, false
	}
	signature, _ := object.Type().(*gotypes.Signature)
	if signature == nil || function.Recv != nil || function.Type.TypeParams != nil ||
		signature.Params().Len() != 1 || !isBasicType(signature.Params().At(0).Type(), gotypes.Uint16) ||
		signature.Results().Len() != 1 || !isRpcErrorPointer(signature.Results().At(0).Type()) ||
		function.Body == nil || len(function.Body.List) != 1 {
		return nil, false
	}
	result, ok := function.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(result.Results) != 1 {
		return nil, false
	}
	call, ok := unparen(result.Results[0]).(*ast.CallExpr)
	if !ok || !isRPCFunction(auditUsedFunction(pkg, call.Fun), "NewRpcError") || len(call.Args) != 4 ||
		!isRPCInternalConstant(pkg.TypesInfo.Types[call.Args[0]].Value) ||
		!isStringConstant(pkg, call.Args[1], "internal") ||
		!isStringConstant(pkg, call.Args[2], "internal") {
		return nil, false
	}
	messageCall, ok := unparen(call.Args[3]).(*ast.CallExpr)
	if !ok || len(messageCall.Args) != 2 || !isNamedFunction(auditUsedFunction(pkg, messageCall.Fun), "fmt", "Sprintf") ||
		!isStringConstant(pkg, messageCall.Args[0], "Exception while serializing transaction: Invalid transaction type %d") {
		return nil, false
	}
	argument, ok := unparen(messageCall.Args[1]).(*ast.Ident)
	return call, ok && pkg.TypesInfo.Uses[argument] == signature.Params().At(0)
}

func isNamedFunction(function *gotypes.Func, packagePath, name string) bool {
	return function != nil && function.Pkg() != nil && function.Pkg().Path() == packagePath && function.Name() == name
}

func (a *rpcInternalErrorAuditor) approvedRpcErrorFactoryLiterals(pkg *packages.Package, file *ast.File) map[*ast.CompositeLit]struct{} {
	approved := make(map[*ast.CompositeLit]struct{})
	if !a.isErrorsImplementation(pkg, file) {
		return approved
	}
	found := false
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "NewRpcError" {
			continue
		}
		object := auditDefinedFunction(pkg, function.Name)
		if !isRPCFunction(object, "NewRpcError") {
			continue
		}
		found = true
		literal, exact := exactRpcErrorFactory(pkg, function, object)
		if !exact {
			a.report(pkg, function, "NewRpcError must remain an exact field-copy factory")
			continue
		}
		approved[literal] = struct{}{}
	}
	if !found {
		a.report(pkg, file, "NewRpcError must remain an exact field-copy factory")
	}
	return approved
}

func exactRpcErrorFactory(pkg *packages.Package, function *ast.FuncDecl, object *gotypes.Func) (*ast.CompositeLit, bool) {
	signature, _ := object.Type().(*gotypes.Signature)
	if signature == nil || signature.Recv() != nil || function.Recv != nil || function.Type.TypeParams != nil ||
		signature.Variadic() || signature.Params().Len() != 4 || signature.Results().Len() != 1 ||
		!isBasicType(signature.Params().At(0).Type(), gotypes.Int) ||
		!isBasicType(signature.Params().At(1).Type(), gotypes.String) ||
		!isBasicType(signature.Params().At(2).Type(), gotypes.String) ||
		!isBasicType(signature.Params().At(3).Type(), gotypes.String) ||
		!isRpcErrorPointer(signature.Results().At(0).Type()) || function.Body == nil || len(function.Body.List) != 1 {
		return nil, false
	}
	result, ok := function.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(result.Results) != 1 {
		return nil, false
	}
	address, ok := unparen(result.Results[0]).(*ast.UnaryExpr)
	if !ok || address.Op != token.AND {
		return nil, false
	}
	literal, ok := unparen(address.X).(*ast.CompositeLit)
	if !ok || !isRpcErrorType(pkg.TypesInfo.TypeOf(literal)) || len(literal.Elts) != 4 {
		return nil, false
	}
	expected := map[string]*gotypes.Var{
		"Code":        signature.Params().At(0),
		"ErrorString": signature.Params().At(1),
		"Type":        signature.Params().At(2),
		"Message":     signature.Params().At(3),
	}
	seen := make(map[string]bool, len(expected))
	for _, element := range literal.Elts {
		keyed, ok := element.(*ast.KeyValueExpr)
		if !ok {
			return nil, false
		}
		key, ok := keyed.Key.(*ast.Ident)
		if !ok || seen[key.Name] {
			return nil, false
		}
		parameter, ok := expected[key.Name]
		if !ok {
			return nil, false
		}
		value, ok := unparen(keyed.Value).(*ast.Ident)
		if !ok || pkg.TypesInfo.Uses[value] != parameter {
			return nil, false
		}
		seen[key.Name] = true
	}
	return literal, len(seen) == len(expected)
}

func isBasicType(value gotypes.Type, kind gotypes.BasicKind) bool {
	basic, ok := gotypes.Unalias(value).(*gotypes.Basic)
	return ok && basic.Kind() == kind
}

func fixedInternalConstructorMessage(name string) (string, bool) {
	switch name {
	case "RpcErrorInternal":
		return "Internal error.", true
	case "RpcErrorTransactionSubmission":
		return "Exception occurred during transaction submission.", true
	default:
		return "", false
	}
}

func exactInternalConstructorCall(pkg *packages.Package, function *ast.FuncDecl, message string) (*ast.CallExpr, bool) {
	object, _ := pkg.TypesInfo.Defs[function.Name].(*gotypes.Func)
	if object == nil {
		return nil, false
	}
	signature, _ := object.Type().(*gotypes.Signature)
	if signature == nil || function.Recv != nil || function.Type.TypeParams != nil ||
		signature.Params().Len() != 0 || signature.Results().Len() != 1 ||
		!isRpcErrorPointer(signature.Results().At(0).Type()) || function.Body == nil || len(function.Body.List) != 1 {
		return nil, false
	}
	result, ok := function.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(result.Results) != 1 {
		return nil, false
	}
	call, ok := unparen(result.Results[0]).(*ast.CallExpr)
	if !ok || !isRPCFunction(auditUsedFunction(pkg, call.Fun), "NewRpcError") || len(call.Args) != 4 {
		return nil, false
	}
	return call, isRPCInternalConstant(pkg.TypesInfo.Types[call.Args[0]].Value) &&
		isStringConstant(pkg, call.Args[1], "internal") &&
		isStringConstant(pkg, call.Args[2], "internal") &&
		isStringConstant(pkg, call.Args[3], message)
}

func isStringConstant(pkg *packages.Package, expression ast.Expr, expected string) bool {
	value := pkg.TypesInfo.Types[expression].Value
	return value != nil && value.Kind() == constant.String && constant.StringVal(value) == expected
}

func rpcErrorLiteralCode(pkg *packages.Package, literal *ast.CompositeLit) constant.Value {
	for _, element := range literal.Elts {
		if keyed, ok := element.(*ast.KeyValueExpr); ok {
			if field, ok := keyed.Key.(*ast.Ident); ok && field.Name == "Code" {
				return pkg.TypesInfo.Types[keyed.Value].Value
			}
		}
	}

	structure, _ := rpcErrorStruct(pkg.TypesInfo.TypeOf(literal))
	if structure == nil {
		return nil
	}
	for i := 0; i < structure.NumFields() && i < len(literal.Elts); i++ {
		if structure.Field(i).Name() == "Code" {
			return pkg.TypesInfo.Types[literal.Elts[i]].Value
		}
	}
	return constant.MakeInt64(0)
}

func (a *rpcInternalErrorAuditor) isRpcErrorField(pkg *packages.Package, expr ast.Expr) bool {
	selector, ok := unparen(expr).(*ast.SelectorExpr)
	if !ok {
		return false
	}
	selection := pkg.TypesInfo.Selections[selector]
	if selection == nil {
		return false
	}
	selected, ok := selection.Obj().(*gotypes.Var)
	if !ok {
		return false
	}
	if !selected.IsField() || selected.Pkg() == nil || selected.Pkg().Path() != rpcTypesPackagePath {
		return false
	}
	switch selected.Name() {
	case "Code", "ErrorString", "Type", "Message", "ErrorException":
		return true
	default:
		return false
	}
}

func isRpcErrorMutationTarget(pkg *packages.Package, expr ast.Expr) bool {
	expr = unparen(expr)
	switch expr.(type) {
	case *ast.StarExpr, *ast.IndexExpr:
		return isRpcErrorType(pkg.TypesInfo.TypeOf(expr))
	default:
		return false
	}
}

func (a *rpcInternalErrorAuditor) isErrorsImplementation(pkg *packages.Package, node ast.Node) bool {
	return pkg.PkgPath == rpcTypesPackagePath && filepath.Base(pkg.Fset.Position(node.Pos()).Filename) == "errors.go"
}

func (a *rpcInternalErrorAuditor) report(pkg *packages.Package, node ast.Node, message string) {
	finding := fmt.Sprintf("%s: %s", pkg.Fset.Position(node.Pos()), message)
	if _, exists := a.reported[finding]; exists {
		return
	}
	a.reported[finding] = struct{}{}
	a.findings = append(a.findings, finding)
}

func auditUsedFunction(pkg *packages.Package, expr ast.Expr) *gotypes.Func {
	switch expr := unparen(expr).(type) {
	case *ast.Ident:
		function, _ := pkg.TypesInfo.Uses[expr].(*gotypes.Func)
		return function
	case *ast.SelectorExpr:
		function, _ := pkg.TypesInfo.Uses[expr.Sel].(*gotypes.Func)
		return function
	default:
		return nil
	}
}

func auditDefinedFunction(pkg *packages.Package, identifier *ast.Ident) *gotypes.Func {
	function, _ := pkg.TypesInfo.Defs[identifier].(*gotypes.Func)
	return function
}

func isDirectCallTarget(node ast.Node, parents map[ast.Node]ast.Node) bool {
	for {
		parent := parents[node]
		parenthesized, ok := parent.(*ast.ParenExpr)
		if !ok {
			call, ok := parent.(*ast.CallExpr)
			return ok && call.Fun == node
		}
		node = parenthesized
	}
}

func unparen(expr ast.Expr) ast.Expr {
	for {
		parenthesized, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = parenthesized.X
	}
}

func isRPCFunction(function *gotypes.Func, name string) bool {
	if function == nil || function.Name() != name || function.Pkg() == nil || function.Pkg().Path() != rpcTypesPackagePath || function.Parent() != function.Pkg().Scope() {
		return false
	}
	signature, _ := function.Type().(*gotypes.Signature)
	return signature != nil && signature.Recv() == nil
}

func isRpcErrorType(value gotypes.Type) bool {
	if value == nil {
		return false
	}
	value = gotypes.Unalias(value)
	if pointer, ok := value.(*gotypes.Pointer); ok {
		value = gotypes.Unalias(pointer.Elem())
	}
	named, ok := value.(*gotypes.Named)
	if !ok {
		return false
	}
	if named.Obj().Name() == "RpcError" && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == rpcTypesPackagePath {
		return true
	}
	structure, ok := named.Underlying().(*gotypes.Struct)
	return ok && isRpcErrorStructShape(structure)
}

func isRpcErrorStructShape(structure *gotypes.Struct) bool {
	fields := []struct {
		name    string
		kind    gotypes.BasicKind
		tag     string
		mapType bool
	}{
		{name: "Code", kind: gotypes.Int, tag: `json:"error_code"`},
		{name: "ErrorString", kind: gotypes.String, tag: `json:"error"`},
		{name: "Type", kind: gotypes.String, tag: `json:"-"`},
		{name: "Message", kind: gotypes.String, tag: `json:"error_message,omitempty"`},
		{name: "ErrorException", kind: gotypes.String, tag: `json:"error_exception,omitempty"`},
		{name: "Extra", tag: `json:"-"`, mapType: true},
		{name: "bareToken", kind: gotypes.Bool},
		{name: "invalidApiVersion", kind: gotypes.Bool},
		{name: "overloaded", kind: gotypes.Bool},
	}
	if structure.NumFields() != len(fields) {
		return false
	}
	for i, expected := range fields {
		field := structure.Field(i)
		if field.Name() != expected.name || structure.Tag(i) != expected.tag {
			return false
		}
		if expected.mapType {
			mapType, ok := field.Type().(*gotypes.Map)
			if !ok || !isBasicType(mapType.Key(), gotypes.String) {
				return false
			}
			value, ok := mapType.Elem().Underlying().(*gotypes.Interface)
			if !ok || !value.Empty() {
				return false
			}
		} else if !isBasicType(field.Type(), expected.kind) {
			return false
		}
	}
	return true
}

func isRpcErrorPointer(value gotypes.Type) bool {
	if value == nil {
		return false
	}
	pointer, ok := gotypes.Unalias(value).(*gotypes.Pointer)
	return ok && isRpcErrorType(pointer.Elem())
}

func rpcErrorStruct(value gotypes.Type) (*gotypes.Struct, bool) {
	if value == nil {
		return nil, false
	}
	value = gotypes.Unalias(value)
	if pointer, ok := value.(*gotypes.Pointer); ok {
		value = gotypes.Unalias(pointer.Elem())
	}
	structure, ok := value.Underlying().(*gotypes.Struct)
	return structure, ok
}

func isRPCInternalConstant(value constant.Value) bool {
	if value == nil {
		return false
	}
	integer := constant.ToInt(value)
	if integer.Kind() == constant.Unknown {
		return false
	}
	number, ok := constant.Int64Val(integer)
	return ok && number == 73
}
