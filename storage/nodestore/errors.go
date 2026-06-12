package nodestore

import "errors"

// ErrDataCorrupt indicates that stored data is corrupted.
var ErrDataCorrupt = errors.New("data corrupt")
