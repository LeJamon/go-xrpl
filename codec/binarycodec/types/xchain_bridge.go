//revive:disable:var-naming
package types

import (
	"errors"
	"fmt"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

var (
	errNotValidXChainBridge = errors.New("not a valid xchain bridge")
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
		issueBytes, err := (&Issue{}).FromJSON(issueJSON)
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
		json[f.issue] = issue
	}

	return json, nil
}
