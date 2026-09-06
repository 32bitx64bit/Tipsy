// Package securitypolicy decodes the bounded, data-only policies carried by
// authenticated TUF targets. It deliberately has no execution, networking, or
// plugin surface.
package securitypolicy

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxPolicyBytes = 64 << 10
	MaxJSONDepth   = 12
	MaxItems       = 64
)

type Code string

const (
	CodeMalformed       Code = "policy_malformed"
	CodeNonCanonical    Code = "policy_noncanonical"
	CodeSchema          Code = "policy_schema_unsupported"
	CodeBounds          Code = "policy_bounds_exceeded"
	CodeNotYetValid     Code = "policy_not_yet_valid"
	CodeExpired         Code = "policy_expired"
	CodeRollback        Code = "policy_rollback"
	CodeUnsafeValue     Code = "policy_unsafe_value"
	CodeProfileUnknown  Code = "policy_profile_unknown"
	CodeCapability      Code = "policy_capability_unknown"
	CodeSignerMalformed Code = "policy_signer_malformed"
)

type Error struct {
	Code Code
	Msg  string
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Msg }

func fail(code Code, format string, args ...any) error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

type Validity struct {
	Sequence  uint64 `json:"sequence"`
	NotBefore string `json:"not_before"`
	Expires   string `json:"expires"`
}

type SignerLineage struct {
	ID             string   `json:"id"`
	SHA256         []string `json:"sha256"`
	MinVersionCode uint64   `json:"min_version_code"`
	MaxVersionCode uint64   `json:"max_version_code"`
}

type RobloxPolicy struct {
	Schema         string          `json:"schema"`
	Validity       Validity        `json:"validity"`
	PackageName    string          `json:"package_name"`
	Platform       string          `json:"platform"`
	Architecture   string          `json:"architecture"`
	MinVersionCode uint64          `json:"min_version_code"`
	MaxVersionCode uint64          `json:"max_version_code"`
	AllowedSplits  []string        `json:"allowed_splits"`
	SignerLineages []SignerLineage `json:"signer_lineages"`
}

type RuntimeSelector struct {
	ReleaseClass string `json:"release_class"`
	RobloxClass  string `json:"roblox_class"`
	KernelClass  string `json:"kernel_class"`
	BackendClass string `json:"backend_class"`
	ProfileID    string `json:"profile_id"`
}

type RuntimePolicy struct {
	Schema               string            `json:"schema"`
	Validity             Validity          `json:"validity"`
	MinimumTipsyVersion  string            `json:"minimum_tipsy_version"`
	RequiredCapabilities []string          `json:"required_capabilities"`
	Selectors            []RuntimeSelector `json:"selectors"`
}

type RecoveryPolicy struct {
	Schema                string   `json:"schema"`
	Validity              Validity `json:"validity"`
	Channel               string   `json:"channel"`
	AdvisoryID            string   `json:"advisory_id"`
	Reason                string   `json:"reason"`
	SourceReleaseSequence uint64   `json:"source_release_sequence"`
	TargetReleaseSequence uint64   `json:"target_release_sequence"`
	TargetPath            string   `json:"target_path"`
	TargetLength          int64    `json:"target_length"`
	TargetSHA256          string   `json:"target_sha256"`
}

var (
	idPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?$`)
	splitPattern   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,80}$`)
)

var knownProfiles = map[string]struct{}{
	"development":       {},
	"linux-baseline-v1": {},
	"linux-strict-v1":   {},
}

var knownCapabilities = map[string]struct{}{
	"same-fd-load": {}, "wx": {}, "relro": {}, "got-validation": {},
	"no-new-privs": {}, "dumpable-zero": {}, "seccomp-v1": {}, "landlock-v1": {},
	"maps-audit-v1": {}, "module-origin-v1": {},
}

func DecodeRoblox(data []byte, now time.Time, minimumSequence uint64) (RobloxPolicy, error) {
	var p RobloxPolicy
	if err := decodeCanonical(data, &p); err != nil {
		return p, err
	}
	if p.Schema != "tipsy.roblox-policy.v1" {
		return p, fail(CodeSchema, "got %q", p.Schema)
	}
	if err := validateValidity(p.Validity, now, minimumSequence); err != nil {
		return p, err
	}
	if p.PackageName != "com.roblox.client" || p.Platform != "android" || p.Architecture != "x86_64" {
		return p, fail(CodeUnsafeValue, "unexpected package/platform/architecture")
	}
	if p.MinVersionCode == 0 || p.MaxVersionCode < p.MinVersionCode {
		return p, fail(CodeUnsafeValue, "invalid version window")
	}
	if len(p.AllowedSplits) == 0 || len(p.AllowedSplits) > 32 || len(p.SignerLineages) == 0 || len(p.SignerLineages) > 16 {
		return p, fail(CodeBounds, "split or signer count")
	}
	if !slices.IsSorted(p.AllowedSplits) || adjacentDuplicate(p.AllowedSplits) {
		return p, fail(CodeNonCanonical, "allowed_splits must be sorted and unique")
	}
	for _, split := range p.AllowedSplits {
		if !splitPattern.MatchString(split) {
			return p, fail(CodeUnsafeValue, "invalid split name")
		}
	}
	for i, lineage := range p.SignerLineages {
		if !idPattern.MatchString(lineage.ID) || lineage.MinVersionCode == 0 || lineage.MaxVersionCode < lineage.MinVersionCode || len(lineage.SHA256) == 0 || len(lineage.SHA256) > 8 {
			return p, fail(CodeSignerMalformed, "lineage %d", i)
		}
		if !slices.IsSorted(lineage.SHA256) || adjacentDuplicate(lineage.SHA256) {
			return p, fail(CodeNonCanonical, "lineage digests must be sorted and unique")
		}
		for _, digest := range lineage.SHA256 {
			if !validSHA256(digest) {
				return p, fail(CodeSignerMalformed, "invalid signer digest")
			}
		}
	}
	return p, nil
}

func DecodeRuntime(data []byte, now time.Time, minimumSequence uint64) (RuntimePolicy, error) {
	var p RuntimePolicy
	if err := decodeCanonical(data, &p); err != nil {
		return p, err
	}
	if p.Schema != "tipsy.runtime-policy.v1" {
		return p, fail(CodeSchema, "got %q", p.Schema)
	}
	if err := validateValidity(p.Validity, now, minimumSequence); err != nil {
		return p, err
	}
	if !versionPattern.MatchString(p.MinimumTipsyVersion) {
		return p, fail(CodeUnsafeValue, "invalid minimum Tipsy version")
	}
	if len(p.RequiredCapabilities) > 16 || len(p.Selectors) == 0 || len(p.Selectors) > MaxItems {
		return p, fail(CodeBounds, "capability or selector count")
	}
	if !slices.IsSorted(p.RequiredCapabilities) || adjacentDuplicate(p.RequiredCapabilities) {
		return p, fail(CodeNonCanonical, "capabilities must be sorted and unique")
	}
	for _, capability := range p.RequiredCapabilities {
		if _, ok := knownCapabilities[capability]; !ok {
			return p, fail(CodeCapability, "%q", capability)
		}
	}
	for i, selector := range p.Selectors {
		if !oneOf(selector.ReleaseClass, "stable", "beta", "development") ||
			!oneOf(selector.RobloxClass, "authorized", "unknown") ||
			!oneOf(selector.KernelClass, "modern", "legacy", "unsupported") ||
			!oneOf(selector.BackendClass, "opengl", "vulkan", "software") {
			return p, fail(CodeUnsafeValue, "selector %d has an unknown class", i)
		}
		if _, ok := knownProfiles[selector.ProfileID]; !ok {
			return p, fail(CodeProfileUnknown, "%q", selector.ProfileID)
		}
	}
	return p, nil
}

func DecodeRecovery(data []byte, now time.Time, minimumSequence uint64) (RecoveryPolicy, error) {
	var p RecoveryPolicy
	if err := decodeCanonical(data, &p); err != nil {
		return p, err
	}
	if p.Schema != "tipsy.recovery-policy.v1" {
		return p, fail(CodeSchema, "got %q", p.Schema)
	}
	if err := validateValidity(p.Validity, now, minimumSequence); err != nil {
		return p, err
	}
	notBefore, _ := parseTime(p.Validity.NotBefore)
	expires, _ := parseTime(p.Validity.Expires)
	if expires.Sub(notBefore) > 7*24*time.Hour {
		return p, fail(CodeUnsafeValue, "recovery validity exceeds seven days")
	}
	if p.Channel != "stable" || !oneOf(p.Reason, "security_recall", "broken_release") ||
		!idPattern.MatchString(p.AdvisoryID) || p.SourceReleaseSequence == 0 ||
		p.TargetReleaseSequence == 0 || p.TargetReleaseSequence >= p.SourceReleaseSequence ||
		p.TargetLength <= 0 || !validSHA256(p.TargetSHA256) {
		return p, fail(CodeUnsafeValue, "invalid recovery authorization")
	}
	if err := ValidateTargetPath(p.TargetPath, "stable/"); err != nil {
		return p, err
	}
	return p, nil
}

func ValidateTargetPath(target, prefix string) error {
	if target == "" || len(target) > 240 || !utf8.ValidString(target) || strings.ContainsAny(target, "\\\x00") ||
		strings.HasPrefix(target, "/") || path.Clean(target) != target || !strings.HasPrefix(target, prefix) {
		return fail(CodeUnsafeValue, "unsafe target path")
	}
	for _, part := range strings.Split(target, "/") {
		if part == "" || part == "." || part == ".." || len(part) > 96 {
			return fail(CodeUnsafeValue, "unsafe target path component")
		}
	}
	return nil
}

func decodeCanonical(data []byte, dst any) error {
	if len(data) == 0 || len(data) > MaxPolicyBytes {
		return fail(CodeBounds, "policy size %d", len(data))
	}
	if err := validateJSONDepth(data, MaxJSONDepth); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fail(CodeMalformed, "%v", err)
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return fail(CodeMalformed, "trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return fail(CodeMalformed, "trailing data: %v", err)
	}
	canonical, err := json.Marshal(dst)
	if err != nil {
		return fail(CodeMalformed, "%v", err)
	}
	if !bytes.Equal(data, canonical) {
		return fail(CodeNonCanonical, "JSON must use the canonical schema encoding")
	}
	return nil
}

func validateJSONDepth(data []byte, maximum int) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	depth := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fail(CodeMalformed, "%v", err)
		}
		if delimiter, ok := tok.(json.Delim); ok {
			switch delimiter {
			case '{', '[':
				depth++
				if depth > maximum {
					return fail(CodeBounds, "JSON depth exceeds %d", maximum)
				}
			case '}', ']':
				depth--
			}
		}
	}
}

func validateValidity(v Validity, now time.Time, minimumSequence uint64) error {
	if v.Sequence == 0 {
		return fail(CodeUnsafeValue, "zero sequence")
	}
	if v.Sequence < minimumSequence {
		return fail(CodeRollback, "sequence %d is below %d", v.Sequence, minimumSequence)
	}
	notBefore, err := parseTime(v.NotBefore)
	if err != nil {
		return err
	}
	expires, err := parseTime(v.Expires)
	if err != nil {
		return err
	}
	if !expires.After(notBefore) || expires.Sub(notBefore) > 93*24*time.Hour {
		return fail(CodeUnsafeValue, "invalid validity window")
	}
	now = now.UTC()
	if now.Before(notBefore) {
		return fail(CodeNotYetValid, "policy is not active")
	}
	if !now.Before(expires) {
		return fail(CodeExpired, "policy has expired")
	}
	return nil
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.Format(time.RFC3339) != value || parsed.Location() != time.UTC {
		return time.Time{}, fail(CodeUnsafeValue, "timestamp must be canonical UTC RFC3339")
	}
	return parsed, nil
}

func validSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func adjacentDuplicate(values []string) bool {
	for i := 1; i < len(values); i++ {
		if values[i] == values[i-1] {
			return true
		}
	}
	return false
}

func oneOf(value string, allowed ...string) bool { return slices.Contains(allowed, value) }
