package escrow

// Pre-computed crypto conditions and fulfillments for testing escrows.
// These are derived from rippled's test vectors in src/test/app/Escrow_test.cpp
// and the crypto-condition specifications.
//
// Crypto-conditions use the Interledger crypto-conditions specification.
// For escrows, XRPL supports PREIMAGE-SHA-256 conditions (type 0).
//
// Condition format (PREIMAGE-SHA-256):
//   - 0xA0: Type prefix for PREIMAGE-SHA-256
//   - 0x25: Length of condition (37 bytes)
//   - 0x80, 0x20: Fingerprint tag + length (32 bytes)
//   - <32 bytes>: SHA-256 hash of the preimage
//   - 0x81, 0x01: Cost tag + length (1 byte)
//   - <cost>: Fulfillment length
//
// Fulfillment format (PREIMAGE-SHA-256):
//   - 0xA0: Type prefix for PREIMAGE-SHA-256
//   - <length>: VarInt length of payload
//   - 0x80: Preimage tag
//   - <length>: Preimage length
//   - <preimage>: The actual preimage bytes

var (
	// TestCondition1 is the crypto-condition for an empty preimage.
	// Fulfillment: Empty string ""
	// This is cb1 from rippled's escrow tests.
	testCondition1 = []byte{
		0xA0, 0x25, 0x80, 0x20,
		0xE3, 0xB0, 0xC4, 0x42, 0x98, 0xFC, 0x1C, 0x14,
		0x9A, 0xFB, 0xF4, 0xC8, 0x99, 0x6F, 0xB9, 0x24,
		0x27, 0xAE, 0x41, 0xE4, 0x64, 0x9B, 0x93, 0x4C,
		0xA4, 0x95, 0x99, 0x1B, 0x78, 0x52, 0xB8, 0x55,
		0x81, 0x01, 0x00,
	}

	// TestFulfillment1 is the fulfillment for TestCondition1 (empty preimage).
	// This is fb1 from rippled's escrow tests.
	testFulfillment1 = []byte{
		0xA0, 0x02, 0x80, 0x00,
	}

	// TestCondition2 is the crypto-condition for preimage "aaa".
	// Fulfillment: "aaa" (0x61, 0x61, 0x61)
	// This is cb2 from rippled's escrow tests.
	testCondition2 = []byte{
		0xA0, 0x25, 0x80, 0x20,
		0x98, 0x34, 0x87, 0x6D, 0xCF, 0xB0, 0x5C, 0xB1,
		0x67, 0xA5, 0xC2, 0x49, 0x53, 0xEB, 0xA5, 0x8C,
		0x4A, 0xC8, 0x9B, 0x1A, 0xDF, 0x57, 0xF2, 0x8F,
		0x2F, 0x9D, 0x09, 0xAF, 0x10, 0x7E, 0xE8, 0xF0,
		0x81, 0x01, 0x03,
	}

	// TestFulfillment2 is the fulfillment for TestCondition2 (preimage "aaa").
	// This is fb2 from rippled's escrow tests.
	testFulfillment2 = []byte{
		0xA0, 0x05, 0x80, 0x03, 0x61, 0x61, 0x61,
	}

	// TestCondition3 is the crypto-condition for preimage "nikb".
	// Fulfillment: "nikb" (0x6E, 0x69, 0x6B, 0x62)
	// This is cb3 from rippled's escrow tests.
	testCondition3 = []byte{
		0xA0, 0x25, 0x80, 0x20,
		0x6E, 0x4C, 0x71, 0x45, 0x30, 0xC0, 0xA4, 0x26,
		0x8B, 0x3F, 0xA6, 0x3B, 0x1B, 0x60, 0x6F, 0x2D,
		0x26, 0x4A, 0x2D, 0x85, 0x7B, 0xE8, 0xA0, 0x9C,
		0x1D, 0xFD, 0x57, 0x0D, 0x15, 0x85, 0x8B, 0xD4,
		0x81, 0x01, 0x04,
	}

	// TestFulfillment3 is the fulfillment for TestCondition3 (preimage "nikb").
	// This is fb3 from rippled's escrow tests.
	testFulfillment3 = []byte{
		0xA0, 0x06, 0x80, 0x04, 0x6E, 0x69, 0x6B, 0x62,
	}

	// TestConditionInvalid is an invalid crypto-condition for negative testing.
	// This should fail validation.
	testConditionInvalid = []byte{
		0xA0, 0x25, 0x80, 0x20,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x81, 0x01, 0x00,
	}

	// TestFulfillmentWrong is a valid fulfillment that doesn't match any test conditions.
	// Use this to test mismatched condition/fulfillment pairs.
	testFulfillmentWrong = []byte{
		0xA0, 0x05, 0x80, 0x03, 0x62, 0x62, 0x62, // preimage "bbb"
	}
)

// TestCondition1 returns the empty-preimage condition.
func TestCondition1() []byte { return append([]byte(nil), testCondition1...) }

// TestFulfillment1 returns the empty-preimage fulfillment.
func TestFulfillment1() []byte { return append([]byte(nil), testFulfillment1...) }

// TestCondition2 returns the condition for preimage "aaa".
func TestCondition2() []byte { return append([]byte(nil), testCondition2...) }

// TestFulfillment2 returns the fulfillment for preimage "aaa".
func TestFulfillment2() []byte { return append([]byte(nil), testFulfillment2...) }

// TestCondition3 returns the condition for preimage "nikb".
func TestCondition3() []byte { return append([]byte(nil), testCondition3...) }

// TestFulfillment3 returns the fulfillment for preimage "nikb".
func TestFulfillment3() []byte { return append([]byte(nil), testFulfillment3...) }

func TestConditionInvalid() []byte { return append([]byte(nil), testConditionInvalid...) }

// TestFulfillmentWrong returns a fulfillment that matches none of the conditions.
func TestFulfillmentWrong() []byte { return append([]byte(nil), testFulfillmentWrong...) }
