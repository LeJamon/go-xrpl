package consensus

import "github.com/LeJamon/goXRPLd/codec/addresscodec"

// CalcNodeID derives a 20-byte NodeID from a 33-byte compressed master
// public key as RIPEMD-160(SHA-256(pubkey)). Mirrors rippled's
// calcNodeID at rippled/src/libxrpl/protocol/PublicKey.cpp:319-327
// (and the NodeID type definition at
// rippled/include/xrpl/protocol/UintTypes.h:59 — base_uint<160>).
//
// The input is the compressed-form pubkey including the type prefix
// byte: 0x02/0x03 for secp256k1 and 0xED for ed25519. Rippled hashes
// the prefix-included bytes verbatim, so cross-implementation NodeID
// values agree byte-for-byte for the same master key.
func CalcNodeID(masterPub [33]byte) NodeID {
	h := addresscodec.Sha256RipeMD160(masterPub[:])
	var id NodeID
	copy(id[:], h)
	return id
}
