// Package drops provides type-safe XRP amount arithmetic.
//
// One XRP equals 1,000,000 drops, the smallest indivisible unit of XRP.
// The XRPAmount type wraps a signed int64 count of drops (mirroring rippled's
// XRPAmount) and provides arithmetic operations, comparison methods, and
// conversions. The package also includes fee calculation utilities used by the
// transaction engine to compute base fees and reserve requirements.
package drops
