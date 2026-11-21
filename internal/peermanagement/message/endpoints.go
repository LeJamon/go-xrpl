package message

import "fmt"

// DecodeEndpointsForPeer validates an inbound TMEndpoints frame and returns
// its version before applying the discovery entry-count limit. Oversized
// frames are represented by a version-only Endpoints value so they never make
// the protobuf runtime allocate a repeated list that will be discarded.
func DecodeEndpointsForPeer(data []byte) (*Endpoints, bool, error) {
	// The peer frame reader enforces this cap before handing payloads to the
	// message package. Keep the same bound for direct callers so a valid small
	// entry count cannot make Decode materialize an arbitrarily large unknown
	// field payload.
	if len(data) > smallMsgMax {
		return nil, false, fmt.Errorf("endpoints payload exceeds %d bytes", smallMsgMax)
	}
	info, err := scanWirePayload(TypeEndpoints, data, true, true)
	if err != nil {
		return nil, false, fmt.Errorf("failed to preflight endpoints: %w", err)
	}
	if info.endpointCount > maxEndpoints {
		return &Endpoints{Version: info.endpointVersion}, true, nil
	}

	decoded, err := codecs[TypeEndpoints].decodePayload(data)
	if err != nil {
		return nil, false, err
	}
	endpoints, ok := decoded.(*Endpoints)
	if !ok {
		return nil, false, fmt.Errorf("decoded endpoints has type %T", decoded)
	}
	return endpoints, false, nil
}
