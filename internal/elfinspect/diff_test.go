package elfinspect

import (
	"strconv"
	"testing"
)

func TestFormatTextNil(t *testing.T) {
	t.Parallel()
	if FormatText(nil) != "<nil>\n" {
		t.Fatal(FormatText(nil))
	}
	if _, err := FormatJSON(nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestSelectExportsKeepsJNI(t *testing.T) {
	t.Parallel()
	all := make([]Symbol, 0, maxListedNonJNIExports+10)
	all = append(all, Symbol{Name: "JNI_OnLoad"}, Symbol{Name: "Java_com_foo_Bar"})
	for i := 0; i < maxListedNonJNIExports+5; i++ {
		all = append(all, Symbol{Name: "sym_" + strconv.Itoa(i)})
	}
	listed, total, trunc := selectExports(all)
	if !trunc {
		t.Fatal("expected truncation")
	}
	if total != len(all) {
		t.Fatalf("total=%d want %d", total, len(all))
	}
	if !containsAny(symbolNames(listed), "JNI_OnLoad", "Java_com_foo_Bar") {
		t.Fatalf("JNI dropped: %v", symbolNames(listed)[:5])
	}
	if len(listed) != 2+maxListedNonJNIExports {
		t.Fatalf("listed=%d", len(listed))
	}
}

func symbolNames(syms []Symbol) []string {
	out := make([]string, 0, len(syms))
	for _, s := range syms {
		if s.Name != "" {
			out = append(out, s.Name)
		}
	}
	return out
}
