package compat

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/elfinspect"
)

// FormatText renders a combined compatibility report.
func FormatText(r *Report) string {
	if r == nil {
		return "<nil>\n"
	}
	var b strings.Builder
	b.WriteString("Tipsy compatibility report\n")
	b.WriteString("==========================\n")
	if r.APK != nil {
		fmt.Fprintf(&b, "APK inputs: %s\n", strings.Join(r.APK.Inputs, ", "))
		fmt.Fprintf(&b, "APK packages: %d\n", len(r.APK.Packages))
		if r.APK.Merged != nil {
			m := r.APK.Merged
			if m.PackageName != "" {
				fmt.Fprintf(&b, "Package: %s", m.PackageName)
				if m.VersionName != "" {
					fmt.Fprintf(&b, " %s", m.VersionName)
				}
				if m.VersionCode != 0 {
					fmt.Fprintf(&b, " (%d)", m.VersionCode)
				}
				b.WriteByte('\n')
			}
			if len(m.Architectures) > 0 {
				fmt.Fprintf(&b, "Architectures: %s\n", strings.Join(m.Architectures, ", "))
			}
		}
	}

	x86 := 0
	for i := range r.Elves {
		if r.Elves[i].IsX86_64() {
			x86++
		}
	}
	fmt.Fprintf(&b, "x86_64 ELF objects analyzed: %d\n", len(r.Elves))
	if x86 != len(r.Elves) {
		fmt.Fprintf(&b, "  (warning: %d of %d are not ELF64 EM_X86_64)\n", len(r.Elves)-x86, len(r.Elves))
	}
	if len(r.Elves) == 0 {
		b.WriteString("*** No x86_64 native libraries analyzed ***\n")
	}
	if len(r.OtherABIs) > 0 {
		fmt.Fprintf(&b, "Other ABIs present (not analyzed; Tipsy is x86-64 only): %s\n", strings.Join(r.OtherABIs, ", "))
	}

	writeList(&b, "Graphics hints", r.GraphicsHints)
	writeList(&b, "Audio hints", r.AudioHints)
	writeList(&b, "GameActivity hints", r.GameActivityHints)
	writeList(&b, "JNI hints", r.JNIHints)
	writeList(&b, "Imported Android/bionic symbols", r.ImportedAndroidSymbols)

	for i := range r.Elves {
		b.WriteByte('\n')
		b.WriteString(elfinspect.FormatText(&r.Elves[i]))
	}
	return b.String()
}

func writeList(b *strings.Builder, title string, items []string) {
	fmt.Fprintf(b, "%s (%d):\n", title, len(items))
	if len(items) == 0 {
		b.WriteString("  (none)\n")
		return
	}
	for _, s := range items {
		fmt.Fprintf(b, "  - %s\n", s)
	}
}

// FormatJSON renders the report as indented JSON.
func FormatJSON(r *Report) ([]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("compat: nil report")
	}
	return json.MarshalIndent(r, "", "  ")
}

func uniqueSorted(in []string) []string {
	out := append([]string(nil), in...)
	slices.Sort(out)
	return slices.Compact(out)
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
