// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package graphics

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProbeABIRealHeader(t *testing.T) {
	source := `#define _DEFAULT_SOURCE 1
#include <vulkan/vulkan_core.h>
#include "vulkan_latency_probe.c"
_Static_assert(sizeof(ProbePhysicalDeviceProperties) == sizeof(VkPhysicalDeviceProperties), "properties size");
_Static_assert(sizeof(ProbePhysicalDeviceProperties2) == sizeof(VkPhysicalDeviceProperties2), "properties2 size");
_Static_assert(offsetof(ProbePhysicalDeviceProperties, limits) == offsetof(VkPhysicalDeviceProperties, limits), "limits offset");
_Static_assert(offsetof(ProbePhysicalDeviceProperties, sparseProperties) == offsetof(VkPhysicalDeviceProperties, sparseProperties), "sparse offset");
_Static_assert(PROBE_ANTI_LAG_FEATURES_STYPE == VK_STRUCTURE_TYPE_PHYSICAL_DEVICE_ANTI_LAG_FEATURES_AMD, "feature type");
_Static_assert(sizeof(ProbeAntiLagFeatures) == sizeof(VkPhysicalDeviceAntiLagFeaturesAMD), "feature size");
_Static_assert(offsetof(ProbeAntiLagFeatures, antiLag) == offsetof(VkPhysicalDeviceAntiLagFeaturesAMD, antiLag), "feature offset");
`
	path := filepath.Join(t.TempDir(), "abi.c")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cc", "-std=c11", "-fsyntax-only", "-I.", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("header ABI: %v\n%s", err, out)
	}
}

func TestProbePropertiesRealDriverCanary(t *testing.T) {
	source := `#define _DEFAULT_SOURCE 1
#include <vulkan/vulkan_core.h>
#include "vulkan_latency_probe.c"
int main(void) {
 void *lib = dlopen("libvulkan.so.1", RTLD_NOW);
 if (!lib) return 77;
 PFN_vkGetInstanceProcAddr gipa = (PFN_vkGetInstanceProcAddr)dlsym(lib, "vkGetInstanceProcAddr");
 if (!gipa) return 77;
 PFN_vkCreateInstance create = (PFN_vkCreateInstance)gipa(NULL, "vkCreateInstance");
 VkInstanceCreateInfo ci = {.sType = VK_STRUCTURE_TYPE_INSTANCE_CREATE_INFO};
 VkInstance inst = VK_NULL_HANDLE;
 if (!create || create(&ci, NULL, &inst) != VK_SUCCESS) return 77;
 PFN_vkEnumeratePhysicalDevices enumerate = (PFN_vkEnumeratePhysicalDevices)gipa(inst, "vkEnumeratePhysicalDevices");
 PFN_vkGetPhysicalDeviceProperties2 get = (PFN_vkGetPhysicalDeviceProperties2)gipa(inst, "vkGetPhysicalDeviceProperties2");
 PFN_vkDestroyInstance destroy = (PFN_vkDestroyInstance)gipa(inst, "vkDestroyInstance");
 uint32_t n = 1; VkPhysicalDevice dev;
 if (!enumerate || !get || enumerate(inst, &n, &dev) < 0 || n == 0) { if (destroy) destroy(inst, NULL); return 77; }
 struct { uint64_t before; ProbePhysicalDeviceProperties2 props; uint64_t after; } guarded = {0};
 guarded.before = 0x123456789abcdef0ULL; guarded.after = 0xfedcba9876543210ULL;
 guarded.props.sType = PROBE_PHYSICAL_DEVICE_PROPERTIES_2_STYPE;
 get(dev, (VkPhysicalDeviceProperties2 *)&guarded.props);
 int bad = guarded.before != 0x123456789abcdef0ULL || guarded.after != 0xfedcba9876543210ULL;
 if (destroy) destroy(inst, NULL);
 dlclose(lib); return bad;
}`
	dir := t.TempDir()
	path := filepath.Join(dir, "driver.c")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "driver")
	cmd := exec.Command("cc", "-std=c11", "-I.", path, "-ldl", "-o", bin)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	out, err := exec.Command(bin).CombinedOutput()
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 77 {
		t.Skip("no usable Vulkan device")
	}
	if err != nil {
		t.Fatalf("real driver canary: %v\n%s", err, out)
	}
}

func TestProbeRejectsMissingOrDisabledFeature(t *testing.T) {
	source := `#define _DEFAULT_SOURCE 1
#include "vulkan_latency_probe.c"
static void props(ProbePhysicalDevice d, ProbePhysicalDeviceProperties *p) { (void)d; p->vendorID=0x1002; strcpy(p->deviceName,"RADV test"); }
static int32_t extensions(ProbePhysicalDevice d, const char *layer, uint32_t *n, ProbeExtensionProperties *p) { (void)d;(void)layer; if (!p) { *n=1; return 0; } strcpy(p[0].extensionName,"VK_AMD_anti_lag"); return 0; }
static void disabled(ProbePhysicalDevice d, ProbePhysicalDeviceFeatures2 *f) { (void)d; ((ProbeAntiLagFeatures *)f->pNext)->antiLag=0; }
int main(void) { ProbeApi api={0}; TipsyAntiLagProbe r={0}; api.properties=props; api.extensions=extensions; if(probe_device(&r,(void *)1,&api)) return 1; memset(&r,0,sizeof(r)); api.features2=disabled; return probe_device(&r,(void *)1,&api); }
`
	dir := t.TempDir()
	path := filepath.Join(dir, "feature.c")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "feature")
	cmd := exec.Command("cc", "-std=c11", "-I.", path, "-ldl", "-o", bin)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	if out, err := exec.Command(bin).CombinedOutput(); err != nil {
		t.Fatalf("unproven feature accepted: %v %s", err, out)
	}
}

func TestProbeRunsAtMostOnce(t *testing.T) {
	antiLagProbeOnce = sync.Once{}
	t.Cleanup(func() { antiLagProbeOnce = sync.Once{} })
	path := t.TempDir() + "/calls"
	t.Setenv("TIPSY_TEST_PROBE_CALLS", path)
	ProbeAntiLagSupport()
	ProbeAntiLagSupportContext(context.Background())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "call\n"); got != 1 {
		t.Fatalf("probe ran %d times, want 1", got)
	}
}

func radvReport() string {
	return "available=1\nneeds_layer_env=0\ndriver=RADV AMD Radeon RX 7800 XT (RADV NAVI32)\n" +
		"vendor=0x1002\nlisted=1\nreason=RADV exposes VK_AMD_anti_lag on an AMD device\n"
}

func TestProbeAcceptsRADVOnAMD(t *testing.T) {
	support := ParseAntiLagProbeReport(radvReport(), nil)
	if !support.Available {
		t.Fatalf("RADV sample rejected: %s", support.Reason)
	}
	if support.NeedsLayerEnv {
		t.Fatal("layer-free sample must not need the layer env")
	}
	if support.Default() {
		t.Fatal("anti-lag must be opt-in: default must stay off even when the probe proved support")
	}
}

func TestProbeAcceptsLayerRetry(t *testing.T) {
	report := "available=1\nneeds_layer_env=1\ndriver=RADV NAVI32\nvendor=0x1002\nlisted=1\n" +
		"reason=VK_AMD_anti_lag needs ENABLE_LAYER_MESA_ANTI_LAG=1\n"
	support := ParseAntiLagProbeReport(report, nil)
	if !support.Available || !support.NeedsLayerEnv {
		t.Fatalf("layer retry must report available + NeedsLayerEnv, got %+v", support)
	}
	// Tipsy does not rewrite the user's environment by itself: without the
	// layer switch the client cannot see the extension, so anti-lag stays off.
	if AntiLagEnabledWithLayer(support, true, "") {
		t.Fatal("layer-only support must not enable anti-lag without the layer env")
	}
	if AntiLagEnabledWithLayer(support, true, "0") {
		t.Fatal("ENABLE_LAYER_MESA_ANTI_LAG=0 must keep anti-lag off")
	}
	// The settings worker sets the layer switch before the client starts.
	if !AntiLagEnabledWithLayer(support, true, "1") {
		t.Fatal("layer-only support must enable anti-lag once the layer env is set")
	}
	if support.ToggleLabel() == "" {
		t.Fatal("toggle label must explain the layer requirement")
	}
}

func TestProbeAcceptsRealRADVDeviceName(t *testing.T) {
	// Observed on RADV (Mesa 25.x) where driverID is 3 and the name is the
	// canonical "AMD ... (RADV NAVI32)" form.
	report := "available=1\nneeds_layer_env=0\ndriver=AMD Radeon RX 7800 XT (RADV NAVI32)\n" +
		"vendor=0x1002\nlisted=1\nreason=RADV exposes VK_AMD_anti_lag on an AMD device\n"
	support := ParseAntiLagProbeReport(report, nil)
	if !support.Available || support.NeedsLayerEnv {
		t.Fatalf("real RADV report rejected: %+v", support)
	}
	if !AntiLagEnabled(support, true) {
		t.Fatal("proved RADV support plus env switch must enable anti-lag")
	}
}

func TestProbeRejectsNonRADVAndSoftware(t *testing.T) {
	cases := []struct {
		name   string
		report string
	}{
		{"lavapipe", "available=1\nneeds_layer_env=0\ndriver=llvmpipe (LLVM 18.1.8, 256 bits)\nvendor=0x10005\nlisted=1\n"},
		{"llvmpipe", "available=1\nneeds_layer_env=0\ndriver=llvmpipe\nvendor=0x1002\nlisted=1\n"},
		{"intel", "available=1\nneeds_layer_env=0\ndriver=Intel(R) Arc(tm) A770\nvendor=0x8086\nlisted=1\n"},
		{"nvidia", "available=1\nneeds_layer_env=0\ndriver=NVIDIA GeForce RTX 4070\nvendor=0x10de\nlisted=1\n"},
		{"amdvlk", "available=1\nneeds_layer_env=0\ndriver=AMD Radeon RX 6800 XT\nvendor=0x1002\nlisted=1\n"},
		{"listed-only", "available=1\nneeds_layer_env=0\ndriver=RADV NAVI32\nvendor=0x1002\nlisted=0\n"},
	}
	for _, c := range cases {
		support := ParseAntiLagProbeReport(c.report, nil)
		if support.Available {
			t.Errorf("%s: probe accepted an ineligible device", c.name)
		}
		if support.Default() {
			t.Errorf("%s: anti-lag must not default on", c.name)
		}
	}
}

func TestProbeFailsClosed(t *testing.T) {
	for _, c := range []struct {
		name   string
		report string
		err    error
	}{
		{"empty", "", errors.New("exit status 1")},
		{"unavailable", "available=0\ndriver=llvmpipe\nvendor=0x10005\nreason=no eligible AMD device\n",
			errors.New("exit status 1")},
		{"truncated", "driver=RADV NAVI32\n", nil},
		{"exit-with-available", radvReport(), errors.New("exit status 1")},
	} {
		support := ParseAntiLagProbeReport(c.report, c.err)
		if support.Available {
			t.Errorf("%s: failing helper must report unavailable", c.name)
		}
		if support.Reason == "" {
			t.Errorf("%s: unavailable result must carry a reason", c.name)
		}
	}
}

func TestAntiLagEnabledNeedsEnvAndProof(t *testing.T) {
	proved := AntiLagSupport{Available: true}
	missing := AntiLagSupport{Reason: "no device"}
	if AntiLagEnabled(proved, false) {
		t.Fatal("TIPSY_ANTILAG unset must disable anti-lag")
	}
	if AntiLagEnabled(missing, true) {
		t.Fatal("unproved support must disable anti-lag")
	}
	if !AntiLagEnabled(proved, true) {
		t.Fatal("proved support plus the env switch must enable anti-lag")
	}
}

func TestAntiLagEnabledLayerNeedsEnvDevice(t *testing.T) {
	// A NeedsLayerEnv result with the layer switch in the process env is what
	// the settings worker produces once it enables the implicit layer.
	layer := AntiLagSupport{Available: true, NeedsLayerEnv: true, Driver: "RADV NAVI32"}
	t.Setenv(EnvAntiLagLayer, "1")
	if !AntiLagEnabled(layer, true) {
		t.Fatal("a NeedsLayerEnv device with the layer env set must be enabled")
	}
	if AntiLagEnabled(layer, false) {
		t.Fatal("TIPSY_ANTILAG unset must still disable anti-lag")
	}
	t.Setenv(EnvAntiLagLayer, "")
	if AntiLagEnabled(layer, true) {
		t.Fatal("the layer env unset must keep a NeedsLayerEnv device off")
	}
	// A layer-free device must not depend on the Mesa switch at all.
	free := AntiLagSupport{Available: true, Driver: "RADV NAVI32"}
	if !AntiLagEnabled(free, true) {
		t.Fatal("layer-free support must enable anti-lag without the layer env")
	}
}

func TestAntiLagRequestedParsesToggle(t *testing.T) {
	for _, value := range []string{"", "0", "false", "no", "off"} {
		if AntiLagRequested(value) {
			t.Fatalf("%q must not request anti-lag", value)
		}
	}
	for _, value := range []string{"1", "true", "YES", " on "} {
		if !AntiLagRequested(value) {
			t.Fatalf("%q must request anti-lag", value)
		}
	}
}

func TestProbeHelperHonoursContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	support := ProbeAntiLagSupportContext(ctx)
	if support.Available {
		t.Fatal("an expired deadline must not enable anti-lag")
	}
	if support.Reason == "" {
		t.Fatal("expired deadline must carry a reason")
	}
}

func TestProbeHelperRunsWithUserEnv(t *testing.T) {
	// TestMain answers AntiLagProbeCommand; this asserts the child path works
	// and that the parent's environment is untouched.
	t.Setenv("ENABLE_LAYER_MESA_ANTI_LAG", "")
	support := ProbeAntiLagSupport()
	if support.Available {
		t.Fatal("a non-helper binary must not report anti-lag support")
	}
	if os.Getenv("ENABLE_LAYER_MESA_ANTI_LAG") != "" {
		t.Fatal("the probe must not set the layer env in the parent")
	}
}

// TestMain serves AntiLagProbeCommand so the subprocess path is exercised
// without a GPU. It reports no eligible device, which must fail closed.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == AntiLagProbeCommand {
		if path := os.Getenv("TIPSY_TEST_PROBE_CALLS"); path != "" {
			f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				os.Exit(2)
			}
			_, _ = f.WriteString("call\n")
			_ = f.Close()
		}
		os.Stdout.WriteString("available=0\nneeds_layer_env=0\ndriver=llvmpipe\n" +
			"vendor=0x10005\nlisted=0\nreason=software rasteriser is not eligible for anti-lag\n")
		os.Exit(1)
	}
	os.Exit(m.Run())
}
