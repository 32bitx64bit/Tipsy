package main

import (
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/diagnostics"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
)

func TestRobloxDoctorCheckMapsImportSeverity(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		wantStatus guimodel.CheckStatus
	}{
		{name: "clean", status: diagnostics.ImportsOK, wantStatus: guimodel.CheckReady},
		{name: "fallback", status: diagnostics.ImportsWarning, wantStatus: guimodel.CheckWarning},
		{name: "fatal", status: diagnostics.ImportsBlocked, wantStatus: guimodel.CheckBlocked},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			check := robloxDoctorCheck(diagnostics.RobloxInfo{
				DataDirPresent: true,
				RuntimeFiles:   "present",
				ImportStatus:   test.status,
				ImportDetail:   "generation test: tipsy_missing_symbol",
			})
			if check.Status != test.wantStatus || !strings.Contains(check.Detail, "tipsy_missing_symbol") {
				t.Fatalf("check=%+v, want status %q", check, test.wantStatus)
			}
		})
	}
}
