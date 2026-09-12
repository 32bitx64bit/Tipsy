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
	"syscall"
	"time"

	"github.com/tipsy-linux/tipsy/internal/config"
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

func probeDisplay() DisplayInfo {
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
	} else if pkgConfigExists(context.Background(), "xi") {
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

func probeRoblox() RobloxInfo {
	dir := config.Paths().DataDir
	info := RobloxInfo{
		DataDir: dir,
		Note:    "Package cookies and tokens are not read.",
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		info.RuntimeFiles = "not present"
		return info
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		info.RuntimeFiles = "empty"
		return info
	}
	info.DataDirPresent = true
	info.RuntimeFiles = "present"
	return info
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
