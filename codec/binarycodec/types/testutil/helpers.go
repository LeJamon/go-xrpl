package testutil

import (
	"testing"

	"github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"
)

// GetFieldInstance returns a FieldInstance for the given field name.
// It panics if the field is not found.
func GetFieldInstance(t *testing.T, fieldName string) definitions.FieldInstance {
	t.Helper()
	fi, err := definitions.Get().GetFieldInstanceByFieldName(fieldName)
	if err != nil {
		t.Fatalf("failed to get field instance for %s: %v", fieldName, err)
	}
	return *fi
}
