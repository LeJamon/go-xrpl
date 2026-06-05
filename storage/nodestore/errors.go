package nodestore

import "errors"

var (
	// ErrNotFound is returned when a requested node is not present in the store.
	ErrNotFound = errors.New("node not found")

	// ErrDataCorrupt indicates that stored data is corrupted.
	ErrDataCorrupt = errors.New("data corrupt")

	// ErrBackendClosed indicates that the backend is closed.
	ErrBackendClosed = errors.New("backend closed")

	// ErrInvalidNode indicates that a node is invalid.
	ErrInvalidNode = errors.New("invalid node")

	// ErrInvalidHash indicates that a hash is invalid.
	ErrInvalidHash = errors.New("invalid hash")

	// ErrInvalidConfig indicates that the configuration is invalid.
	ErrInvalidConfig = errors.New("invalid config")

	// ErrShutdown indicates that the database is shutting down.
	ErrShutdown = errors.New("nodestore shutdown")
)
