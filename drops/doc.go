// Package drops provides type-safe XRP amount arithmetic using uint64 drops.
//
// One XRP equals 1,000,000 drops, the smallest indivisible unit of XRP.
// The Drops type wraps a uint64 value and provides safe arithmetic operations,
// comparison methods, and conversions. It also includes fee calculation
// utilities used by the transaction engine to compute base fees and
// reserve requirements.
package drops
