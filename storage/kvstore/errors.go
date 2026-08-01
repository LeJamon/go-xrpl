package kvstore

import "errors"

// ErrNotFound is returned when a requested key is not present in the store.
var ErrNotFound = errors.New("kvstore: key not found")

var ErrClosed = errors.New("kvstore: store is closed")
