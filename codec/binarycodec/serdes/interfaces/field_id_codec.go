//revive:disable:var-naming
package interfaces

// FieldIDCodec defines methods to encode and decode XRPL field IDs.
type FieldIDCodec interface {
	Encode(fieldName string) ([]byte, error)
	Decode(h string) (string, error)
}
