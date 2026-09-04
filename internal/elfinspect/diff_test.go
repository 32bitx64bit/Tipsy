package elfinspect

import (
	"slices"
	"strconv"
	"testing"
)

func TestDiffNeededAndImports(t *testing.T) {
	t.Parallel()
	old := &Report{
		DTNeeded: []string{"libc.so", "liblog.so"},
		Imports: []Symbol{
			{Name: "ALooper_pollOnce"},
			{Name: "__libc_init"},
		},
	}
	new := &Report{
		DTNeeded: []string{"libc.so", "libandroid.so"},
		Imports: []Symbol{
			{Name: "ALooper_pollOnce"},
			{Name: "ANativeWindow_fromSurface"},
		},
	}
	d := Diff(old, new)
	if !slices.Equal(d.AddedNeeded, []string{"libandroid.so"}) {
		t.Fatalf("AddedNeeded=%v", d.AddedNeeded)
	}
	if !slices.Equal(d.RemovedNeeded, []string{"liblog.so"}) {
		t.Fatalf("RemovedNeeded=%v", d.RemovedNeeded)
	}
	if !slices.Equal(d.AddedImports, []string{"ANativeWindow_fromSurface"}) {
		t.Fatalf("AddedImports=%v", d.AddedImports)
	}
	if !slices.Equal(d.RemovedImports, []string{"__libc_init"}) {
		t.Fatalf("RemovedImports=%v", d.RemovedImports)
	}
}

func TestDiffNil(t *testing.T) {
	t.Parallel()
	d := Diff(nil, &Report{DTNeeded: []string{"libc.so"}})
	if !slices.Equal(d.AddedNeeded, []string{"libc.so"}) {
		t.Fatalf("AddedNeeded=%v", d.AddedNeeded)
	}
	if d.RemovedNeeded == nil {
		t.Fatal("RemovedNeeded nil")
	}
}

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
