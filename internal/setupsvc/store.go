// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package setupsvc

import "github.com/tipsy-linux/tipsy/internal/integrity"

// StoreForTrust returns the generation store with the active pointer that
// matches trust.
func StoreForTrust(root string, trust TrustPolicy) integrity.Store {
	return integrity.Store{
		Root:       root,
		ActiveSlot: activeSlotForTrust(trust),
	}
}
