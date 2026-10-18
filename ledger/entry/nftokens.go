package entry

import (
	"bytes"
	"errors"
	"fmt"
	"slices"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/types"
)

func (r *streamReader) readNFTokens() ([]NFTokenValue, error) {
	tokens := make([]NFTokenValue, 0)
	for r.hasMore() {
		typeCode, fieldCode, err := r.readFieldHeader()
		if err != nil {
			return nil, err
		}
		if typeCode == 15 && fieldCode == 1 {
			return tokens, nil
		}
		if typeCode != 14 || fieldCode != 12 {
			return nil, errors.New("ledgerfields: NFTokens: expected NFToken wrapper")
		}
		token, err := r.readNFToken()
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, nil
}

func (r *streamReader) readNFToken() (NFTokenValue, error) {
	var token NFTokenValue
	for r.hasMore() {
		typeCode, fieldCode, err := r.readFieldHeader()
		if err != nil {
			return token, err
		}
		if typeCode == 14 && fieldCode == 1 {
			break
		}
		switch {
		case typeCode == 5 && fieldCode == 10:
			if token.HasNFTokenID() {
				return token, errors.New("ledgerfields: NFToken: duplicate NFTokenID")
			}
			if len(r.data)-r.pos < len(token.NFTokenID) {
				return token, errors.New("ledgerfields: NFToken: truncated NFTokenID")
			}
			copy(token.NFTokenID[:], r.data[r.pos:r.pos+32])
			r.pos += 32
			token.present |= innerNFTokenValueBitNFTokenID
		case typeCode == 7 && fieldCode == 5:
			if token.HasURI() {
				return token, errors.New("ledgerfields: NFToken: duplicate URI")
			}
			n, err := r.readVariableLength()
			if err != nil {
				return token, err
			}
			if n > len(r.data)-r.pos {
				return token, errors.New("ledgerfields: NFToken: truncated URI")
			}
			token.URI = bytes.Clone(r.data[r.pos : r.pos+n])
			r.pos += n
			token.present |= innerNFTokenValueBitURI
		default:
			return token, newErrUnknownField("NFToken", typeCode, fieldCode)
		}
	}
	if !token.HasNFTokenID() {
		return token, errors.New("ledgerfields: NFToken: required field NFTokenID is missing")
	}
	return token, nil
}

func validateNFTokens(tokens []NFTokenValue) error {
	for i := range tokens {
		if !tokens[i].HasNFTokenID() {
			return fmt.Errorf("ledgerfields: NFTokens[%d]: required field NFTokenID is not set", i)
		}
	}
	return nil
}

func nftokensToAny(tokens []NFTokenValue) []any {
	result := make([]any, len(tokens))
	for i, token := range tokens {
		object := make(map[string]any, 2)
		if token.HasNFTokenID() {
			object["NFTokenID"] = upperHex(token.NFTokenID[:])
		}
		if token.HasURI() {
			object["URI"] = upperHex(token.URI)
		}
		result[i] = map[string]any{"NFToken": object}
	}
	return result
}

func emitIfChangedNFTokens(out map[string]any, name string, prev, curr []NFTokenValue, pPresent, cPresent uint64) {
	if pPresent == 0 {
		return
	}
	if cPresent != 0 && slices.EqualFunc(prev, curr, func(a, b NFTokenValue) bool {
		return a.present == b.present && a.NFTokenID == b.NFTokenID &&
			(!a.HasURI() || bytes.Equal(a.URI, b.URI))
	}) {
		return
	}
	out[name] = nftokensToAny(prev)
}

func (n *NFTokenPage) encodeNFTokens() ([]byte, error) {
	if n.HasNFTokens() {
		if err := validateNFTokens(n.NFTokens); err != nil {
			return nil, err
		}
		if len(n.NFTokens) > types.MaxJSONArrayElements {
			return nil, &types.JSONArrayTooLargeError{Field: "NFTokens"}
		}
	}

	// NFTokens sorts after every other page field. Encode the outer fields
	// normally, then append the typed array without materializing JSON objects.
	outer := *n
	outer.present &^= nftokenpageBitNFTokens
	fields := outer.ToMap()
	if !n.HasPreviousTxnID() {
		fields["PreviousTxnID"] = "0000000000000000000000000000000000000000000000000000000000000000"
	}
	if !n.HasPreviousTxnLgrSeq() {
		fields["PreviousTxnLgrSeq"] = uint32(0)
	}
	data, err := binarycodec.EncodeBytes(fields)
	if err != nil || !n.HasNFTokens() {
		return data, err
	}

	size := 2
	for _, token := range n.NFTokens {
		size += 35 // wrapper, ID field and payload, object end marker
		if token.HasURI() {
			if len(token.URI) > 918744 {
				return nil, serdes.ErrLengthPrefixTooLong
			}
			size += 1 + len(token.URI) + variableLengthSize(len(token.URI))
		}
	}
	data = slices.Grow(data, size)
	data = append(data, 0xFA)
	for _, token := range n.NFTokens {
		data = append(data, 0xEC, 0x5A)
		data = append(data, token.NFTokenID[:]...)
		if token.HasURI() {
			data = append(data, 0x75)
			data = appendVariableLength(data, len(token.URI))
			data = append(data, token.URI...)
		}
		data = append(data, types.ObjectEndMarker)
	}
	return append(data, types.ArrayEndMarker), nil
}

func variableLengthSize(length int) int {
	switch {
	case length <= 192:
		return 1
	case length <= 12480:
		return 2
	default:
		return 3
	}
}

func appendVariableLength(data []byte, length int) []byte {
	switch {
	case length <= 192:
		return append(data, byte(length))
	case length <= 12480:
		length -= 193
		return append(data, byte((length>>8)+193), byte(length))
	default:
		length -= 12481
		return append(data, byte((length>>16)+241), byte(length>>8), byte(length))
	}
}
