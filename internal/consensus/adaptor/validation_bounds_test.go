package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSTValidationRejectsCompleteUnencodableVL(t *testing.T) {
	identity, fields := signedValidationFixture(t)
	field := validationWireField{
		key:       validationFieldKey(typeVector256, fieldAmendments),
		typeCode:  typeVector256,
		fieldCode: fieldAmendments,
		wire:      appendFieldHeader(nil, typeVector256, fieldAmendments),
	}
	field.wire = append(field.wire, 0xFE, 0xD4, 0x18)
	field.wire = append(field.wire, make([]byte, maxVLEncodedLength+1)...)

	fields = insertValidationWireField(fields, field)
	blob, _, _ := signValidationWireFields(t, identity, fields)
	_, err := parseSTValidation(blob)
	assert.ErrorIs(t, err, errVLEncodedTooLong)
}

func TestVerifyValidationRetriesAfterSerializationFailure(t *testing.T) {
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)

	validation := &consensus.Validation{Full: true, LedgerSeq: 42}
	validation.LedgerID[0] = 1
	require.NoError(t, identity.SignValidation(validation))

	validation.Amendments = make([][32]byte, maxVLEncodedLength/32+1)
	err = verifyValidation(validation)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "918744")

	validation.Amendments = nil
	require.NoError(t, verifyValidation(validation))
}

func TestVerifyValidationCacheTracksSignedFieldMutation(t *testing.T) {
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)

	validation := &consensus.Validation{Full: true, LedgerSeq: 42}
	validation.LedgerID[0] = 1
	require.NoError(t, identity.SignValidation(validation))
	require.NoError(t, verifyValidation(validation))

	validation.LedgerSeq++
	require.Error(t, verifyValidation(validation))
	validation.LedgerSeq--
	require.NoError(t, verifyValidation(validation))
}

func TestVerifyValidationConcurrentInitialCache(t *testing.T) {
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)

	validation := &consensus.Validation{Full: true, LedgerSeq: 42}
	validation.LedgerID[0] = 1
	require.NoError(t, identity.SignValidation(validation))
	serialized := serializeSTValidation(validation)
	validation, err = parseSTValidation(serialized)
	require.NoError(t, err)

	const workers = 16
	start := make(chan struct{})
	results := make(chan error, workers)
	for range workers {
		go func() {
			<-start
			results <- verifyValidation(validation)
		}()
	}
	close(start)
	for range workers {
		require.NoError(t, <-results)
	}
}
