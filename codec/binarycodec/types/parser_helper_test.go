package types

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

// testParser returns a real BinaryParser over data, bound to the global
// definitions — the test stand-in for the deleted gomock parser.
func testParser(data []byte) *serdes.BinaryParser {
	return serdes.NewBinaryParser(data, definitions.Get())
}

// getFieldInstance returns a FieldInstance for the given field name,
// failing the test if the field is not found.
func getFieldInstance(t *testing.T, fieldName string) definitions.FieldInstance {
	t.Helper()
	fi, err := definitions.Get().GetFieldInstanceByFieldName(fieldName)
	if err != nil {
		t.Fatalf("failed to get field instance for %s: %v", fieldName, err)
	}
	return *fi
}
