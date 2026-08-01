package adaptor

import (
	"encoding/binary"
	"math/big"
	"testing"
	"time"

	rootcrypto "github.com/LeJamon/go-xrpl/crypto"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/stretchr/testify/require"
)

func TestVerifyValidationEnforcesCanonicalFlag(t *testing.T) {
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	validation := &consensus.Validation{
		LedgerID:  consensus.LedgerID{1},
		LedgerSeq: 2,
		SignTime:  time.Unix(protocol.RippleEpochUnix+3, 0),
		Full:      true,
	}
	require.NoError(t, identity.SignValidation(validation))
	require.NoError(t, VerifyValidation(validation))

	r, s, err := rootcrypto.DERSigToRS(validation.Signature)
	require.NoError(t, err)
	highS := new(big.Int).Sub(btcec.S256().N, new(big.Int).SetBytes(s))
	validation.Signature = rootcrypto.EncodeDERSignature(new(big.Int).SetBytes(r), highS)
	require.Error(t, VerifyValidation(validation))

	validation.Flags &^= vfFullyCanonicalSig
	validation.Signature, err = identity.Sign(buildValidationSigningData(validation))
	require.NoError(t, err)
	r, s, err = rootcrypto.DERSigToRS(validation.Signature)
	require.NoError(t, err)
	highS.Sub(btcec.S256().N, new(big.Int).SetBytes(s))
	validation.Signature = rootcrypto.EncodeDERSignature(new(big.Int).SetBytes(r), highS)
	require.NoError(t, VerifyValidation(validation))
}

func TestSTValidationPreservesSignedAmountPresence(t *testing.T) {
	for _, test := range []struct {
		name  string
		raw   uint64
		value drops.XRPAmount
	}{
		{name: "positive zero", raw: 1 << 62},
		{name: "negative one", raw: 1, value: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity, fields := signedValidationFixture(t)
			amount := validationWireField{
				key:       validationFieldKey(typeAmount, fieldBaseFeeDrops),
				typeCode:  typeAmount,
				fieldCode: fieldBaseFeeDrops,
				wire:      appendFieldHeader(nil, typeAmount, fieldBaseFeeDrops),
			}
			amount.wire = binary.BigEndian.AppendUint64(amount.wire, test.raw)
			fields = append(fields, amount)

			blob, _, _ := signValidationWireFields(t, identity, fields)
			validation, err := parseSTValidation(blob)
			require.NoError(t, err)
			value, present := validation.BaseFeeDropsVote()
			require.True(t, present)
			require.Equal(t, test.value, value)

			vote := extractFeeVote(validation, true)
			require.NotNil(t, vote.BaseFee)
			require.Equal(t, test.value, *vote.BaseFee)
		})
	}
}

func TestSerializeSTValidationEmitsCloseTimeAndCanonicalFlag(t *testing.T) {
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	closeTime := time.Unix(protocol.RippleEpochUnix+10, 0).UTC()
	validation := &consensus.Validation{
		LedgerID:  consensus.LedgerID{1},
		LedgerSeq: 2,
		SignTime:  time.Unix(protocol.RippleEpochUnix+9, 0),
		CloseTime: closeTime,
		Flags:     0x40,
	}
	require.NoError(t, identity.SignValidation(validation))

	parsed, err := parseSTValidation(SerializeSTValidation(validation))
	require.NoError(t, err)
	require.Equal(t, closeTime, parsed.CloseTime)
	require.NotZero(t, parsed.Flags&vfFullyCanonicalSig)
	require.NotZero(t, parsed.Flags&0x40)

	validation.Flags = 0x40
	parsed, err = parseSTValidation(SerializeSTValidation(validation))
	require.NoError(t, err)
	require.Equal(t, uint32(0x40), parsed.Flags)
}
