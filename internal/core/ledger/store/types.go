package store

type Status int

const (
	StatusOK Status = iota
	StatusNotFound
	StatusDataCorrupt
	StatusUnknown
	StatusBackendError
	StatusCustomCode = 100
)
