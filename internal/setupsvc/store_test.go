// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"testing"

	"github.com/tipsy-linux/tipsy/internal/integrity"
)

func TestStoreForTrustKeepsOfficialAndDevelopmentSlotsApart(t *testing.T) {
	official := StoreForTrust("/tmp/store", KeylessReleaseTrustPolicy())
	if official.Root != "/tmp/store" || official.ActiveSlot != integrity.ActiveSlotOfficial {
		t.Fatalf("official store = %+v", official)
	}
	dev := StoreForTrust("/tmp/store", DevelopmentTrustPolicy())
	if dev.Root != "/tmp/store" || dev.ActiveSlot != integrity.ActiveSlotDevelopment {
		t.Fatalf("development store = %+v", dev)
	}
	if official.ActiveSlot == dev.ActiveSlot {
		t.Fatal("official and development launches must not share an active pointer")
	}
}
