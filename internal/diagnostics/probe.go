// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package diagnostics

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tipsy-linux/tipsy/internal/android"
	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

func probeSystem() SystemInfo {
	return SystemInfo{
		OS:           readOSName(),
		Kernel:       kernelRelease(),
		Architecture: goArch(),
	}
}

func readOSName() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	vars := parseOSRelease(string(data))
	if v := vars["PRETTY_NAME"]; v != "" {
		return v
	}
	if v := vars["NAME"]; v != "" {
		return v
	}
	return runtime.GOOS
}

func parseOSRelease(data string) map[string]string {
	out := make(map[string]string)
	sc := bufio.NewScanner(strings.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		out[k] = v
	}
	return out
}

func kernelRelease() string {
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	var u syscall.Utsname
	if err := syscall.Uname(&u); err == nil {
		if s := utsString(u.Release[:]); s != "" {
			return s
		}
	}
	return "unknown"
}

func utsString(v []int8) string {
	b := make([]byte, 0, len(v))
	for _, c := range v {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}

func probeDisplay(ctx context.Context) DisplayInfo {
	if ctx == nil {
		ctx = context.Background()
	}
	display := os.Getenv("DISPLAY")
	if display == "" {
		display = "unset"
	}
	session := os.Getenv("XDG_SESSION_TYPE")
	if session == "" {
		if display != "unset" {
			session = "X11 (DISPLAY set)"
		} else {
			session = "unknown"
		}
	}
	xrandr := "not found"
	if _, err := exec.LookPath("xrandr"); err == nil {
		xrandr = "OK"
	}
	xinput := "not found"
	if _, err := exec.LookPath("xinput"); err == nil {
		xinput = "OK"
	} else if pkgConfigExists(ctx, "xi") {
		xinput = "OK (libXi)"
	}
	return DisplayInfo{
		Session: session,
		DISPLAY: display,
		XRandR:  xrandr,
		XInput2: xinput,
	}
}

func probeGPU(ctx context.Context) GPUInfo {
	vendor, driver := gpuFromSysfs()
	if vendor == "" {
		vendor = gpuFromLSPCI(ctx)
	}
	if vendor == "" {
		vendor = "unknown"
	}
	if driver == "" {
		driver = "unknown"
	}
	if isMesaDriver(driver) && !strings.Contains(strings.ToLower(driver), "mesa") {
		driver = "Mesa (" + driver + ")"
	}

	egl := "not found"
	if ver, ok := pkgConfigModversion(ctx, "egl"); ok {
		egl = "OK"
		if ver != "" {
			egl = "OK (" + ver + ")"
		}
	} else if libPresent("libEGL.so", "libEGL.so.1") {
		egl = "OK (libEGL present)"
	}

	gles := "not found"
	if ver, ok := pkgConfigModversion(ctx, "glesv2"); ok {
		gles = "OK"
		if ver != "" {
			gles = "OK (" + ver + ")"
		}
	} else if libPresent("libGLESv2.so", "libGLESv2.so.2") {
		gles = "OK (libGLESv2 present)"
	}

	vulkan := "not found"
	if ver, ok := pkgConfigModversion(ctx, "vulkan"); ok {
		vulkan = "OK"
		if ver != "" {
			vulkan = "OK (" + ver + ")"
		}
	} else if libPresent("libvulkan.so", "libvulkan.so.1") {
		vulkan = "OK (libvulkan present)"
	}

	return GPUInfo{
		Vendor: vendor,
		Driver: driver,
		EGL:    egl,
		GLES:   gles,
		Vulkan: vulkan,
	}
}

func gpuFromSysfs() (vendor, driver string) {
	entries, err := os.ReadDir("/sys/class/drm")
	if err != nil {
		return "", ""
	}
	var vendors []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "card") || strings.Contains(name, "-") {
			continue
		}
		base := filepath.Join("/sys/class/drm", name, "device")
		idb, err := os.ReadFile(filepath.Join(base, "vendor"))
		if err != nil {
			continue
		}
		id := strings.TrimSpace(strings.ToLower(string(idb)))
		vendors = append(vendors, pciVendorName(id))
		if driver == "" {
			if link, err := os.Readlink(filepath.Join(base, "driver")); err == nil {
				driver = filepath.Base(link)
			}
		}
	}
	return uniqueJoin(vendors), driver
}

func pciVendorName(id string) string {
	id = strings.TrimPrefix(id, "0x")
	switch id {
	case "1002", "1022":
		return "AMD"
	case "10de":
		return "NVIDIA"
	case "8086":
		return "Intel"
	case "1af4":
		return "VirtIO"
	case "1414":
		return "Microsoft"
	case "15ad":
		return "VMware"
	default:
		if id == "" {
			return "unknown"
		}
		return "PCI:" + id
	}
}

func gpuFromLSPCI(ctx context.Context) string {
	if _, err := exec.LookPath("lspci"); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lspci", "-nn").Output()
	if err != nil {
		return ""
	}
	var vendors []string
	for _, line := range strings.Split(string(out), "\n") {
		low := strings.ToLower(line)
		if !strings.Contains(low, "vga") && !strings.Contains(low, "3d") && !strings.Contains(low, "display") {
			continue
		}
		switch {
		case strings.Contains(low, "amd") || strings.Contains(low, "ati") || strings.Contains(line, "1002:"):
			vendors = append(vendors, "AMD")
		case strings.Contains(low, "nvidia") || strings.Contains(line, "10de:"):
			vendors = append(vendors, "NVIDIA")
		case strings.Contains(low, "intel") || strings.Contains(line, "8086:"):
			vendors = append(vendors, "Intel")
		default:
			vendors = append(vendors, strings.TrimSpace(line))
		}
	}
	return uniqueJoin(vendors)
}

func isMesaDriver(driver string) bool {
	d := strings.ToLower(driver)
	for _, s := range []string{"amdgpu", "radeon", "i915", "xe", "iris", "nouveau", "virtio", "vmwgfx"} {
		if strings.Contains(d, s) {
			return true
		}
	}
	return false
}

func probeAudio(ctx context.Context) AudioInfo {
	uid := strconv.Itoa(os.Getuid())
	pw := "not found"
	if _, err := exec.LookPath("pipewire"); err == nil || commandExists("pw-cli") || commandExists("pw-dump") {
		pw = "OK"
	} else if fileExists(filepath.Join("/run/user", uid, "pipewire-0")) {
		pw = "OK (socket)"
	}
	pulse := "not found"
	if commandExists("pulseaudio") || commandExists("pactl") {
		pulse = "OK"
	} else if fileExists(filepath.Join("/run/user", uid, "pulse", "native")) {
		pulse = "OK (socket)"
	}
	info := AudioInfo{PipeWire: pw, Pulse: pulse}
	attachMicrophone(ctx, &info)
	return info
}

func probeQt(ctx context.Context) QtInfo {
	if ver, ok := pkgConfigModversion(ctx, "Qt6Widgets"); ok {
		info := QtInfo{Widgets: "OK"}
		if ver != "" {
			info.Version = ver
			info.Widgets = "OK (" + ver + ")"
		}
		return info
	}
	return QtInfo{Widgets: "not found (CLI still builds; GUI needs Qt 6)"}
}

// inspectActiveImports is a seam for tests to count probes; production reads
// the active generation through setupsvc.
var inspectActiveImports = func(ctx context.Context, storeRoot string, trust setupsvc.TrustPolicy) (setupsvc.ActiveClientImportReport, error) {
	return setupsvc.InspectActiveClientImports(ctx, storeRoot, trust, android.SupportsImport)
}

// probeCache shares one Roblox generation probe across the subsystems of a
// single report: loader and roblox both report the same active generation, so
// they must not inspect it twice.
type probeCache struct {
	once   sync.Once
	roblox RobloxInfo
}

func (c *probeCache) robloxInfo(ctx context.Context, trust setupsvc.TrustPolicy) RobloxInfo {
	c.once.Do(func() {
		c.roblox = probeRoblox(ctx, trust)
	})
	return c.roblox
}

func probeRoblox(ctx context.Context, trust setupsvc.TrustPolicy) RobloxInfo {
	if ctx == nil {
		ctx = context.Background()
	}
	paths := config.Paths()
	info := RobloxInfo{
		DataDir: paths.DataDir,
		Note:    "Package cookies and tokens are not read.",
	}
	// A cancelled report must not stat, read, or inspect anything further.
	if ctx.Err() != nil {
		return info
	}
	fi, err := os.Stat(paths.DataDir)
	if err != nil || !fi.IsDir() {
		info.RuntimeFiles = "not present"
	} else if entries, readErr := os.ReadDir(paths.DataDir); readErr != nil || len(entries) == 0 {
		info.RuntimeFiles = "empty"
	} else {
		info.DataDirPresent = true
		info.RuntimeFiles = "present"
	}

	storeRoot := setupsvc.GenerationStoreDir(filepath.Join(paths.DataDir, "runtime"))
	if ctx.Err() != nil {
		return info
	}
	active, err := inspectActiveImports(ctx, storeRoot, trust)
	if err != nil {
		// No active generation (or one that cannot be read) retains the
		// historical data-directory-only diagnostic.
		return info
	}
	info.Generation = active.GenerationID
	info.Version = active.VersionName
	if info.Version == "" && active.VersionCode != 0 {
		info.Version = strconv.FormatInt(active.VersionCode, 10)
	}
	info.ImportStatus = ImportsOK
	label := "Roblox generation " + info.Generation
	if info.Version != "" {
		label += " (" + info.Version + ")"
	}
	imports := active.Imports
	if len(imports.Fatal) > 0 || len(imports.AbsentLibraries) > 0 {
		var details []string
		if len(imports.Fatal) > 0 {
			details = append(details, "unresolvable native symbol(s): "+strings.Join(importNames(imports.Fatal), ", "))
		}
		if len(imports.AbsentLibraries) > 0 {
			details = append(details, "bundled native libraries absent from lib/x86_64: "+strings.Join(imports.AbsentLibraries, ", "))
		}
		info.ImportStatus = ImportsBlocked
		info.ImportDetail = label + " cannot launch: " + strings.Join(details, "; ")
	} else if len(imports.Fallback) > 0 {
		info.ImportStatus = ImportsWarning
		info.ImportDetail = label + " binds missing native function(s) to Tipsy's unsupported fallback (returns 0): " + strings.Join(importNames(imports.Fallback), ", ")
	}
	return info
}

func importNames(imports []setupsvc.MissingImport) []string {
	names := make([]string, len(imports))
	for i, missing := range imports {
		names[i] = missing.Name
	}
	return names
}

func pkgConfigExists(ctx context.Context, name string) bool {
	_, ok := pkgConfigModversion(ctx, name)
	return ok
}

func pkgConfigModversion(ctx context.Context, name string) (string, bool) {
	if _, err := exec.LookPath("pkg-config"); err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "pkg-config", "--exists", name).Run(); err != nil {
		return "", false
	}
	out, err := exec.CommandContext(ctx, "pkg-config", "--modversion", name).Output()
	if err != nil {
		return "", true
	}
	return strings.TrimSpace(string(out)), true
}

func libPresent(names ...string) bool {
	dirs := []string{
		"/usr/lib",
		"/usr/lib64",
		"/usr/lib/x86_64-linux-gnu",
		"/lib",
		"/lib64",
		"/lib/x86_64-linux-gnu",
		"/usr/local/lib",
	}
	for _, dir := range dirs {
		for _, n := range names {
			if fileExists(filepath.Join(dir, n)) {
				return true
			}
		}
	}
	return false
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func uniqueJoin(in []string) string {
	seen := make(map[string]struct{})
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return strings.Join(out, ", ")
}
