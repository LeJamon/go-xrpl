package list

import (
	"fmt"
	"io"
	"os"
)

const maxBodySize = 8 << 20

// readBoundedBody consumes at most maxBodySize+1 bytes. Reading one byte over
// the limit distinguishes an exactly full payload from an oversized one
// without allocating an unbounded buffer.
func readBoundedBody(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxBodySize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBodySize {
		return nil, fmt.Errorf("body exceeds %d bytes", maxBodySize)
	}
	return body, nil
}

func readBoundedFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readBoundedBody(f)
}
