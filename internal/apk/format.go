package apk

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FormatText renders a human-readable inspect report.
func FormatText(r *Report) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Tipsy APK report\n")
	if len(r.Inputs) > 0 {
		b.WriteString("Inputs:\n")
		for _, in := range r.Inputs {
			fmt.Fprintf(&b, "  %s\n", in)
		}
	}
	if r.Merged != nil {
		b.WriteString("Merged:\n")
		writeMerged(&b, r.Merged)
	}
	fmt.Fprintf(&b, "Packages: %d\n", len(r.Packages))
	for i := range r.Packages {
		writePackage(&b, &r.Packages[i])
	}
	return b.String()
}

func writeMerged(b *strings.Builder, m *Merged) {
	fmt.Fprintf(b, "  Package: %s\n", dash(m.PackageName))
	fmt.Fprintf(b, "  Version: %s (%d)\n", dash(m.VersionName), m.VersionCode)
	if m.ApplicationLabel != "" {
		fmt.Fprintf(b, "  Label: %s\n", m.ApplicationLabel)
	}
	fmt.Fprintf(b, "  ABIs: %s\n", joinOrDash(m.Architectures))
	if len(m.NativeCode) > 0 {
		fmt.Fprintf(b, "  NativeCode: %s\n", strings.Join(m.NativeCode, ", "))
	}
	if len(m.SplitNames) > 0 {
		fmt.Fprintf(b, "  Splits: %s\n", formatSplits(m.SplitNames))
	}
	writeSigning(b, "  ", m.Signing)
	fmt.Fprintf(b, "  Native libraries: %d\n", len(m.NativeLibraries))
	for _, lib := range m.NativeLibraries {
		fmt.Fprintf(b, "    %s  %s  %d bytes\n", lib.ZIPPath, lib.SHA256, lib.Size)
	}
}

func writePackage(b *strings.Builder, p *Package) {
	fmt.Fprintf(b, "  %s\n", p.Path)
	fmt.Fprintf(b, "    SHA256: %s\n", p.FileSHA256)
	fmt.Fprintf(b, "    Size: %d\n", p.Size)
	fmt.Fprintf(b, "    Package: %s\n", dash(p.PackageName))
	fmt.Fprintf(b, "    Version: %s (%d)\n", dash(p.VersionName), p.VersionCode)
	split := p.SplitName
	if split == "" {
		if p.IsSplit {
			split = "(split)"
		} else {
			split = "(base)"
		}
	}
	fmt.Fprintf(b, "    Split: %s\n", split)
	if p.ApplicationLabel != "" {
		fmt.Fprintf(b, "    Label: %s\n", p.ApplicationLabel)
	}
	fmt.Fprintf(b, "    Debuggable: %t\n", p.Debuggable)
	fmt.Fprintf(b, "    ManifestOK: %t\n", p.ManifestOK)
	fmt.Fprintf(b, "    ABIs: %s\n", joinOrDash(p.Architectures))
	if len(p.NativeCode) > 0 {
		fmt.Fprintf(b, "    NativeCode: %s\n", strings.Join(p.NativeCode, ", "))
	}
	if len(p.UsesFeatures) > 0 {
		fmt.Fprintf(b, "    Uses-feature: %s\n", strings.Join(p.UsesFeatures, ", "))
	}
	if p.LauncherActivity != "" {
		fmt.Fprintf(b, "    Launcher: %s\n", p.LauncherActivity)
	}
	if len(p.GameActivities) > 0 {
		fmt.Fprintf(b, "    GameActivity: %s\n", strings.Join(p.GameActivities, ", "))
	}
	writeSigning(b, "    ", p.Signing)
	fmt.Fprintf(b, "    Native libraries: %d\n", len(p.NativeLibraries))
	for _, lib := range p.NativeLibraries {
		fmt.Fprintf(b, "      %s  %s  %d bytes\n", lib.ZIPPath, lib.SHA256, lib.Size)
	}
}

func writeSigning(b *strings.Builder, indent string, s SigningInfo) {
	fmt.Fprintf(b, "%sSigning: v1=%t v2=%t v3=%t v3.1=%t\n",
		indent, s.HasV1, s.HasV2, s.HasV3, s.HasV3_1)
	for i, h := range s.CertSHA256 {
		subj := ""
		if i < len(s.Subjects) {
			subj = s.Subjects[i]
		}
		if subj != "" {
			fmt.Fprintf(b, "%s  cert %s  %s\n", indent, h, subj)
		} else {
			fmt.Fprintf(b, "%s  cert %s\n", indent, h)
		}
	}
	if s.ParseError != "" {
		fmt.Fprintf(b, "%s  parse error: %s\n", indent, s.ParseError)
	}
}

func formatSplits(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		if n == "" {
			out[i] = "(base)"
		} else {
			out[i] = n
		}
	}
	return strings.Join(out, ", ")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func joinOrDash(ss []string) string {
	if len(ss) == 0 {
		return "-"
	}
	return strings.Join(ss, ", ")
}

// FormatJSON renders the inspect report as indented JSON.
func FormatJSON(r *Report) ([]byte, error) {
	if r == nil {
		return []byte("null"), nil
	}
	return json.MarshalIndent(r, "", "  ")
}

// FormatDiff renders a human-readable package-level comparison.
func FormatDiff(d *Diff) string {
	if d == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Tipsy APK diff\n")
	if d.empty() {
		b.WriteString("No package-level differences.\n")
		return b.String()
	}
	if d.PackageNameOld != d.PackageNameNew {
		fmt.Fprintf(&b, "Package: %s -> %s\n", dash(d.PackageNameOld), dash(d.PackageNameNew))
	} else {
		fmt.Fprintf(&b, "Package: %s\n", dash(d.PackageNameNew))
	}
	if d.VersionNameOld != d.VersionNameNew || d.VersionCodeOld != d.VersionCodeNew {
		fmt.Fprintf(&b, "Version: %s (%d) -> %s (%d)\n",
			dash(d.VersionNameOld), d.VersionCodeOld, dash(d.VersionNameNew), d.VersionCodeNew)
	} else {
		fmt.Fprintf(&b, "Version: %s (%d)\n", dash(d.VersionNameNew), d.VersionCodeNew)
	}
	writeList(&b, "ABIs added", d.ArchitecturesAdded)
	writeList(&b, "ABIs removed", d.ArchitecturesRemoved)
	writeList(&b, "Natives added", d.NativesAdded)
	writeList(&b, "Natives removed", d.NativesRemoved)
	writeList(&b, "Natives changed", d.NativesChanged)
	writeList(&b, "Certificates added", d.CertSHA256Added)
	writeList(&b, "Certificates removed", d.CertSHA256Removed)
	return b.String()
}

func writeList(b *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", title)
	for _, it := range items {
		fmt.Fprintf(b, "  %s\n", it)
	}
}

// FormatDiffJSON renders the diff as indented JSON.
func FormatDiffJSON(d *Diff) ([]byte, error) {
	if d == nil {
		return []byte("null"), nil
	}
	return json.MarshalIndent(d, "", "  ")
}
