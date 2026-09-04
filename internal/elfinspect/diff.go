package elfinspect

import "slices"

// Diff returns DT_NEEDED and imported-symbol names present only in new or only in old.
// Useful for compare-roblox: added vs removed Android/bionic dependencies.
func Diff(old, new *Report) *ReportDiff {
	if old == nil {
		old = &Report{}
	}
	if new == nil {
		new = &Report{}
	}
	oldNeeded := uniqueCopy(old.DTNeeded)
	newNeeded := uniqueCopy(new.DTNeeded)
	oldImp := uniqueCopy(symbolNames(old.Imports))
	newImp := uniqueCopy(symbolNames(new.Imports))
	return &ReportDiff{
		AddedNeeded:    sliceOnlyIn(newNeeded, oldNeeded),
		RemovedNeeded:  sliceOnlyIn(oldNeeded, newNeeded),
		AddedImports:   sliceOnlyIn(newImp, oldImp),
		RemovedImports: sliceOnlyIn(oldImp, newImp),
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

func uniqueCopy(in []string) []string {
	out := append([]string(nil), in...)
	slices.Sort(out)
	return slices.Compact(out)
}

func sliceOnlyIn(have, exclude []string) []string {
	ex := map[string]struct{}{}
	for _, s := range exclude {
		ex[s] = struct{}{}
	}
	var out []string
	for _, s := range have {
		if _, ok := ex[s]; !ok {
			out = append(out, s)
		}
	}
	if out == nil {
		return []string{}
	}
	return out
}
