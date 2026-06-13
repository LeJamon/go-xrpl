// Copyright (c) 2024-2025. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package amendment

// NFTsWithDynamicEnabled returns true if NFTs with dynamic features are enabled.
func (r *Rules) NFTsWithDynamicEnabled() bool {
	return r.NFTsEnabled() && r.Enabled(FeatureDynamicNFT)
}
