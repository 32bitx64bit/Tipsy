// Package elfinspect statically analyzes ELF objects with the Go standard
// library debug/elf. It does not load or execute code.
package elfinspect

// Report is a static analysis of one ELF object. It never loads or executes code.
type Report struct {
	Name             string            `json:"name"`
	Class            string            `json:"class"`   // ELF64 / ELF32
	Machine          string            `json:"machine"` // EM_X86_64
	Type             string            `json:"type"`    // ET_DYN
	Interpreter      string            `json:"interpreter,omitempty"`
	DTNeeded         []string          `json:"dtNeeded"`
	Imports          []Symbol          `json:"imports"`
	Exports          []Symbol          `json:"exports"`
	TLS              TLSInfo           `json:"tls"`
	Relocations      RelocationSummary `json:"relocations"`
	AndroidNotes     []string          `json:"androidNotes"`
	JNIEntryPoints   []string          `json:"jniEntryPoints"`
	ExportTotal      int               `json:"exportTotal"`
	ExportsTruncated bool              `json:"exportsTruncated,omitempty"`
}

// Symbol is a dynamic-symbol import or export.
type Symbol struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Library string `json:"library,omitempty"` // DT_NEEDED / GNU verneed when known
	Binding string `json:"binding,omitempty"` // STB_GLOBAL, STB_WEAK
	Type    string `json:"type,omitempty"`    // STT_FUNC, STT_OBJECT, ...
}

// TLSInfo describes thread-local storage if the object uses it.
type TLSInfo struct {
	Present      bool     `json:"present"`
	Size         uint64   `json:"size,omitempty"` // PT_TLS p_memsz, else .tdata+.tbss
	Align        uint64   `json:"align,omitempty"`
	FileSize     uint64   `json:"fileSize,omitempty"`
	SectionNames []string `json:"sectionNames,omitempty"`
}

// RelocationSummary counts relocation entries by ELF type name.
type RelocationSummary struct {
	Total         int            `json:"total"`
	ByType        map[string]int `json:"byType,omitempty"`
	AndroidREL    bool           `json:"androidRel,omitempty"`
	AndroidRELA   bool           `json:"androidRela,omitempty"`
	AndroidPacked bool           `json:"androidPacked,omitempty"`
}

// ReportDiff is the delta between two ELF reports (new versus old).
type ReportDiff struct {
	AddedNeeded    []string `json:"addedNeeded"`
	RemovedNeeded  []string `json:"removedNeeded"`
	AddedImports   []string `json:"addedImports"`
	RemovedImports []string `json:"removedImports"`
}

// IsX86_64 reports whether this object is the architecture Tipsy runs.
func (r *Report) IsX86_64() bool {
	return r != nil && r.Class == "ELF64" && r.Machine == "EM_X86_64"
}
