package kvstore

import "errors"

// ErrNotFound is returned when a requested key is not present in the store.
var ErrNotFound = errors.New("kvstore: key not found")

// ErrClosed is returned when an operation is attempted on a closed resource.
var ErrClosed = errors.New("kvstore: store is closed")

// ErrValueTooLarge means a bounded read declined a value before copying it.
var ErrValueTooLarge = errors.New("kvstore: value exceeds read allowance")
