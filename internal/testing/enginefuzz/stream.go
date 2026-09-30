package enginefuzz

import "encoding/binary"

type stream struct {
	data []byte
	pos  int
}

func (s *stream) drained() bool { return s.pos >= len(s.data) }

func (s *stream) offset() int { return s.pos }

func (s *stream) u8() byte {
	if s.drained() {
		s.pos++
		return 0
	}
	b := s.data[s.pos]
	s.pos++
	return b
}

func (s *stream) u32() uint32 {
	var b [4]byte
	for i := range b {
		b[i] = s.u8()
	}
	return binary.BigEndian.Uint32(b[:])
}

func (s *stream) u64() uint64 {
	var b [8]byte
	for i := range b {
		b[i] = s.u8()
	}
	return binary.BigEndian.Uint64(b[:])
}

func (s *stream) index(n int) int {
	if n <= 0 {
		panic("enginefuzz: index bound must be positive")
	}
	return int(s.u8()) % n
}

func (s *stream) bounded(n uint64) uint64 {
	if n == 0 {
		panic("enginefuzz: bounded range must be positive")
	}
	return s.u64() % n
}

func (s *stream) chance(n byte) bool { return s.u8() < n }
