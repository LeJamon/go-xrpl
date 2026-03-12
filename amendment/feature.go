// Copyright (c) 2024-2025. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package amendment

import (
	"crypto/sha512"
)

// VoteBehavior defines how a node votes on an amendment by default.
type VoteBehavior int

const (
	// VoteDefaultNo means the amendment is not voted for by default.
	VoteDefaultNo VoteBehavior = iota
	// VoteDefaultYes means the amendment is voted for by default.
	VoteDefaultYes
	// VoteObsolete means the amendment is obsolete and should not be voted on.
	VoteObsolete
)

// Supported indicates whether the node's code supports an amendment.
type Supported int

const (
	// SupportedNo means the amendment is not supported by this code.
	SupportedNo Supported = iota
	// SupportedYes means the amendment is supported by this code.
	SupportedYes
)

// Feature represents an XRP Ledger amendment/feature.
type Feature struct {
	// Name is the human-readable name of the feature.
	Name string
	// ID is the SHA-512 half of the feature name, used as unique identifier.
	ID [32]byte
	// Supported indicates if this code supports the feature.
	Supported Supported
	// Vote is the default voting behavior for this feature.
	Vote VoteBehavior
	// Retired indicates if this feature has been active for at least two years
	// and its pre-amendment code has been removed.
	Retired bool
}

// SHA512Half computes the SHA-512 hash and returns the first 32 bytes (256 bits).
// This is the standard hash function used for XRP Ledger identifiers.
func SHA512Half(data []byte) [32]byte {
	hash := sha512.Sum512(data)
	var result [32]byte
	copy(result[:], hash[:32])
	return result
}

// FeatureID computes the feature ID from a feature name.
func FeatureID(name string) [32]byte {
	return SHA512Half([]byte(name))
}

// IsSupported returns true if the feature is supported by this code.
func (f *Feature) IsSupported() bool {
	return f.Supported == SupportedYes
}

// IsDefaultYes returns true if the feature should be voted for by default.
func (f *Feature) IsDefaultYes() bool {
	return f.Vote == VoteDefaultYes
}

// IsObsolete returns true if the feature is obsolete.
func (f *Feature) IsObsolete() bool {
	return f.Vote == VoteObsolete
}

// String returns the feature name.
func (f *Feature) String() string {
	return f.Name
}
