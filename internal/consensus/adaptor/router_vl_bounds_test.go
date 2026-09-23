package adaptor

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRouterValidationVariableLengthBounds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		length   int
		truncate bool
		accepted bool
	}{
		{"largest aligned vector", 918720, false, true},
		{"first oversized aligned vector", 918752, false, false},
		{"largest wire prefix aligned vector", 929984, false, false},
		{"truncated valid vector", 918720, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			identity, fields := signedValidationFixture(t)
			signingTime := validationWireFieldIndex(t, fields, validationFieldKey(typeUINT32, fieldSigningTime))
			fields[signingTime].wire = appendFieldHeader(nil, typeUINT32, fieldSigningTime)
			fields[signingTime].wire = binary.BigEndian.AppendUint32(fields[signingTime].wire, uint32(time.Now().Unix()-protocol.RippleEpochUnix))
			validBlob, _, _ := signValidationWireFields(t, identity, fields)

			field := validationWireField{
				key:       validationFieldKey(typeVector256, fieldAmendments),
				typeCode:  typeVector256,
				fieldCode: fieldAmendments,
				wire:      appendFieldHeader(nil, typeVector256, fieldAmendments),
			}
			encoded := tc.length - 12481
			field.wire = append(field.wire, byte(241+(encoded>>16)), byte(encoded>>8), byte(encoded))
			field.wire = append(field.wire, make([]byte, tc.length)...)
			fields = insertValidationWireField(fields, field)
			blob, _, _ := signValidationWireFields(t, identity, fields)
			if tc.truncate {
				blob = blob[:len(blob)-1]
			}

			router, sender := makeRouterWithBadDataRecorder(t)
			engine := &mockEngine{}
			router.engine = engine
			router.handleMessage(&peermanagement.InboundMessage{
				PeerID:  7,
				Type:    message.TypeValidation,
				Payload: encodePayload(t, &message.Validation{Validation: blob}),
			})

			if tc.accepted {
				require.Len(t, engine.getValidations(), 1)
				assert.Len(t, engine.getValidations()[0].Amendments, tc.length/32)
				assert.Empty(t, sender.getBadDataCalls())
				return
			}
			assert.Empty(t, engine.getValidations())
			require.Equal(t, []badDataCall{{peerID: 7, reason: "validation-parse"}}, sender.getBadDataCalls())
			firstSeen, _ := router.messageSeen.observe(hashValidationSuppression(blob))
			assert.True(t, firstSeen, "rejected bytes must not enter validation suppression")

			router.handleMessage(&peermanagement.InboundMessage{
				PeerID:  7,
				Type:    message.TypeValidation,
				Payload: encodePayload(t, &message.Validation{Validation: validBlob}),
			})
			assert.Len(t, engine.getValidations(), 1)
			assert.Len(t, sender.getBadDataCalls(), 1)
		})
	}
}
