package credential

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

func TestSerializeCredentialSubjectNodePresence(t *testing.T) {
	issuer := [20]byte{0x01}
	otherSubject := [20]byte{0x02}

	t.Run("self-issued omits SubjectNode", func(t *testing.T) {
		data, err := serializeCredentialEntry(&CredentialEntry{
			Subject:        issuer,
			Issuer:         issuer,
			CredentialType: []byte{0xab, 0xcd},
			IssuerNode:     0,
			SubjectNode:    0,
			HasSubjectNode: true,
			Flags:          LsfCredentialAccepted,
		})
		require.NoError(t, err)

		fields, err := binarycodec.DecodeBytes(data)
		require.NoError(t, err)
		require.Equal(t, "0", fields["IssuerNode"])
		require.NotContains(t, fields, "SubjectNode")

		parsed, err := ParseCredentialEntry(data)
		require.NoError(t, err)
		require.False(t, parsed.HasSubjectNode)
	})

	t.Run("cross-account preserves page zero", func(t *testing.T) {
		data, err := serializeCredentialEntry(&CredentialEntry{
			Subject:        otherSubject,
			Issuer:         issuer,
			CredentialType: []byte{0xab, 0xcd},
			IssuerNode:     0,
			SubjectNode:    0,
			HasSubjectNode: true,
		})
		require.NoError(t, err)

		fields, err := binarycodec.DecodeBytes(data)
		require.NoError(t, err)
		require.Equal(t, "0", fields["SubjectNode"])

		parsed, err := ParseCredentialEntry(data)
		require.NoError(t, err)
		require.True(t, parsed.HasSubjectNode)
		require.Zero(t, parsed.SubjectNode)

		reencoded, err := serializeCredentialEntry(parsed)
		require.NoError(t, err)
		require.Equal(t, data, reencoded)
	})
}

func TestSerializeCredentialGeneratedPathMatchesReference(t *testing.T) {
	expiration := uint32(0)
	cred := &CredentialEntry{
		Subject:           [20]byte{0x02, 0x22},
		Issuer:            [20]byte{0x01, 0x11},
		CredentialType:    []byte{0xab, 0xcd, 0xef},
		Expiration:        &expiration,
		URI:               []byte("credential"),
		Flags:             LsfCredentialAccepted,
		IssuerNode:        7,
		SubjectNode:       0,
		HasSubjectNode:    true,
		PreviousTxnID:     [32]byte{0xaa, 0xbb},
		PreviousTxnLgrSeq: 0,
	}

	got, err := serializeCredentialEntry(cred)
	require.NoError(t, err)
	want := referenceCredentialBytes(t, cred)
	require.Equal(t, want, got)

	const wantHex = "110081220001000025000000002a00000000301b0000000000000007301c000000000000000055aabb000000000000000000000000000000000000000000000000000000000000750a63726564656e7469616c701f03abcdef841401110000000000000000000000000000000000008018140222000000000000000000000000000000000000"
	require.Equal(t, wantHex, hex.EncodeToString(got))

	parsed, err := ParseCredentialEntry(got)
	require.NoError(t, err)
	require.Equal(t, cred.Subject, parsed.Subject)
	require.Equal(t, cred.Issuer, parsed.Issuer)
	require.Equal(t, cred.CredentialType, parsed.CredentialType)
	require.Equal(t, cred.Expiration, parsed.Expiration)
	require.Equal(t, cred.URI, parsed.URI)
	require.Equal(t, cred.Flags, parsed.Flags)
	require.Equal(t, cred.IssuerNode, parsed.IssuerNode)
	require.Equal(t, cred.SubjectNode, parsed.SubjectNode)
	require.Equal(t, cred.HasSubjectNode, parsed.HasSubjectNode)
	require.Equal(t, cred.PreviousTxnID, parsed.PreviousTxnID)
	require.Equal(t, cred.PreviousTxnLgrSeq, parsed.PreviousTxnLgrSeq)
	reencoded, err := serializeCredentialEntry(parsed)
	require.NoError(t, err)
	require.Equal(t, got, reencoded)
}

func TestSerializeCredentialRequiredFieldErrors(t *testing.T) {
	_, err := serializeCredentialEntry(nil)
	require.ErrorContains(t, err, "nil entry")

	_, err = serializeCredentialEntry(&CredentialEntry{
		Subject: [20]byte{0x01},
		Issuer:  [20]byte{0x02},
	})
	require.ErrorContains(t, err, "empty credential type")

	_, err = serializeCredentialEntry(&CredentialEntry{
		Subject:           [20]byte{0x01},
		Issuer:            [20]byte{0x02},
		CredentialType:    []byte{0x01},
		PreviousTxnLgrSeq: 1,
	})
	require.ErrorContains(t, err, "PreviousTxnLgrSeq set without PreviousTxnID")
}

func referenceCredentialBytes(t *testing.T, cred *CredentialEntry) []byte {
	t.Helper()

	subject, err := state.EncodeAccountID(cred.Subject)
	require.NoError(t, err)
	issuer, err := state.EncodeAccountID(cred.Issuer)
	require.NoError(t, err)

	fields := map[string]any{
		"LedgerEntryType": "Credential",
		"Subject":         subject,
		"Issuer":          issuer,
		"CredentialType":  hex.EncodeToString(cred.CredentialType),
		"IssuerNode":      tx.FormatUint64Hex(cred.IssuerNode),
		"Flags":           cred.Flags,
	}
	if cred.Expiration != nil {
		fields["Expiration"] = *cred.Expiration
	}
	if len(cred.URI) != 0 {
		fields["URI"] = hex.EncodeToString(cred.URI)
	}
	if cred.HasSubjectNode && cred.Subject != cred.Issuer {
		fields["SubjectNode"] = tx.FormatUint64Hex(cred.SubjectNode)
	}
	if cred.PreviousTxnID != ([32]byte{}) {
		fields["PreviousTxnID"] = hex.EncodeToString(cred.PreviousTxnID[:])
		fields["PreviousTxnLgrSeq"] = cred.PreviousTxnLgrSeq
	}

	data, err := binarycodec.EncodeBytes(fields)
	require.NoError(t, err)
	return data
}
