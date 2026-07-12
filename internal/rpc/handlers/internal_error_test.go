package handlers

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/stretchr/testify/require"
)

func TestRPCInternalErrorSanitizesCause(t *testing.T) {
	original := xrpllog.Root()
	t.Cleanup(func() { xrpllog.SetRoot(original) })

	var logs bytes.Buffer
	cfg := &xrpllog.Config{Level: xrpllog.LevelError, Format: "json", Output: &logs}
	xrpllog.SetRoot(xrpllog.New(xrpllog.NewHandler(cfg), cfg))

	rpcErr := rpcInternalError("test operation failed", errors.New("backend detail"))

	require.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	require.Equal(t, "Internal error.", rpcErr.Message)
	require.NotContains(t, rpcErr.Message, "backend detail")
	require.Contains(t, logs.String(), "test operation failed")
	require.Contains(t, logs.String(), "backend detail")
	require.Contains(t, logs.String(), xrpllog.PartitionRPC)
}

func TestRPCErrorInternalMessagesAreLiterals(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	require.True(t, ok)

	dir := filepath.Dir(testFile)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, 0)
		require.NoError(t, err)

		typeAliases := make(map[string]struct{})
		dotImported := false
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			require.NoError(t, err)
			if importPath != "github.com/LeJamon/go-xrpl/internal/rpc/types" {
				continue
			}
			if spec.Name == nil {
				typeAliases["types"] = struct{}{}
			} else if spec.Name.Name == "." {
				dotImported = true
			} else if spec.Name.Name != "_" {
				typeAliases[spec.Name.Name] = struct{}{}
			}
		}

		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}

			isInternalError := false
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				ident, ok := fun.X.(*ast.Ident)
				if ok && fun.Sel.Name == "RpcErrorInternal" {
					_, isInternalError = typeAliases[ident.Name]
				}
			case *ast.Ident:
				isInternalError = dotImported && fun.Name == "RpcErrorInternal"
			}
			if !isInternalError {
				return true
			}

			if len(call.Args) != 1 {
				t.Errorf(
					"%s: RpcErrorInternal requires exactly one literal message",
					fset.Position(call.Pos()),
				)
				return true
			}
			literal, isLiteral := call.Args[0].(*ast.BasicLit)
			if !isLiteral || literal.Kind != token.STRING {
				t.Errorf(
					"%s: RpcErrorInternal message must be a string literal; log runtime errors with rpcInternalError",
					fset.Position(call.Pos()),
				)
			}
			return true
		})
	}
}
