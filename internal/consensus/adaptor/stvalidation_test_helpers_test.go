package adaptor

import (
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

func serializeSTValidation(v *consensus.Validation) []byte {
	serialized, err := serializeSTValidationChecked(v)
	if err != nil {
		panic(err)
	}
	return serialized
}

func appendVL(buf []byte, data []byte) []byte {
	encoded, err := appendVLChecked(buf, data)
	if err != nil {
		panic(err)
	}
	return encoded
}

func buildValidationSigningData(v *consensus.Validation) []byte {
	digest, err := buildValidationSigningDataChecked(v)
	if err != nil {
		panic(err)
	}
	return digest
}

func validationToMessage(v *consensus.Validation) *message.Validation {
	msg, err := validationToMessageChecked(v)
	if err != nil {
		panic(err)
	}
	return msg
}
