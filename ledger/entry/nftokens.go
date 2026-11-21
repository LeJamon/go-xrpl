package entry

import (
	"bytes"
	"errors"
	"fmt"
	"slices"

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

func validateNFTokensForEncode(tokens []NFTokenValue) error {
	if err := validateNFTokens(tokens); err != nil {
		return err
	}
	if len(tokens) > types.MaxJSONArrayElements {
		return &types.JSONArrayTooLargeError{Field: "NFTokens"}
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
