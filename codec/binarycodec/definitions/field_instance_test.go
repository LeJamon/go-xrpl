package definitions

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefinitions_CreateFieldHeader(t *testing.T) {
	loadDefinitions()
	require.Equal(t, FieldHeader{
		TypeCode:  1,
		FieldCode: 2,
	}, Get().CreateFieldHeader(1, 2))
}

func TestFieldInstanceMap_CodecDecodeSelf(t *testing.T) {
	loadDefinitions()
}
