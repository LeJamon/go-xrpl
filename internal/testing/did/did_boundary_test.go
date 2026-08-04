package did_test

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/did"
)

func TestDIDSetFieldLengthBoundaries(t *testing.T) {
	fields := []struct {
		name  string
		set   func(*did.DIDSetBuilder, string)
		value func(*didEntryView) string
	}{
		{name: "URI", set: func(b *did.DIDSetBuilder, value string) { b.URI(value) }, value: func(v *didEntryView) string { return v.uri }},
		{name: "DIDDocument", set: func(b *did.DIDSetBuilder, value string) { b.Document(value) }, value: func(v *didEntryView) string { return v.document }},
		{name: "Data", set: func(b *did.DIDSetBuilder, value string) { b.Data(value) }, value: func(v *didEntryView) string { return v.data }},
	}

	for _, field := range fields {
		for _, length := range []int{256, 257} {
			t.Run(fmt.Sprintf("%s/%d", field.name, length), func(t *testing.T) {
				env := jtx.NewTestEnv(t)
				alice := jtx.NewAccount("alice")
				env.Fund(alice)
				env.Close()

				builder := did.DIDSet(alice)
				field.set(builder, strings.Repeat("a", length))
				result := env.Submit(builder.Build())
				if length == 257 {
					jtx.RequireTxFail(t, result, jtx.TemMALFORMED)
					require.Zero(t, env.OwnerCount(alice))
					requireDIDAbsent(t, env, alice)
					return
				}

				jtx.RequireTxSuccess(t, result)
				entry := getDIDEntry(t, env, alice)
				require.NotNil(t, entry)
				view := &didEntryView{uri: entry.URI, document: entry.DIDDocument, data: entry.Data}
				decoded, err := hex.DecodeString(field.value(view))
				require.NoError(t, err)
				require.Len(t, decoded, 256)
				require.Equal(t, uint32(1), env.OwnerCount(alice))
			})
		}
	}
}

type didEntryView struct {
	uri      string
	document string
	data     string
}
