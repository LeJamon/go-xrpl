package adaptor

import (
	"bytes"
	"encoding/binary"
	"sort"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type validationWireField struct {
	key       uint32
	typeCode  int
	fieldCode int
	wire      []byte
}

func signedValidationFixture(t *testing.T) (*ValidatorIdentity, []validationWireField) {
	t.Helper()

	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)

	validation := &consensus.Validation{
		Full:      true,
		LedgerSeq: 42,
		SignTime:  time.Unix(protocol.RippleEpochUnix+800_000_000, 0),
	}
	for i := range validation.LedgerID {
		validation.LedgerID[i] = byte(i + 1)
	}
	require.NoError(t, identity.SignValidation(validation))

	return identity, decodeValidationWireFields(t, SerializeSTValidation(validation))
}

func decodeValidationWireFields(t *testing.T, blob []byte) []validationWireField {
	t.Helper()

	var fields []validationWireField
	for pos := 0; pos < len(blob); {
		start := pos
		typeCode, fieldCode, err := readFieldHeader(blob, &pos)
		require.NoError(t, err)
		_, err = skipFieldData(typeCode, blob, &pos)
		require.NoError(t, err)
		fields = append(fields, validationWireField{
			key:       validationFieldKey(typeCode, fieldCode),
			typeCode:  typeCode,
			fieldCode: fieldCode,
			wire:      append([]byte(nil), blob[start:pos]...),
		})
	}
	return fields
}

func validationWireFieldIndex(t *testing.T, fields []validationWireField, key uint32) int {
	t.Helper()

	for i := range fields {
		if fields[i].key == key {
			return i
		}
	}
	t.Fatalf("validation field 0x%x not found", key)
	return -1
}

func signValidationWireFields(
	t *testing.T,
	identity *ValidatorIdentity,
	fields []validationWireField,
) ([]byte, []byte, [32]byte) {
	t.Helper()

	signingFields := make([]validationWireField, 0, len(fields))
	for _, field := range fields {
		if field.key != validationFieldKey(typeBlob, fieldSignature) {
			signingFields = append(signingFields, field)
		}
	}
	sort.SliceStable(signingFields, func(i, j int) bool {
		return signingFields[i].key < signingFields[j].key
	})

	var signingData []byte
	for _, field := range signingFields {
		signingData = append(signingData, field.wire...)
	}
	digest := sha512half.Sum(protocol.HashPrefixValidation().Bytes(), signingData)
	signature, err := identity.Sign(digest[:])
	require.NoError(t, err)
	require.True(t, Verify(identity.SigningKey[:], digest[:], signature))

	var blob []byte
	for _, field := range fields {
		if field.key == validationFieldKey(typeBlob, fieldSignature) {
			blob = appendFieldHeader(blob, typeBlob, fieldSignature)
			blob = appendVL(blob, signature)
			continue
		}
		blob = append(blob, field.wire...)
	}
	return blob, signature, digest
}

func TestParseSTValidation_RequiresEveryTemplateField(t *testing.T) {
	required := []struct {
		name string
		key  uint32
	}{
		{"Flags", validationFieldKey(typeUINT32, fieldFlags)},
		{"LedgerHash", validationFieldKey(typeHash256, fieldLedgerHash)},
		{"LedgerSequence", validationFieldKey(typeUINT32, fieldLedgerSequence)},
		{"SigningTime", validationFieldKey(typeUINT32, fieldSigningTime)},
		{"SigningPubKey", validationFieldKey(typeBlob, fieldSigningPubKey)},
		{"Signature", validationFieldKey(typeBlob, fieldSignature)},
	}

	for _, test := range required {
		t.Run(test.name, func(t *testing.T) {
			identity, fields := signedValidationFixture(t)
			index := validationWireFieldIndex(t, fields, test.key)
			fields = append(fields[:index:index], fields[index+1:]...)

			blob, _, _ := signValidationWireFields(t, identity, fields)
			_, err := parseSTValidation(blob)
			assert.ErrorIs(t, err, errMissingFields)
		})
	}
}

func TestParseSTValidation_RejectsDuplicateTemplateFields(t *testing.T) {
	required := []struct {
		name string
		key  uint32
	}{
		{"Flags", validationFieldKey(typeUINT32, fieldFlags)},
		{"LedgerHash", validationFieldKey(typeHash256, fieldLedgerHash)},
		{"LedgerSequence", validationFieldKey(typeUINT32, fieldLedgerSequence)},
		{"SigningTime", validationFieldKey(typeUINT32, fieldSigningTime)},
		{"SigningPubKey", validationFieldKey(typeBlob, fieldSigningPubKey)},
		{"Signature", validationFieldKey(typeBlob, fieldSignature)},
	}

	for _, test := range required {
		t.Run(test.name, func(t *testing.T) {
			identity, fields := signedValidationFixture(t)
			index := validationWireFieldIndex(t, fields, test.key)
			duplicate := fields[index]
			duplicate.wire = append([]byte(nil), duplicate.wire...)
			fields = append(fields, validationWireField{})
			copy(fields[index+2:], fields[index+1:])
			fields[index+1] = duplicate

			blob, _, _ := signValidationWireFields(t, identity, fields)
			_, err := parseSTValidation(blob)
			assert.ErrorIs(t, err, errDuplicateField)
		})
	}
}

func TestParseSTValidation_TemplateShape(t *testing.T) {
	t.Run("out of order is canonicalized", func(t *testing.T) {
		identity, fields := signedValidationFixture(t)
		signingTime := validationWireFieldIndex(t, fields, validationFieldKey(typeUINT32, fieldSigningTime))
		ledgerHash := validationWireFieldIndex(t, fields, validationFieldKey(typeHash256, fieldLedgerHash))
		fields[signingTime], fields[ledgerHash] = fields[ledgerHash], fields[signingTime]

		blob, _, _ := signValidationWireFields(t, identity, fields)
		validation, err := parseSTValidation(blob)
		require.NoError(t, err)
		assert.NoError(t, VerifyValidation(validation))

		canonical, err := CanonicalSTValidation(validation)
		require.NoError(t, err)
		assert.False(t, bytes.Equal(blob, canonical))
		canonicalFields := decodeValidationWireFields(t, canonical)
		assert.True(t, sort.SliceIsSorted(canonicalFields, func(i, j int) bool {
			return canonicalFields[i].key < canonicalFields[j].key
		}))
		reparsed, err := parseSTValidation(canonical)
		require.NoError(t, err)
		assert.NoError(t, VerifyValidation(reparsed))
	})

	t.Run("unexpected field", func(t *testing.T) {
		identity, fields := signedValidationFixture(t)
		ledgerHash := validationWireFieldIndex(t, fields, validationFieldKey(typeHash256, fieldLedgerHash))
		unexpected := validationWireField{
			key:       validationFieldKey(typeHash256, 2),
			typeCode:  typeHash256,
			fieldCode: 2,
			wire:      appendFieldHeader(nil, typeHash256, 2),
		}
		unexpected.wire = append(unexpected.wire, make([]byte, 32)...)
		fields = append(fields, validationWireField{})
		copy(fields[ledgerHash+2:], fields[ledgerHash+1:])
		fields[ledgerHash+1] = unexpected

		blob, _, _ := signValidationWireFields(t, identity, fields)
		_, err := parseSTValidation(blob)
		assert.ErrorIs(t, err, errUnexpectedField)
	})

	t.Run("wrong type", func(t *testing.T) {
		identity, fields := signedValidationFixture(t)
		flags := validationWireFieldIndex(t, fields, validationFieldKey(typeUINT32, fieldFlags))
		fields[flags] = validationWireField{
			key:       validationFieldKey(typeUINT64, fieldFlags),
			typeCode:  typeUINT64,
			fieldCode: fieldFlags,
			wire:      appendFieldHeader(nil, typeUINT64, fieldFlags),
		}
		fields[flags].wire = binary.BigEndian.AppendUint64(fields[flags].wire, vfFullyCanonicalSig|vfFullValidation)

		blob, _, _ := signValidationWireFields(t, identity, fields)
		_, err := parseSTValidation(blob)
		assert.ErrorIs(t, err, errUnexpectedField)
	})

	t.Run("wrong signing key length", func(t *testing.T) {
		identity, fields := signedValidationFixture(t)
		signingKey := validationWireFieldIndex(t, fields, validationFieldKey(typeBlob, fieldSigningPubKey))
		fields[signingKey].wire = appendFieldHeader(nil, typeBlob, fieldSigningPubKey)
		fields[signingKey].wire = appendVL(fields[signingKey].wire, identity.SigningKey[:32])

		blob, _, _ := signValidationWireFields(t, identity, fields)
		_, err := parseSTValidation(blob)
		assert.ErrorIs(t, err, errInvalidFieldValue)
	})

	t.Run("long signing key", func(t *testing.T) {
		identity, fields := signedValidationFixture(t)
		signingKey := validationWireFieldIndex(t, fields, validationFieldKey(typeBlob, fieldSigningPubKey))
		longKey := append(append([]byte(nil), identity.SigningKey[:]...), 0)
		fields[signingKey].wire = appendFieldHeader(nil, typeBlob, fieldSigningPubKey)
		fields[signingKey].wire = appendVL(fields[signingKey].wire, longKey)

		blob, _, _ := signValidationWireFields(t, identity, fields)
		_, err := parseSTValidation(blob)
		assert.ErrorIs(t, err, errInvalidFieldValue)
	})

	t.Run("ed25519 signing key", func(t *testing.T) {
		identity, fields := signedValidationFixture(t)
		signingKey := validationWireFieldIndex(t, fields, validationFieldKey(typeBlob, fieldSigningPubKey))
		ed25519Key := make([]byte, 33)
		ed25519Key[0] = 0xED
		fields[signingKey].wire = appendFieldHeader(nil, typeBlob, fieldSigningPubKey)
		fields[signingKey].wire = appendVL(fields[signingKey].wire, ed25519Key)

		blob, _, _ := signValidationWireFields(t, identity, fields)
		_, err := parseSTValidation(blob)
		assert.ErrorIs(t, err, errInvalidFieldValue)
	})

	t.Run("wrong vector length", func(t *testing.T) {
		identity, fields := signedValidationFixture(t)
		amendments := validationWireField{
			key:       validationFieldKey(typeVector256, fieldAmendments),
			typeCode:  typeVector256,
			fieldCode: fieldAmendments,
			wire:      appendFieldHeader(nil, typeVector256, fieldAmendments),
		}
		amendments.wire = appendVL(amendments.wire, make([]byte, 31))
		fields = append(fields, amendments)

		blob, _, _ := signValidationWireFields(t, identity, fields)
		_, err := parseSTValidation(blob)
		assert.ErrorIs(t, err, errInvalidFieldValue)
	})

	t.Run("non-canonical field id", func(t *testing.T) {
		identity, fields := signedValidationFixture(t)
		flags := validationWireFieldIndex(t, fields, validationFieldKey(typeUINT32, fieldFlags))
		fields[flags].wire = append([]byte{0x20, byte(fieldFlags)}, fields[flags].wire[1:]...)

		blob, _, _ := signValidationWireFields(t, identity, fields)
		_, err := parseSTValidation(blob)
		assert.ErrorIs(t, err, errNonCanonicalFieldID)
	})

	t.Run("explicit default cookie", func(t *testing.T) {
		identity, fields := signedValidationFixture(t)
		ledgerHash := validationWireFieldIndex(t, fields, validationFieldKey(typeHash256, fieldLedgerHash))
		cookie := validationWireField{
			key:       validationFieldKey(typeUINT64, fieldCookie),
			typeCode:  typeUINT64,
			fieldCode: fieldCookie,
			wire:      appendFieldHeader(nil, typeUINT64, fieldCookie),
		}
		cookie.wire = binary.BigEndian.AppendUint64(cookie.wire, 0)
		fields = append(fields, validationWireField{})
		copy(fields[ledgerHash+1:], fields[ledgerHash:])
		fields[ledgerHash] = cookie

		blob, _, _ := signValidationWireFields(t, identity, fields)
		_, err := parseSTValidation(blob)
		assert.ErrorIs(t, err, errInvalidFieldValue)
	})
}

func TestParseSTValidation_ValidatesAmounts(t *testing.T) {
	issuedAmount := func(raw uint64) []byte {
		amount := make([]byte, 48)
		binary.BigEndian.PutUint64(amount, raw)
		amount[8] = 1
		amount[28] = 1
		return amount
	}
	mptAmount := func() []byte {
		amount := make([]byte, 33)
		amount[0] = 0x60
		amount[8] = 7
		amount[9] = 1
		return amount
	}
	amountField := func(amount []byte) validationWireField {
		wire := appendFieldHeader(nil, typeAmount, fieldBaseFeeDrops)
		wire = append(wire, amount...)
		return validationWireField{
			key:       validationFieldKey(typeAmount, fieldBaseFeeDrops),
			typeCode:  typeAmount,
			fieldCode: fieldBaseFeeDrops,
			wire:      wire,
		}
	}

	const (
		issuedFlag       = uint64(1) << 63
		positiveFlag     = uint64(1) << 62
		minIOUMantissa   = uint64(1_000_000_000_000_000)
		centeredExponent = uint64(97)
	)

	invalid := []struct {
		name   string
		amount []byte
		target error
	}{
		{
			name:   "native negative zero",
			amount: make([]byte, 8),
			target: errInvalidFieldValue,
		},
		{
			name: "issued native currency",
			amount: func() []byte {
				amount := issuedAmount(issuedFlag)
				clear(amount[8:28])
				return amount
			}(),
			target: errInvalidFieldValue,
		},
		{
			name: "issued native account",
			amount: func() []byte {
				amount := issuedAmount(issuedFlag)
				clear(amount[28:48])
				return amount
			}(),
			target: errInvalidFieldValue,
		},
		{
			name:   "issued invalid mantissa",
			amount: issuedAmount(issuedFlag | positiveFlag | centeredExponent<<54 | 1),
			target: errInvalidFieldValue,
		},
		{
			name:   "issued invalid exponent",
			amount: issuedAmount(issuedFlag | positiveFlag | minIOUMantissa),
			target: errInvalidFieldValue,
		},
		{
			name:   "truncated MPT",
			amount: mptAmount()[:32],
			target: errShortData,
		},
	}

	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			identity, fields := signedValidationFixture(t)
			fields = append(fields, amountField(test.amount))

			blob, _, _ := signValidationWireFields(t, identity, fields)
			_, err := parseSTValidation(blob)
			assert.ErrorIs(t, err, test.target)
		})
	}

	valid := []struct {
		name   string
		amount []byte
	}{
		{
			name: "native",
			amount: func() []byte {
				amount := make([]byte, 8)
				binary.BigEndian.PutUint64(amount, positiveFlag|10)
				return amount
			}(),
		},
		{
			name:   "issued zero",
			amount: issuedAmount(issuedFlag),
		},
		{
			name:   "MPT",
			amount: mptAmount(),
		},
	}

	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			identity, fields := signedValidationFixture(t)
			fields = append(fields, amountField(test.amount))

			blob, _, _ := signValidationWireFields(t, identity, fields)
			validation, err := parseSTValidation(blob)
			require.NoError(t, err)
			assert.NoError(t, VerifyValidation(validation))
		})
	}
}

func TestParseSTValidation_TracksRequiredPresenceSeparatelyFromValue(t *testing.T) {
	identity, fields := signedValidationFixture(t)

	for _, key := range []uint32{
		validationFieldKey(typeUINT32, fieldFlags),
		validationFieldKey(typeUINT32, fieldLedgerSequence),
		validationFieldKey(typeUINT32, fieldSigningTime),
	} {
		index := validationWireFieldIndex(t, fields, key)
		fields[index].wire = fields[index].wire[:len(fields[index].wire)-4]
		fields[index].wire = binary.BigEndian.AppendUint32(fields[index].wire, 0)
	}
	ledgerHash := validationWireFieldIndex(t, fields, validationFieldKey(typeHash256, fieldLedgerHash))
	fields[ledgerHash].wire = appendFieldHeader(nil, typeHash256, fieldLedgerHash)
	fields[ledgerHash].wire = append(fields[ledgerHash].wire, make([]byte, 32)...)

	blob, _, _ := signValidationWireFields(t, identity, fields)
	validation, err := parseSTValidation(blob)
	require.NoError(t, err)
	assert.Zero(t, validation.Flags)
	assert.Zero(t, validation.LedgerSeq)
	assert.Equal(t, consensus.LedgerID{}, validation.LedgerID)
	assert.NoError(t, VerifyValidation(validation))
}

func TestParseSTValidation_RequiredSignatureMayBeEmptyStructurally(t *testing.T) {
	_, fields := signedValidationFixture(t)
	signature := validationWireFieldIndex(t, fields, validationFieldKey(typeBlob, fieldSignature))
	fields[signature].wire = appendFieldHeader(nil, typeBlob, fieldSignature)
	fields[signature].wire = appendVL(fields[signature].wire, nil)

	var blob []byte
	for _, field := range fields {
		blob = append(blob, field.wire...)
	}
	validation, err := parseSTValidation(blob)
	require.NoError(t, err)
	assert.Empty(t, validation.Signature)
	assert.Error(t, VerifyValidation(validation))
}

func TestParseOrVerifyValidation_RejectsEverySerializedByteMutation(t *testing.T) {
	identity, fields := signedValidationFixture(t)
	blob, _, _ := signValidationWireFields(t, identity, fields)
	validation, err := parseSTValidation(blob)
	require.NoError(t, err)
	require.NoError(t, VerifyValidation(validation))

	for i := range blob {
		tampered := append([]byte(nil), blob...)
		tampered[i] ^= 1

		parsed, err := parseSTValidation(tampered)
		if err != nil {
			continue
		}
		assert.Error(t, VerifyValidation(parsed), "mutation at byte %d was accepted", i)
	}
}
