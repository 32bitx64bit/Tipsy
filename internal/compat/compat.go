// Package compat builds a combined APK + ELF compatibility report for Tipsy.
package compat

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/apk"
	"github.com/tipsy-linux/tipsy/internal/elfinspect"
)

// Report is a combined APK + ELF compatibility snapshot for Tipsy diagnostics.
type Report struct {
	APK                    *apk.Report         `json:"apk,omitempty"`
	Elves                  []elfinspect.Report `json:"elves"`
	OtherABIs              []string            `json:"otherAbis,omitempty"`
	GraphicsHints          []string            `json:"graphicsHints"`
	AudioHints             []string            `json:"audioHints"`
	GameActivityHints      []string            `json:"gameActivityHints"`
	JNIHints               []string            `json:"jniHints"`
	ImportedAndroidSymbols []string            `json:"importedAndroidSymbols"`
}

// Build inspects APKs, analyzes x86_64 native libraries, and classifies Android hints.
func Build(ctx context.Context, paths []string) (*Report, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	apkRep, err := apk.Inspect(ctx, paths)
	if err != nil {
		return nil, err
	}
	elves, other, err := analyzeNatives(ctx, apkRep)
	if err != nil {
		return nil, err
	}
	return Assemble(apkRep, elves, other), nil
}

// Assemble fills hint fields from already-analyzed ELFs. apkRep may be nil.
func Assemble(apkRep *apk.Report, elves []elfinspect.Report, otherABIs []string) *Report {
	rep := &Report{
		APK:                    apkRep,
		Elves:                  elves,
		OtherABIs:              uniqueSorted(otherABIs),
		GraphicsHints:          ClassifyGraphics(elves),
		AudioHints:             ClassifyAudio(elves),
		GameActivityHints:      ClassifyGameActivity(apkRep, elves),
		JNIHints:               ClassifyJNI(elves),
		ImportedAndroidSymbols: ClassifyAndroidImports(elves),
	}
	return rep
}

func analyzeNatives(ctx context.Context, apkRep *apk.Report) ([]elfinspect.Report, []string, error) {
	libs := nativeLibs(apkRep)
	other := map[string]struct{}{}
	seen := map[string]struct{}{}
	var elves []elfinspect.Report
	for _, lib := range libs {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		abi := libABI(lib)
		if !isX86_64(abi) {
			if abi != "" {
				other[abi] = struct{}{}
			}
			continue
		}
		key := lib.SHA256
		if key == "" {
			key = lib.APKPath + "|" + lib.ZIPPath
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		data, err := apk.ReadZipFile(lib.APKPath, lib.ZIPPath)
		if err != nil {
			return nil, nil, fmt.Errorf("compat: read %s:%s: %w", lib.APKPath, lib.ZIPPath, err)
		}
		name := lib.ZIPPath
		if name == "" {
			name = lib.Name
		}
		rep, err := elfinspect.AnalyzeBytes(name, data)
		if err != nil {
			return nil, nil, fmt.Errorf("compat: elf %s: %w", name, err)
		}
		elves = append(elves, *rep)
	}
	return elves, sortedKeys(other), nil
}

func nativeLibs(r *apk.Report) []apk.NativeLib {
	if r == nil {
		return nil
	}
	if r.Merged != nil && len(r.Merged.NativeLibraries) > 0 {
		return r.Merged.NativeLibraries
	}
	var out []apk.NativeLib
	for _, p := range r.Packages {
		out = append(out, p.NativeLibraries...)
	}
	return out
}

func libABI(lib apk.NativeLib) string {
	if lib.ABI != "" {
		return lib.ABI
	}
	// lib/<abi>/libfoo.so
	cleaned := strings.ReplaceAll(lib.ZIPPath, "\\", "/")
	parts := strings.Split(cleaned, "/")
	if len(parts) >= 3 && parts[0] == "lib" {
		return parts[1]
	}
	dir := path.Dir(cleaned)
	if base := path.Base(dir); base != "." && base != "/" {
		return base
	}
	return ""
}

func isX86_64(abi string) bool {
	switch strings.ToLower(strings.ReplaceAll(abi, "-", "_")) {
	case "x86_64", "amd64":
		return true
	default:
		return false
	}
}
