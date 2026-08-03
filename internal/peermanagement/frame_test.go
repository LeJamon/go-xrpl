package peermanagement

import (
	"encoding/binary"
	"io"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

func readTestFrame(r io.Reader) (*message.Header, []byte, error) {
	header, err := message.ReadHeader(r)
	if err != nil {
		return nil, nil, err
	}
	payload, err := message.ReadPayload(r, *header)
	if err != nil {
		return nil, nil, err
	}
	return header, payload, nil
}

func rawTestWireMessage(
	msgType message.MessageType,
	payload []byte,
	algorithm message.CompressionAlgorithm,
	uncompressedSize uint32,
) []byte {
	headerSize := message.HeaderSizeUncompressed
	if algorithm != message.AlgorithmNone {
		headerSize = message.HeaderSizeCompressed
	}
	frame := make([]byte, headerSize+len(payload))
	firstFour := uint32(len(payload))
	if algorithm != message.AlgorithmNone {
		firstFour |= uint32(algorithm) << 24
	}
	binary.BigEndian.PutUint32(frame[:4], firstFour)
	binary.BigEndian.PutUint16(frame[4:6], uint16(msgType))
	if algorithm != message.AlgorithmNone {
		binary.BigEndian.PutUint32(frame[6:10], uncompressedSize)
	}
	copy(frame[headerSize:], payload)
	return frame
}
