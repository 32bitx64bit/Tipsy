package elfinspect

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// FormatText renders a human-readable ELF diagnostic report.
func FormatText(r *Report) string {
	if r == nil {
		return "<nil>\n"
	}
	var b strings.Builder
	if note := archNote(r.Class, r.Machine); note != "" {
		fmt.Fprintf(&b, "*** %s ***\n\n", note)
	}
	fmt.Fprintf(&b, "ELF: %s\n", r.Name)
	fmt.Fprintf(&b, "  Class:        %s\n", r.Class)
	fmt.Fprintf(&b, "  Machine:      %s\n", r.Machine)
	fmt.Fprintf(&b, "  Type:         %s\n", r.Type)
	if r.Interpreter != "" {
		fmt.Fprintf(&b, "  Interpreter:  %s\n", r.Interpreter)
	} else {
		fmt.Fprintf(&b, "  Interpreter:  (none)\n")
	}
	fmt.Fprintf(&b, "  TLS:          %s\n", formatTLS(r.TLS))

	fmt.Fprintf(&b, "  DT_NEEDED (%d):\n", len(r.DTNeeded))
	if len(r.DTNeeded) == 0 {
		fmt.Fprintf(&b, "    (none)\n")
	} else {
		for _, n := range r.DTNeeded {
			fmt.Fprintf(&b, "    - %s\n", n)
		}
	}

	if len(r.AndroidNotes) > 0 {
		fmt.Fprintf(&b, "  Android notes:\n")
		for _, n := range r.AndroidNotes {
			fmt.Fprintf(&b, "    - %s\n", n)
		}
	}

	if len(r.JNIEntryPoints) > 0 {
		fmt.Fprintf(&b, "  JNI entry points (%d):\n", len(r.JNIEntryPoints))
		for _, n := range r.JNIEntryPoints {
			fmt.Fprintf(&b, "    - %s\n", n)
		}
	}

	fmt.Fprintf(&b, "  Relocations:  total=%d", r.Relocations.Total)
	if r.Relocations.AndroidPacked {
		b.WriteString(" android-packed=yes")
	}
	b.WriteByte('\n')
	if len(r.Relocations.ByType) > 0 {
		keys := make([]string, 0, len(r.Relocations.ByType))
		for k := range r.Relocations.ByType {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "    - %s: %d\n", k, r.Relocations.ByType[k])
		}
	}

	fmt.Fprintf(&b, "  Imports (%d):\n", len(r.Imports))
	if len(r.Imports) == 0 {
		fmt.Fprintf(&b, "    (none)\n")
	} else {
		for _, s := range r.Imports {
			fmt.Fprintf(&b, "    - %s\n", formatSymbol(s))
		}
	}

	exportLabel := fmt.Sprintf("%d", len(r.Exports))
	if r.ExportsTruncated {
		exportLabel = fmt.Sprintf("%d listed / %d total, truncated", len(r.Exports), r.ExportTotal)
	} else if r.ExportTotal > 0 && r.ExportTotal != len(r.Exports) {
		exportLabel = fmt.Sprintf("%d listed / %d total", len(r.Exports), r.ExportTotal)
	}
	fmt.Fprintf(&b, "  Exports (%s):\n", exportLabel)
	if len(r.Exports) == 0 {
		fmt.Fprintf(&b, "    (none)\n")
	} else {
		for _, s := range r.Exports {
			fmt.Fprintf(&b, "    - %s\n", formatSymbol(s))
		}
	}
	return b.String()
}

func formatTLS(t TLSInfo) string {
	if !t.Present {
		return "none"
	}
	var parts []string
	parts = append(parts, "present")
	if t.Size > 0 {
		parts = append(parts, fmt.Sprintf("size=%d", t.Size))
	}
	if t.Align > 0 {
		parts = append(parts, fmt.Sprintf("align=%d", t.Align))
	}
	if len(t.SectionNames) > 0 {
		parts = append(parts, "sections="+strings.Join(t.SectionNames, ","))
	}
	return strings.Join(parts, ", ")
}

func formatSymbol(s Symbol) string {
	out := s.Name
	var extra []string
	if s.Library != "" {
		extra = append(extra, s.Library)
	}
	if s.Version != "" {
		extra = append(extra, s.Version)
	}
	if s.Binding != "" && s.Binding != "STB_GLOBAL" {
		extra = append(extra, s.Binding)
	}
	if len(extra) > 0 {
		out += " [" + strings.Join(extra, " ") + "]"
	}
	return out
}

// FormatJSON renders the report as indented JSON.
func FormatJSON(r *Report) ([]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("elfinspect: nil report")
	}
	return json.MarshalIndent(r, "", "  ")
}
