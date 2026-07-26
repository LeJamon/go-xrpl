package shamap

func sme_keyFromByte(b byte) [32]byte {
	var k [32]byte
	k[0] = b
	return k
}

func sme_keyFromTwo(hi, lo byte) [32]byte {
	var k [32]byte
	k[0] = hi
	k[1] = lo
	return k
}

func sme_data12(b byte) []byte {
	d := make([]byte, 12)
	for i := range d {
		d[i] = b
	}
	return d
}
