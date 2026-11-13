// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPresentOffPathChecksGateBeforeLatencyCall(t *testing.T) {
	data, err := os.ReadFile("vulkan.c")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start := strings.Index(source, "// Opt-in anti-lag update and present pacing.")
	if start < 0 {
		start = strings.Index(source, "// Opt-in anti-lag update.")
	}
	if start < 0 {
		t.Fatal("present hook not found")
	}
	end := strings.Index(source[start:], "tipsy_vk_output_present(")
	if end < 0 {
		t.Fatal("present output not found")
	}
	section := source[start : start+end]
	gate := strings.Index(section, "if (tipsy_vk_antilag_armed())")
	call := strings.Index(section, "tipsy_vk_latency_present(")
	if gate < 0 || call < 0 || gate > call {
		t.Fatal("present latency call executes before armed check")
	}
}

func TestAntiLagEnvTruthMatchesGraphics(t *testing.T) {
	source := `#define _POSIX_C_SOURCE 200809L
#include "vulkan_latency.c"
int main(void) {
 setenv("TIPSY_ANTILAG", " YES ", 1);
 if (!tipsy_vk_antilag_requested()) return 1;
 setenv("TIPSY_ANTILAG", "false", 1);
 return tipsy_vk_antilag_requested() ? 2 : 0;
}`
	dir := t.TempDir()
	path := filepath.Join(dir, "env.c")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "env")
	if out, err := exec.Command("cc", "-std=c11", "-ffunction-sections", "-fdata-sections", "-Wl,--gc-sections", "-I.", path, "-o", bin, "-pthread").CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	if out, err := exec.Command(bin).CombinedOutput(); err != nil {
		t.Fatalf("env truth mismatch: %v %s", err, out)
	}
}

func TestLatencyDeviceDestroyPreservesOtherDevice(t *testing.T) {
	source := `#include "vulkan_latency.c"
int main(void) {
  void *a = (void *)1, *b = (void *)2;
  atomic_store(&vk_latency_devices[0].device, a);
  atomic_store(&vk_latency_devices[1].device, b);
  tipsy_vk_latency_device_destroyed(a);
  return atomic_load(&vk_latency_devices[0].device) != NULL ||
    atomic_load(&vk_latency_devices[1].device) != b;
}`
	dir := t.TempDir()
	path := filepath.Join(dir, "destroy.c")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "destroy")
	cmd := exec.Command("cc", "-std=c11", "-ffunction-sections", "-fdata-sections", "-Wl,--gc-sections", "-I.", path, "-o", bin, "-pthread")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	if out, err := exec.Command(bin).CombinedOutput(); err != nil {
		t.Fatalf("destroy damaged another device: %v %s", err, out)
	}
}

func TestLatencyOffPathStaysInert(t *testing.T) {
	source := `#include "vulkan_latency.c"
void GoAndroid_LogVulkanAntiLag(int s, char *r) { (void)s; (void)r; }
int main(void) {
 tipsy_vk_latency_present((void *)1);
 return tipsy_vk_antilag_armed() != 0;
}`
	dir := t.TempDir()
	path := filepath.Join(dir, "off.c")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "off")
	cmd := exec.Command("cc", "-std=c11", "-ffunction-sections", "-fdata-sections", "-Wl,--gc-sections", "-I.", path, "-o", bin, "-pthread")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	if out, err := exec.Command(bin).CombinedOutput(); err != nil {
		t.Fatalf("off path modified outputs: %v %s", err, out)
	}
}

func TestAntiLagInjectsOmittedSupportedExtension(t *testing.T) {
	source := `#include <vulkan/vulkan_core.h>
#include "vulkan_latency.c"
void GoAndroid_LogVulkanAntiLag(int s, char *r) { (void)s; (void)r; }
int main(void) {
 atomic_store(&vk_antilag_armed, 1);
 VkDeviceCreateInfo in = {.sType = VK_STRUCTURE_TYPE_DEVICE_CREATE_INFO};
 VkDeviceCreateInfo out;
 int added = 0;
 if (!tipsy_vk_latency_prepare_device_create((void *)1, &in, &out, sizeof(out), &added, 1) || !added) return 1;
 int bad = out.enabledExtensionCount != 1 || strcmp(out.ppEnabledExtensionNames[0], "VK_AMD_anti_lag") || !out.pNext;
 tipsy_vk_latency_release_device_create(&out); return bad;
}`
	dir := t.TempDir()
	path := filepath.Join(dir, "inject.c")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "inject")
	cmd := exec.Command("cc", "-std=c11", "-ffunction-sections", "-fdata-sections", "-Wl,--gc-sections", "-I.", path, "-o", bin, "-pthread")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	if out, err := exec.Command(bin).CombinedOutput(); err != nil {
		t.Fatalf("supported omitted extension not injected: %v %s", err, out)
	}
}

func TestAntiLagConcurrentCreateClonesRemainTracked(t *testing.T) {
	source := `#include <vulkan/vulkan_core.h>
#include "vulkan_latency.c"
void GoAndroid_LogVulkanAntiLag(int s, char *r) { (void)s; (void)r; }
int main(void) {
 atomic_store(&vk_antilag_armed, 1);
 VkDeviceCreateInfo in = {.sType = VK_STRUCTURE_TYPE_DEVICE_CREATE_INFO}, a, b;
 int added;
 if (!tipsy_vk_latency_prepare_device_create((void *)1, &in, &a, sizeof(a), &added, 1)) return 1;
 if (!tipsy_vk_latency_prepare_device_create((void *)1, &in, &b, sizeof(b), &added, 1)) return 2;
 tipsy_vk_latency_release_device_create(&b);
 if (vk_antilag_publish == NULL || vk_antilag_publish->out_info != &a) return 3;
 tipsy_vk_latency_release_device_create(&a);
 return vk_antilag_publish != NULL;
}`
	dir := t.TempDir()
	path := filepath.Join(dir, "clones.c")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "clones")
	cmd := exec.Command("cc", "-std=c11", "-ffunction-sections", "-fdata-sections", "-Wl,--gc-sections", "-I.", path, "-o", bin, "-pthread")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	if out, err := exec.Command(bin).CombinedOutput(); err != nil {
		t.Fatalf("concurrent create clones lost: %v %s", err, out)
	}
}

func TestAntiLagDoesNotDuplicateClientExtension(t *testing.T) {
	source := `#include <vulkan/vulkan_core.h>
#include "vulkan_latency.c"
void GoAndroid_LogVulkanAntiLag(int s, char *r) { (void)s; (void)r; }
int main(void) {
 atomic_store(&vk_antilag_armed, 1);
 const char *name = "VK_AMD_anti_lag";
 VkDeviceCreateInfo in = {.sType = VK_STRUCTURE_TYPE_DEVICE_CREATE_INFO, .enabledExtensionCount = 1, .ppEnabledExtensionNames = &name}, out;
 int added = 0;
 int prepared = tipsy_vk_latency_prepare_device_create((void *)1, &in, &out, sizeof(out), &added, 1);
 if (prepared) tipsy_vk_latency_release_device_create(&out);
 return prepared || added || tipsy_vk_antilag_armed();
}`
	dir := t.TempDir()
	path := filepath.Join(dir, "duplicate.c")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "duplicate")
	cmd := exec.Command("cc", "-std=c11", "-ffunction-sections", "-fdata-sections", "-Wl,--gc-sections", "-I.", path, "-o", bin, "-pthread")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	if out, err := exec.Command(bin).CombinedOutput(); err != nil {
		t.Fatalf("client extension handling: %v %s", err, out)
	}
}

func TestAntiLagRADVRealDevice(t *testing.T) {
	source := `#define _DEFAULT_SOURCE 1
#include <vulkan/vulkan_core.h>
#include <dlfcn.h>
#include "vulkan_latency.c"
void GoAndroid_LogVulkanAntiLag(int s, char *r) { (void)s; (void)r; }
static PFN_vkAntiLagUpdateAMD real_update;
static unsigned calls;
static void counted(void *d, const void *data) { calls++; real_update(d, data); }
int main(void) {
 setenv("ENABLE_LAYER_MESA_ANTI_LAG", "1", 1);
 void *lib = dlopen("libvulkan.so.1", RTLD_NOW); if (!lib) return 77;
 PFN_vkGetInstanceProcAddr g = (PFN_vkGetInstanceProcAddr)dlsym(lib, "vkGetInstanceProcAddr"); if (!g) return 77;
 PFN_vkCreateInstance ci = (PFN_vkCreateInstance)g(NULL, "vkCreateInstance");
 VkApplicationInfo app = {.sType=VK_STRUCTURE_TYPE_APPLICATION_INFO,.apiVersion=VK_API_VERSION_1_2};
 VkInstanceCreateInfo ii = {.sType=VK_STRUCTURE_TYPE_INSTANCE_CREATE_INFO,.pApplicationInfo=&app}; VkInstance inst;
 if (!ci || ci(&ii, NULL, &inst) != VK_SUCCESS) return 77;
 PFN_vkEnumeratePhysicalDevices ep=(PFN_vkEnumeratePhysicalDevices)g(inst,"vkEnumeratePhysicalDevices");
 PFN_vkGetPhysicalDeviceProperties2 gp=(PFN_vkGetPhysicalDeviceProperties2)g(inst,"vkGetPhysicalDeviceProperties2");
 PFN_vkGetPhysicalDeviceFeatures2 gf=(PFN_vkGetPhysicalDeviceFeatures2)g(inst,"vkGetPhysicalDeviceFeatures2");
 PFN_vkEnumerateDeviceExtensionProperties ex=(PFN_vkEnumerateDeviceExtensionProperties)g(inst,"vkEnumerateDeviceExtensionProperties");
 PFN_vkGetPhysicalDeviceQueueFamilyProperties qf=(PFN_vkGetPhysicalDeviceQueueFamilyProperties)g(inst,"vkGetPhysicalDeviceQueueFamilyProperties");
 PFN_vkCreateDevice cd=(PFN_vkCreateDevice)g(inst,"vkCreateDevice");
 PFN_vkGetDeviceProcAddr gd=(PFN_vkGetDeviceProcAddr)g(inst,"vkGetDeviceProcAddr");
 if (!ep||!gp||!gf||!ex||!qf||!cd||!gd) return 77;
 uint32_t n=0; if (ep(inst,&n,NULL)!=VK_SUCCESS||!n) return 77; VkPhysicalDevice *devs=calloc(n,sizeof(*devs));
 if (!devs || ep(inst,&n,devs)!=VK_SUCCESS) return 77;
 VkPhysicalDevice dev=0;
 for (uint32_t i=0;i<n;i++) { VkPhysicalDeviceDriverProperties dp={.sType=VK_STRUCTURE_TYPE_PHYSICAL_DEVICE_DRIVER_PROPERTIES}; VkPhysicalDeviceProperties2 p={.sType=VK_STRUCTURE_TYPE_PHYSICAL_DEVICE_PROPERTIES_2,.pNext=&dp}; gp(devs[i],&p); if (p.properties.vendorID==0x1002 && dp.driverID==VK_DRIVER_ID_MESA_RADV) { dev=devs[i]; break; } }
 free(devs); if (!dev) return 77;
 uint32_t count=0; if (ex(dev,NULL,&count,NULL)!=VK_SUCCESS||!count) return 77;
 VkExtensionProperties *props=calloc(count,sizeof(*props)); if (!props||ex(dev,NULL,&count,props)!=VK_SUCCESS) return 77;
 int found=0; for (uint32_t i=0;i<count;i++) if (!strcmp(props[i].extensionName,"VK_AMD_anti_lag")) found=1; free(props); if (!found) return 77;
 VkPhysicalDeviceAntiLagFeaturesAMD af={.sType=VK_STRUCTURE_TYPE_PHYSICAL_DEVICE_ANTI_LAG_FEATURES_AMD}; VkPhysicalDeviceFeatures2 features={.sType=VK_STRUCTURE_TYPE_PHYSICAL_DEVICE_FEATURES_2,.pNext=&af}; gf(dev,&features); if (!af.antiLag) return 77;
 uint32_t nq=0; qf(dev,&nq,NULL); if (!nq) return 77; VkQueueFamilyProperties *qs=calloc(nq,sizeof(*qs)); qf(dev,&nq,qs); uint32_t qi=0; while (qi<nq && !qs[qi].queueCount) qi++; free(qs); if(qi==nq) return 77;
 float priority=1; VkDeviceQueueCreateInfo q={.sType=VK_STRUCTURE_TYPE_DEVICE_QUEUE_CREATE_INFO,.queueFamilyIndex=qi,.queueCount=1,.pQueuePriorities=&priority};
 VkDeviceCreateInfo in={.sType=VK_STRUCTURE_TYPE_DEVICE_CREATE_INFO,.queueCreateInfoCount=1,.pQueueCreateInfos=&q}, out; int added=0;
 atomic_store(&vk_antilag_armed,1);
 if (!tipsy_vk_latency_prepare_device_create(dev,&in,&out,sizeof(out),&added,1)||!added) return 1;
 printf("enabled extension: %s\n",out.ppEnabledExtensionNames[out.enabledExtensionCount-1]);
 VkDevice device=0; VkResult result=cd(dev,&out,NULL,&device); tipsy_vk_latency_release_device_create(&out); if(result!=VK_SUCCESS) { printf("vkCreateDevice=%d\n",result); return 2; }
 real_update=(PFN_vkAntiLagUpdateAMD)gd(device,"vkAntiLagUpdateAMD"); if(!real_update) return 3;
 if(!tipsy_vk_latency_open_session(device,(void *)counted,NULL)||!tipsy_vk_antilag_update(device)) return 4;
 printf("RADV vkAntiLagUpdateAMD calls=%u\n",calls);
 PFN_vkDestroyDevice dd=(PFN_vkDestroyDevice)gd(device,"vkDestroyDevice"); if(dd) dd(device,NULL);
 PFN_vkDestroyInstance di=(PFN_vkDestroyInstance)g(inst,"vkDestroyInstance"); if(di) di(inst,NULL);
 return calls!=1;
}`
	dir := t.TempDir()
	path := filepath.Join(dir, "radv.c")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "radv")
	args := []string{"-std=c11", "-ffunction-sections", "-fdata-sections", "-Wl,--gc-sections", "-I.", path, "-o", bin, "-pthread", "-ldl"}
	if os.Getenv("TIPSY_TEST_ASAN") == "1" {
		args = append(args, "-fsanitize=address")
	}
	cmd := exec.Command("cc", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	run := exec.Command(bin)
	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "LSAN_OPTIONS=") ||
			strings.HasPrefix(entry, "VK_LOADER_DISABLE_DYNAMIC_LIBRARY_UNLOADING=") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "VK_LOADER_DEBUG=layer")
	if os.Getenv("TIPSY_TEST_ASAN") == "1" {
		suppressions, err := filepath.Abs("testdata/lsan_mesa_radv.supp")
		if err != nil {
			t.Fatal(err)
		}
		lsan := "suppressions=" + suppressions
		if existing := os.Getenv("LSAN_OPTIONS"); existing != "" {
			lsan = existing + ":" + lsan
		}
		// The loader unloads the driver and layer before LSan runs, which
		// leaves the Mesa allocation attributed to an unknown module and no
		// module-name suppression can match it. Keep both mapped for the leak
		// check so testdata/lsan_mesa_radv.supp can name them and nothing else.
		env = append(env,
			"LSAN_OPTIONS="+lsan,
			"VK_LOADER_DISABLE_DYNAMIC_LIBRARY_UNLOADING=1")
	}
	run.Env = env
	out, err := run.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 77 {
		t.Skipf("no eligible RADV anti-lag device: %s", out)
	}
	if err != nil {
		t.Fatalf("RADV engagement: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}

func TestAntiLagFeatureCheckRejectsUnsupported(t *testing.T) {
	source := `#include <vulkan/vulkan_core.h>
#include "vulkan_latency.c"
static void query(void *device, void *raw) {
 (void)device; VkPhysicalDeviceFeatures2 *features = raw;
 VkPhysicalDeviceAntiLagFeaturesAMD *anti = features->pNext;
 anti->antiLag = 0;
}

int main(void) { return tipsy_vk_latency_feature_supported((void *)1, (void *)query) != 0; }
`
	dir := t.TempDir()
	path := filepath.Join(dir, "feature.c")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "feature")
	cmd := exec.Command("cc", "-std=c11", "-ffunction-sections", "-fdata-sections", "-Wl,--gc-sections", "-I.", path, "-o", bin, "-pthread")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	if out, err := exec.Command(bin).CombinedOutput(); err != nil {
		t.Fatalf("unsupported feature accepted: %v %s", err, out)
	}
}

func TestUnarmedDeviceCreateLatencyHookTakesNoLockOrLog(t *testing.T) {
	source := `#define _POSIX_C_SOURCE 200809L
#include <pthread.h>
#include <stdlib.h>
#include "vulkan_latency.c"
static int lock_calls;
static int log_calls;
int __real_pthread_mutex_lock(pthread_mutex_t *mutex);
int __real_pthread_mutex_unlock(pthread_mutex_t *mutex);
int __wrap_pthread_mutex_lock(pthread_mutex_t *mutex) {
  lock_calls++;
  return __real_pthread_mutex_lock(mutex);
}
int __wrap_pthread_mutex_unlock(pthread_mutex_t *mutex) {
  return __real_pthread_mutex_unlock(mutex);
}
void GoAndroid_LogVulkanAntiLag(int state, char *reason) { (void)state; (void)reason; log_calls++; }
void GoAndroid_LogVulkanShaderCache(int persistent, char *dir) { (void)persistent; (void)dir; log_calls++; }
int main(void) {
  char info[72] = {0};
  char out[72] = {0};
  int added = -1;
  unsetenv("TIPSY_ANTILAG");
  atomic_store(&vk_antilag_armed, 0);
  atomic_store(&vk_antilag_closed, 0);
  if (tipsy_vk_latency_prepare_device_create((void *)1, info, out, sizeof out, &added, 1)) return 10;
  if (added != 0) return 11;
  if (lock_calls != 0) return 12;
  if (log_calls != 0) return 13;
  return 0;
}`
	dir := t.TempDir()
	path := filepath.Join(dir, "unarmed.c")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "unarmed")
	cmd := exec.Command("cc", "-std=c11", "-ffunction-sections", "-fdata-sections",
		"-Wl,--gc-sections", "-Wl,--wrap=pthread_mutex_lock",
		"-Wl,--wrap=pthread_mutex_unlock", "-I.", path, "-o", bin, "-pthread")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	if out, err := exec.Command(bin).CombinedOutput(); err != nil {
		t.Fatalf("unarmed device-create hook did work: %v %s", err, out)
	}
}

func TestShaderCacheDisableValue(t *testing.T) {
	source := `#define _POSIX_C_SOURCE 200809L
#include "vulkan_latency.c"
static int result = -1;
void GoAndroid_LogVulkanShaderCache(int persistent, char *dir) { (void)dir; result = persistent; }
int main(void) {
  setenv("MESA_SHADER_CACHE_DISABLE", "1", 1);
  unsetenv("MESA_SHADER_CACHE_MAX_SIZE");
  tipsy_vk_latency_log_shader_cache();
  return result == 0 ? 0 : 1;
}`
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.c")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "cache")
	cmd := exec.Command("cc", "-std=c11", "-ffunction-sections", "-fdata-sections", "-Wl,--gc-sections", "-I.", path, "-o", bin)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	if out, err := exec.Command(bin).CombinedOutput(); err != nil {
		t.Fatalf("disabled shader cache reported persistent: %v %s", err, out)
	}
}

func TestVulkanLatencyAntiLagABIRealHeader(t *testing.T) {
	source := `#include <vulkan/vulkan_core.h>
#include "vulkan_latency.c"
_Static_assert(sizeof(TipsyVkAntiLagFeatures) == sizeof(VkPhysicalDeviceAntiLagFeaturesAMD), "anti-lag feature size");
_Static_assert(offsetof(TipsyVkAntiLagFeatures, antiLag) == offsetof(VkPhysicalDeviceAntiLagFeaturesAMD, antiLag), "anti-lag feature offset");
_Static_assert(TIPSY_VK_ANTI_LAG_FEATURES_STYPE == VK_STRUCTURE_TYPE_PHYSICAL_DEVICE_ANTI_LAG_FEATURES_AMD, "anti-lag feature type");
_Static_assert(sizeof(TipsyVkAntiLagData) == sizeof(VkAntiLagDataAMD), "anti-lag data size");
_Static_assert(TIPSY_VK_ANTI_LAG_DATA_STYPE == VK_STRUCTURE_TYPE_ANTI_LAG_DATA_AMD, "anti-lag data type");
_Static_assert(TIPSY_VK_ANTI_LAG_MODE_ON == VK_ANTI_LAG_MODE_ON_AMD, "anti-lag mode");
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

// Header values from the Vulkan SDK's vulkan_core.h (1.4.363). The C unit
// compares its own #defines against this table through
// tipsy_test_vk_latency_abi, so a wrong constant fails here instead of
// silently producing a struct the driver rejects.
var vulkanHeaderABI = []struct {
	name  string
	value uint32
}{
	{"anti_lag_features", 1000476000}, // VK_STRUCTURE_TYPE_PHYSICAL_DEVICE_ANTI_LAG_FEATURES_AMD
	{"anti_lag_data", 1000476001},     // VK_STRUCTURE_TYPE_ANTI_LAG_DATA_AMD
	{"anti_lag_mode_on", 1},           // VK_ANTI_LAG_MODE_ON_AMD
}

func abiCheck(entries []struct {
	name  string
	value uint32
}) int {
	return tipsyTestLatencyABI(entries)
}

func TestVulkanLatencyCBuiltinsMatchHeaders(t *testing.T) {
	if got := abiCheck(vulkanHeaderABI); got != 0 {
		t.Fatalf("%d C ABI constants disagree with vulkan_core.h", got)
	}
}

func TestVulkanLatencyABICheckDetectsWrongValue(t *testing.T) {
	// The checker must catch a bad value, not report zero by accident.
	bad := []struct {
		name  string
		value uint32
	}{{"anti_lag_features", 1000208000}}
	if got := abiCheck(bad); got != 1 {
		t.Fatalf("a wrong VK_STRUCTURE_TYPE_PHYSICAL_DEVICE_ANTI_LAG_FEATURES_AMD must be reported, got %d", got)
	}
}

func TestVulkanLatencyABICheckDetectsUnknownName(t *testing.T) {
	bad := []struct {
		name  string
		value uint32
	}{{"no_such_constant", 0}}
	if got := abiCheck(bad); got != 1 {
		t.Fatalf("an unknown constant name must be reported, got %d", got)
	}
}
