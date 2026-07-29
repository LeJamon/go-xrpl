package state

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/keylet"
)

type accountRootLookupReader struct {
	data []byte
	err  error
}

func (r accountRootLookupReader) Read(keylet.Keylet) ([]byte, error) {
	return r.data, r.err
}

func TestReadAccountRootPreservesLookupFailures(t *testing.T) {
	readErr := errors.New("storage read failed")
	if _, err := ReadAccountRoot(accountRootLookupReader{err: readErr}, [20]byte{}); !errors.Is(err, readErr) {
		t.Fatalf("ReadAccountRoot error = %v, want %v", err, readErr)
	}

	if account, err := ReadAccountRoot(accountRootLookupReader{}, [20]byte{}); err != nil || account != nil {
		t.Fatalf("ReadAccountRoot absent = (%v, %v), want (nil, nil)", account, err)
	}

	if _, err := ReadAccountRoot(accountRootLookupReader{data: []byte{0xff}}, [20]byte{}); err == nil {
		t.Fatal("ReadAccountRoot accepted malformed AccountRoot data")
	}
}
