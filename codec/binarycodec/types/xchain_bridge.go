//revive:disable:var-naming
package types

import (
	"bytes"
	"errors"
	"fmt"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

var (
	errNotValidXChainBridge = errors.New("not a valid xchain bridge")
	badXRPCurrencyBytes     = []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 'X', 'R', 'P', 0, 0, 0, 0, 0}
)

// accountVLLength is the length prefix a door account carries on the wire: a
// bridge door is serialized like an STAccount, a VL-prefixed 160-bit value.
const accountVLLength = 20

// XChainBridge is the codec for the XChainBridge type: two door accounts and
// two issues. Each door is serialized as a VL-prefixed AccountID and each
// issue as an Issue (20 bytes for XRP, 40 for an IOU), matching rippled's
// STXChainBridge::add.
type XChainBridge struct{}

// xchainBridgeFields lists the door/issue field pairs in wire order.
var xchainBridgeFields = [2]struct {
	door, issue string
}{
	{"LockingChainDoor", "LockingChainIssue"},
	{"IssuingChainDoor", "IssuingChainIssue"},
}

// FromJSON converts a json XChainBridge object to its byte slice representation.
// Doors are classic address strings; issues are Issue-shaped objects
// ({"currency": ...} or {"currency": ..., "issuer": ...}).
func (x *XChainBridge) FromJSON(json any) ([]byte, error) {
	v, ok := json.(map[string]any)
	if !ok {
		return nil, errNotValidJSON
	}
	for name := range v {
		switch name {
		case "LockingChainDoor", "LockingChainIssue", "IssuingChainDoor", "IssuingChainIssue":
		default:
			return nil, fmt.Errorf("%w: extra field %s", errNotValidXChainBridge, name)
		}
	}

	out := make([]byte, 0, 2*(1+20+40))
	for _, f := range xchainBridgeFields {
		door, ok := v[f.door].(string)
		if !ok {
			return nil, fmt.Errorf("%w: %s must be a string", errNotValidXChainBridge, f.door)
		}
		_, doorID, err := addresscodec.DecodeClassicAddressToAccountID(door)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", errNotValidXChainBridge, f.door, errDecodeClassicAddress)
		}
		out = append(out, accountVLLength)
		out = append(out, doorID...)

		issueJSON, ok := v[f.issue]
		if !ok {
			return nil, fmt.Errorf("%w: missing %s", errNotValidXChainBridge, f.issue)
		}
		issueMap, ok := issueJSON.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: %s must be an issue object", errNotValidXChainBridge, f.issue)
		}
		issueBytes, err := xchainIssueFromJSON(issueMap)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", errNotValidXChainBridge, f.issue, err)
		}
		out = append(out, issueBytes...)
	}

	return out, nil
}

// ToJSON converts the byte slice representation of an XChainBridge to its json
// representation: VL-prefixed door account followed by an issue, twice.
func (x *XChainBridge) ToJSON(p *serdes.BinaryParser, _ ...int) (any, error) {
	json := make(map[string]any, 4)

	for _, f := range xchainBridgeFields {
		vlen, err := p.ReadVariableLength()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.door, err)
		}
		if vlen != accountVLLength {
			return nil, fmt.Errorf("%w: %s has invalid STAccount size %d", errNotValidXChainBridge, f.door, vlen)
		}
		doorBytes, err := p.ReadBytes(accountVLLength)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.door, err)
		}
		door, err := addresscodec.Encode(doorBytes, []byte{addresscodec.AccountAddressPrefix}, addresscodec.AccountAddressLength)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.door, err)
		}
		json[f.door] = door

		issue, err := (&Issue{}).ToJSON(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.issue, err)
		}
		if issueMap, ok := issue.(map[string]any); ok {
			if _, err := xchainIssueFromJSON(issueMap); err != nil {
				return nil, fmt.Errorf("%w: %s: %w", errNotValidXChainBridge, f.issue, err)
			}
		} else {
			return nil, fmt.Errorf("%w: %s is not an issue object", errNotValidXChainBridge, f.issue)
		}
		json[f.issue] = issue
	}

	return json, nil
}

func xchainIssueFromJSON(issue map[string]any) ([]byte, error) {
	if _, isMPT := issue["mpt_issuance_id"]; isMPT {
		return nil, errors.New("MPT issues are not supported")
	}
	currency, ok := issue["currency"]
	if !ok {
		return nil, ErrInvalidCurrency
	}
	currencyBytes, err := (&Currency{}).FromJSON(currency)
	if err != nil || bytes.Equal(currencyBytes, noCurrencyBytes) || bytes.Equal(currencyBytes, badXRPCurrencyBytes) {
		return nil, ErrInvalidCurrency
	}

	issuer, hasIssuer := issue["issuer"]
	if bytes.Equal(currencyBytes, zeroByteArray) {
		if hasIssuer && issuer != nil {
			return nil, ErrInvalidIssuer
		}
		return currencyBytes, nil
	}
	issuerString, ok := issuer.(string)
	if !hasIssuer || !ok || issuerString == "" {
		return nil, ErrInvalidIssuer
	}
	_, issuerBytes, err := addresscodec.DecodeClassicAddressToAccountID(issuerString)
	if err != nil || bytes.Equal(issuerBytes, zeroByteArray) || bytes.Equal(issuerBytes, noAccountBytes) {
		return nil, ErrInvalidIssuer
	}
	return append(currencyBytes, issuerBytes...), nil
}
