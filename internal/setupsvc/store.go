// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import "github.com/tipsy-linux/tipsy/internal/integrity"

// StoreForTrust returns the generation store with the active pointer that
// matches trust. OfficialVerified and DevelopmentUnrestricted keep separate
// active records so the live client and a development build can share one
// retained-APK pool without overwriting each other.
func StoreForTrust(root string, trust TrustPolicy) integrity.Store {
	store := integrity.Store{Root: root}
	switch trust.Mode {
	case OfficialVerified:
		store.ActiveSlot = integrity.ActiveSlotOfficial
	case DevelopmentUnrestricted:
		store.ActiveSlot = integrity.ActiveSlotDevelopment
	}
	return store
}
