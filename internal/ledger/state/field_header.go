package state

// parseFieldHeader decodes the type and field codes of a binary field header
// at offset, handling extended (low-nibble zero) codes. ok is false when the
// data is truncated mid-header; newOffset always reflects the bytes consumed.
func parseFieldHeader(data []byte, offset int) (typeCode, fieldCode byte, newOffset int, ok bool) {
	if offset >= len(data) {
		return 0, 0, offset, false
	}
	header := data[offset]
	offset++

	typeCode = (header >> 4) & 0x0F
	fieldCode = header & 0x0F

	if typeCode == 0 {
		if offset >= len(data) {
			return typeCode, fieldCode, offset, false
		}
		typeCode = data[offset]
		offset++
	}

	if fieldCode == 0 {
		if offset >= len(data) {
			return typeCode, fieldCode, offset, false
		}
		fieldCode = data[offset]
		offset++
	}

	return typeCode, fieldCode, offset, true
}
