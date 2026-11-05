// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"os"
	"testing"
	"unsafe"
)

// The anti-lag unit: every case drives the real C gate and the real
// present/device-create hooks through vulkan_latency_go.go.

// testDevice hands out one live C allocation per test.
func testDevice(t *testing.T) unsafe.Pointer {
	t.Helper()
	device := tipsyTestFakeDevice()
	t.Cleanup(func() { tipsyTestFreeDevice(device) })
	return device
}

// armTestAntiLag opens the gate and returns the device the record belongs to.
func armTestAntiLag(t *testing.T) unsafe.Pointer {
	t.Helper()
	device := testDevice(t)
	if !tipsyTestArmAntiLag("radv", true, device) {
		t.Fatal("arm must open the gate for a proven RADV device")
	}
	return device
}

func resetLatencyTest(t *testing.T) {
	t.Helper()
	tipsyTestLatencyReset()
	tipsyTestLatencyInstallProcs()
	t.Cleanup(tipsyTestLatencyReset)
}

func TestAntiLagNeverCallsWithoutEnv(t *testing.T) {
	resetLatencyTest(t)
	t.Setenv("TIPSY_ANTILAG", "")
	if tipsyTestArmAntiLag("radv", true, testDevice(t)) {
		t.Fatal("gate must stay closed without TIPSY_ANTILAG=1")
	}
	if tipsyTestAntiLagUpdate(testDevice(t)) {
		t.Fatal("no update may be issued with the gate closed")
	}
	if got := tipsyTestAntiLagUpdateCount(); got != 0 {
		t.Fatalf("driver saw %d anti-lag calls; want 0", got)
	}
}

func TestAntiLagRejectsNonRADVAndUnproven(t *testing.T) {
	resetLatencyTest(t)
	t.Setenv("TIPSY_ANTILAG", "1")
	for _, driver := range []string{"lavapipe", "llvmpipe", "intel", ""} {
		if tipsyTestArmAntiLag(driver, true, testDevice(t)) {
			t.Fatalf("gate opened for driver %q", driver)
		}
	}
	if tipsyTestArmAntiLag("radv", false, testDevice(t)) {
		t.Fatal("gate opened without a probe proof")
	}
	if tipsyTestAntiLagUpdate(testDevice(t)) {
		t.Fatal("update must not run with the gate closed")
	}
	if tipsyTestAntiLagUpdateCount() != 0 {
		t.Fatal("driver must not see any anti-lag call")
	}
}

func TestAntiLagUpdateSendsONWithDefaults(t *testing.T) {
	resetLatencyTest(t)
	t.Setenv("TIPSY_ANTILAG", "1")
	device := armTestAntiLag(t)
	if !tipsyTestAntiLagUpdate(device) {
		t.Fatal("armed device must receive an update")
	}
	if got := tipsyTestAntiLagUpdateCount(); got != 1 {
		t.Fatalf("driver calls = %d; want 1", got)
	}
	if got := tipsyTestAntiLagMode(); got != 1 {
		t.Fatalf("mode = %d; want ON(1)", got)
	}
	if got := tipsyTestAntiLagMaxFPS(); got != 0 {
		t.Fatalf("maxFPS = %d; want 0", got)
	}
	if !tipsyTestAntiLagPresentationNull() {
		t.Fatal("pPresentationInfo must be NULL")
	}
}

func TestDeviceCreateWithoutExtensionClosesSoftly(t *testing.T) {
	resetLatencyTest(t)
	t.Setenv("TIPSY_ANTILAG", "1")
	armedDevice := armTestAntiLag(t)
	prepared, added := tipsyTestPrepareDeviceCreate(false, false, false)
	if prepared || added {
		t.Fatal("no injection when the device lacks VK_AMD_anti_lag")
	}
	if VulkanAntiLagArmed() {
		t.Fatal("gate must close after a disagreement")
	}
	if tipsyTestAntiLagUpdate(armedDevice) {
		t.Fatal("update must stay off after the soft close")
	}
	if tipsyTestAntiLagUpdateCount() != 0 {
		t.Fatal("driver must not see an anti-lag call after the close")
	}
}

func TestDeviceCreateInjectsExtensionWhenSupported(t *testing.T) {
	resetLatencyTest(t)
	t.Setenv("TIPSY_ANTILAG", "1")
	armTestAntiLag(t)
	prepared, added := tipsyTestPrepareDeviceCreate(true, false, false)
	if !prepared || !added {
		t.Fatal("injection expected when the device exposes VK_AMD_anti_lag")
	}
	if !VulkanAntiLagArmed() {
		t.Fatal("gate must stay armed after a successful injection")
	}
}

func TestDeviceCreateRespectsClientAntiLagStruct(t *testing.T) {
	resetLatencyTest(t)
	t.Setenv("TIPSY_ANTILAG", "1")
	armTestAntiLag(t)
	/* Client already opted in: forward its chain, stay armed. */
	if prepared, _ := tipsyTestPrepareDeviceCreate(true, true, true); prepared {
		t.Fatal("client antiLag chain must be forwarded unchanged")
	}
	if !VulkanAntiLagArmed() {
		t.Fatal("a client opt-in must not close our gate")
	}
	/* Client disables it: shadowing would change its request, so turn off. */
	if prepared, _ := tipsyTestPrepareDeviceCreate(true, true, false); prepared {
		t.Fatal("client antiLag=0 must not be overridden")
	}
	if VulkanAntiLagArmed() {
		t.Fatal("client antiLag=0 must close the gate softly")
	}
}

func TestInstanceCreationGuardClosesOnce(t *testing.T) {
	resetLatencyTest(t)
	if !tipsyTestInstanceCreationBegin() {
		t.Fatal("first instance creation must close the guard")
	}
	if tipsyTestInstanceCreationBegin() {
		t.Fatal("guard must fire once")
	}
	if !tipsyTestInstanceCreationClosed() {
		t.Fatal("guard must report closed")
	}
}

func TestShaderCacheLogNeverSetsEnv(t *testing.T) {
	resetLatencyTest(t)
	t.Setenv("MESA_SHADER_CACHE_DIR", "/var/tmp/mesa")
	t.Setenv("MESA_SHADER_CACHE_DISABLE", "")
	tipsyTestLogShaderCache()
	tipsyTestLogShaderCache()
	if got := os.Getenv("MESA_SHADER_CACHE_DIR"); got != "/var/tmp/mesa" {
		t.Fatalf("shader-cache env changed: %q", got)
	}
	if got := os.Getenv("MESA_SHADER_CACHE_DISABLE"); got != "" {
		t.Fatalf("shader-cache disable env set: %q", got)
	}
}

func TestClassifyAcceptsOnlyRADVOnAMD(t *testing.T) {
	resetLatencyTest(t)
	cases := []struct {
		driver   string
		vendor   uint32
		listed   bool
		software bool
		want     bool
	}{
		{"RADV", 0x1002, true, false, true},
		{"lavapipe", 0x10005, true, true, false},
		{"llvmpipe", 0x10005, true, true, false},
		{"amd", 0x1002, true, false, false},
		{"radv", 0x8086, true, false, false},
		{"radv", 0x1002, false, false, false},
	}
	for _, c := range cases {
		driver, accept := tipsyTestClassify(c.driver, c.vendor, c.listed, c.software)
		if accept != c.want {
			t.Errorf("classify(%q, 0x%x, listed=%v, sw=%v) = %v; want %v",
				c.driver, c.vendor, c.listed, c.software, accept, c.want)
		}
		if driver != lowerASCII(c.driver) {
			t.Errorf("classify(%q) driver = %q; want %q", c.driver, driver, lowerASCII(c.driver))
		}
	}
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c - 'A' + 'a'
		}
	}
	return string(b)
}

// The structure types this unit puts on the wire are checked against the
// Vulkan headers by TestVulkanLatencyCBuiltinsMatchHeaders in
// vulkan_latency_abi_test.go.

func TestChainWalkIsBoundedAndReadOnly(t *testing.T) {
	resetLatencyTest(t)
	// A five-node chain carrying sType 0xa000..0xa004 is walked read-only.
	antiLag := vulkanHeaderValue(t, "anti_lag_features")
	if !tipsyTestLatencyChainFinds(5, 0xa000+4) {
		t.Fatal("chain walk must find a type in a linear chain")
	}
	if tipsyTestLatencyChainFinds(5, antiLag) {
		t.Fatal("chain walk reported a type the chain does not carry")
	}
	if tipsyTestLatencyChainFinds(0, 0xa000) {
		t.Fatal("an empty chain must report nothing")
	}
}

// TestAntiLagResolvesAfterDeviceCreate drives the real prepare hook and the
// real resolve hook in the order vulkan.c uses them, with a fake
// vkGetDeviceProcAddr that fails for any handle but the created device. The
// lookup must never happen during prepare, and the entry must come from the
// device the host vkCreateDevice returned.
func TestAntiLagResolvesAfterDeviceCreate(t *testing.T) {
	t.Setenv("TIPSY_ANTILAG", "1")
	resetLatencyTest(t)
	armTestAntiLag(t)
	fake := newFakeDeviceGDPA(t)
	fake.resolve = true
	installFakeDeviceGDPA(t, fake)

	prepared, added := tipsyTestPrepareDeviceCreate(true, false, false)
	if fake.lookups != 0 || fake.failed != 0 {
		t.Fatalf("prepare called vkGetDeviceProcAddr %d times (%d foreign handles)", fake.lookups, fake.failed)
	}
	if !prepared || !added {
		t.Fatal("injection expected: the chain carries VK_AMD_anti_lag")
	}
	if !VulkanAntiLagArmed() {
		t.Fatal("prepare must keep the gate armed")
	}

	// Host vkCreateDevice fails: no lookup may follow.
	fake.create(false)
	if fake.lookups != 0 || fake.failed != 0 {
		t.Fatal("no device-proc lookup may follow a failed vkCreateDevice")
	}

	fake.create(true)
	ok, update := tipsyTestResolveAntiLag(fake.device())
	if !ok || update == nil {
		t.Fatal("resolve must return the created device's vkAntiLagUpdateAMD")
	}
	if fake.failed != 0 {
		t.Fatalf("vkGetDeviceProcAddr got %d handles other than the created device", fake.failed)
	}
	if !VulkanAntiLagArmed() {
		t.Fatal("a resolved entry must keep anti-lag armed")
	}
}

// TestAntiLagUnresolvedClosesSoftly: a NULL lookup closes anti-lag with one
// soft close and no second log.
func TestAntiLagUnresolvedClosesSoftly(t *testing.T) {
	t.Setenv("TIPSY_ANTILAG", "1")
	resetLatencyTest(t)
	armTestAntiLag(t)
	fake := newFakeDeviceGDPA(t)
	installFakeDeviceGDPA(t, fake)
	fake.create(true)

	ok, update := tipsyTestResolveAntiLag(fake.device())
	if ok || update != nil {
		t.Fatal("an unresolved vkAntiLagUpdateAMD must not be reported")
	}
	if VulkanAntiLagArmed() {
		t.Fatal("a NULL lookup must close anti-lag")
	}
	if got := tipsyTestAntiLagSoftCloses(); got != 1 {
		t.Fatalf("soft closes = %d; want 1", got)
	}
	ok, update = tipsyTestResolveAntiLag(fake.device())
	if ok || update != nil || tipsyTestAntiLagSoftCloses() != 1 {
		t.Fatal("a closed gate must log nothing more")
	}
}

// vulkanHeaderValue returns a header constant from the table in
// vulkan_latency_abi_test.go.
func vulkanHeaderValue(t *testing.T, name string) uint32 {
	t.Helper()
	for _, entry := range vulkanHeaderABI {
		if entry.name == name {
			return entry.value
		}
	}
	t.Fatalf("no header value named %q", name)
	return 0
}

// BenchmarkAntiLagOffPresent measures the present entry with the gate closed
// (the off path). The C loop keeps cgo overhead out of the per-present number.
func BenchmarkAntiLagOffPresent(b *testing.B) {
	b.Setenv("TIPSY_ANTILAG", "")
	tipsyTestLatencyReset()
	tipsyTestLatencyInstallProcs()
	b.Cleanup(tipsyTestLatencyReset)
	queue := tipsyTestFakeDevice()
	b.Cleanup(func() { tipsyTestFreeDevice(queue) })
	const iterations = 4096
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tipsyTestPresentLoop(queue, iterations)
	}
	b.StopTimer()
	per := float64(b.Elapsed().Nanoseconds()) / float64(int64(b.N)*iterations)
	b.ReportMetric(per, "ns/op")
}
