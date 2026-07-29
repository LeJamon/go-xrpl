package nodestore

import (
	"errors"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
)

// ErrDataCorrupt indicates that stored data is corrupted.
var ErrDataCorrupt = errors.New("data corrupt")

// ErrInvalidNode indicates that a node cannot be stored.
var ErrInvalidNode = errors.New("invalid node")

// ErrInvalidConfig indicates that a database configuration is invalid.
var ErrInvalidConfig = errors.New("invalid nodestore config")

// ErrClosed indicates that an operation was attempted after Close.
var ErrClosed = kvstore.ErrClosed
