package message

import (
	"errors"

	"google.golang.org/protobuf/encoding/protowire"
)

// WalkManifests validates a TMManifests payload before visiting its entries.
// The returned byte slices alias payload and are only valid during the call.
func WalkManifests(payload []byte, visit func([]byte)) (int, error) {
	count, err := validateManifests(payload, false, nil)
	if err != nil {
		return 0, err
	}
	if visit == nil || count == 0 {
		return count, nil
	}
	_, err = validateManifests(payload, true, visit)
	return count, err
}

func validateManifests(payload []byte, visitEntries bool, visit func([]byte)) (int, error) {
	count := 0
	for len(payload) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(payload)
		if tagLen < 0 {
			return 0, protowire.ParseError(tagLen)
		}
		payload = payload[tagLen:]

		switch {
		case num == 1 && typ == protowire.BytesType:
			entry, valueLen := protowire.ConsumeBytes(payload)
			if valueLen < 0 {
				return 0, protowire.ParseError(valueLen)
			}
			stObject, err := manifestSTObject(entry)
			if err != nil {
				return 0, err
			}
			count++
			if visitEntries {
				visit(stObject)
			}
			payload = payload[valueLen:]
		case num == 2 && typ == protowire.VarintType:
			_, valueLen := protowire.ConsumeVarint(payload)
			if valueLen < 0 {
				return 0, protowire.ParseError(valueLen)
			}
			payload = payload[valueLen:]
		default:
			valueLen := protowire.ConsumeFieldValue(num, typ, payload)
			if valueLen < 0 {
				return 0, protowire.ParseError(valueLen)
			}
			payload = payload[valueLen:]
		}
	}
	return count, nil
}

func manifestSTObject(payload []byte) ([]byte, error) {
	var stObject []byte
	found := false
	for len(payload) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(payload)
		if tagLen < 0 {
			return nil, protowire.ParseError(tagLen)
		}
		payload = payload[tagLen:]

		if num == 1 && typ == protowire.BytesType {
			value, valueLen := protowire.ConsumeBytes(payload)
			if valueLen < 0 {
				return nil, protowire.ParseError(valueLen)
			}
			stObject = value
			found = true
			payload = payload[valueLen:]
			continue
		}

		valueLen := protowire.ConsumeFieldValue(num, typ, payload)
		if valueLen < 0 {
			return nil, protowire.ParseError(valueLen)
		}
		payload = payload[valueLen:]
	}
	if !found {
		return nil, errors.New("TMManifest: missing required stobject")
	}
	return stObject, nil
}
