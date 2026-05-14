package definitions

import (
	"encoding/json"
	"fmt"
)

// FieldInstance is a struct that represents a field instance.
type FieldInstance struct {
	FieldName string
	*FieldInfo
	FieldHeader *FieldHeader
	Ordinal     int32
}

// FieldInfo is a struct that represents the field info.
type FieldInfo struct {
	Nth            int32 `json:"nth"`
	IsVLEncoded    bool  `json:"isVLEncoded"`
	IsSerialized   bool  `json:"isSerialized"`
	IsSigningField bool  `json:"isSigningField"`
	Type           string `json:"type"`
}

// FieldHeader is a struct that represents the field header.
type FieldHeader struct {
	TypeCode  int32
	FieldCode int32
}

// CreateFieldHeader creates a new field header.
func (d *Definitions) CreateFieldHeader(tc, fc int32) FieldHeader {
	return FieldHeader{
		TypeCode:  tc,
		FieldCode: fc,
	}
}

type fieldInstanceMap map[string]*FieldInstance

// UnmarshalJSON decodes the FIELDS array of `[name, fieldInfo]` pairs from
// definitions.json into a flat name -> FieldInstance map.
func (fi *fieldInstanceMap) UnmarshalJSON(data []byte) error {
	var pairs []json.RawMessage
	if err := json.Unmarshal(data, &pairs); err != nil {
		return fmt.Errorf("FIELDS: %w", err)
	}
	m := make(fieldInstanceMap, len(pairs))
	for i, raw := range pairs {
		var pair [2]json.RawMessage
		if err := json.Unmarshal(raw, &pair); err != nil {
			return fmt.Errorf("FIELDS[%d]: %w", i, err)
		}
		var name string
		if err := json.Unmarshal(pair[0], &name); err != nil {
			return fmt.Errorf("FIELDS[%d].name: %w", i, err)
		}
		info := &FieldInfo{}
		if err := json.Unmarshal(pair[1], info); err != nil {
			return fmt.Errorf("FIELDS[%d].info (%q): %w", i, name, err)
		}
		m[name] = &FieldInstance{
			FieldName: name,
			FieldInfo: info,
			Ordinal:   info.Nth,
		}
	}
	*fi = m
	return nil
}
