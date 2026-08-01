package nodestore

import (
	"errors"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
)

// ErrDataCorrupt indicates that stored data is corrupted.
var ErrDataCorrupt = errors.New("data corrupt")

var ErrInvalidNode = errors.New("invalid node")

var ErrInvalidConfig = errors.New("invalid nodestore config")

var ErrClosed = kvstore.ErrClosed
