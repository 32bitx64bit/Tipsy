// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"reflect"
	"strings"
	"syscall"

	"github.com/tipsy-linux/tipsy/internal/jni"
)

const (
	desktopAppPolicyRelativePath = "appData/LocalStorage/appStorage.json"
	maxDesktopAppPolicyCacheSize = 1 << 20
	// The official 2.734.917 defaults contain at least 210 fields and the
	// current complete cached response contains 235. Refusing smaller objects
	// prevents a partial response from becoming a terminal whole-policy value.
	minCompleteAppPolicyFields = 200
)

var errDesktopAppPolicyUnavailable = errors.New("complete unambiguous app policy unavailable")

// desktopAppPolicyOverride derives presentation from Tipsy's existing Android
// PC form factor. It never changes the cache and touch diagnostic mode keeps
// the official policy unchanged.
func desktopAppPolicyOverride(filesDir string) (string, error) {
	if filesDir == "." || filesDir == "" || jni.PointerDeviceIsTouch() {
		return "", nil
	}
	raw, err := readDesktopAppPolicyCache(filesDir)
	if err != nil {
		return "", err
	}
	policy, err := selectCompleteCachedAppPolicy(raw)
	if err != nil {
		return "", err
	}
	overlay := clonePolicyMap(policy)
	overlay["UseGridHomePage"] = true
	overlay["UseGridPageLayout"] = true
	overlay["SystemBarPlacement"] = "Left"
	overlay["ShouldSystemBarUsuallyBePresent"] = true
	encoded, err := json.Marshal(overlay)
	if err != nil {
		return "", fmt.Errorf("encode desktop app policy: %w", err)
	}
	return string(encoded), nil
}

func readDesktopAppPolicyCache(filesDir string) ([]byte, error) {
	f, err := openDesktopAppPolicyCache(filesDir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("app policy cache is not an owner-private regular file")
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); !ok || sys.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("app policy cache has unexpected owner")
	}
	if st.Size() <= 0 || st.Size() > maxDesktopAppPolicyCacheSize {
		return nil, fmt.Errorf("app policy cache size is outside bounds")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxDesktopAppPolicyCacheSize+1))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > maxDesktopAppPolicyCacheSize {
		return nil, fmt.Errorf("app policy cache size is outside bounds")
	}
	return raw, nil
}

// openDesktopAppPolicyCache walks every component below FilesDir with
// O_NOFOLLOW. prepareAppStorage already hardens this tree in production; the
// descriptor-relative walk also closes symlink traversal races at read time.
func openDesktopAppPolicyCache(filesDir string) (*os.File, error) {
	rootFD, err := syscall.Open(filesDir, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	fd := rootFD
	for _, component := range []string{"appData", "LocalStorage"} {
		next, openErr := syscall.Openat(fd, component, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
		if fd != rootFD {
			_ = syscall.Close(fd)
		}
		if openErr != nil {
			_ = syscall.Close(rootFD)
			return nil, openErr
		}
		fd = next
	}
	fileFD, err := syscall.Openat(fd, "appStorage.json", syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if fd != rootFD {
		_ = syscall.Close(fd)
	}
	_ = syscall.Close(rootFD)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fileFD), desktopAppPolicyRelativePath), nil
}

func selectCompleteCachedAppPolicy(raw []byte) (map[string]any, error) {
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(raw, &outer); err != nil {
		return nil, fmt.Errorf("decode app policy cache: %w", err)
	}
	encodedConfiguration, ok := outer["AppConfiguration"]
	if !ok {
		return nil, errDesktopAppPolicyUnavailable
	}
	var configurationJSON string
	if err := json.Unmarshal(encodedConfiguration, &configurationJSON); err != nil {
		return nil, fmt.Errorf("decode app configuration string: %w", err)
	}
	var configuration map[string]json.RawMessage
	if err := json.Unmarshal([]byte(configurationJSON), &configuration); err != nil {
		return nil, fmt.Errorf("decode app configuration: %w", err)
	}

	var selected map[string]any
	for key, encodedPolicy := range configuration {
		if !strings.HasPrefix(key, "GUAC:") || !strings.HasSuffix(key, ":app-policy") {
			continue
		}
		var policyJSON string
		if err := json.Unmarshal(encodedPolicy, &policyJSON); err != nil {
			return nil, fmt.Errorf("decode cached app policy string: %w", err)
		}
		policy, err := decodeCompleteAppPolicy(policyJSON)
		if err != nil {
			return nil, err
		}
		if selected == nil {
			selected = policy
			continue
		}
		if !appPolicyValuesEqual(selected, policy) {
			return nil, errDesktopAppPolicyUnavailable
		}
	}
	if selected == nil {
		return nil, errDesktopAppPolicyUnavailable
	}
	return selected, nil
}

func appPolicyValuesEqual(left, right any) bool {
	switch leftValue := left.(type) {
	case map[string]any:
		rightValue, ok := right.(map[string]any)
		if !ok || len(leftValue) != len(rightValue) {
			return false
		}
		for key, value := range leftValue {
			other, ok := rightValue[key]
			if !ok || !appPolicyValuesEqual(value, other) {
				return false
			}
		}
		return true
	case []any:
		rightValue, ok := right.([]any)
		if !ok || len(leftValue) != len(rightValue) {
			return false
		}
		for i := range leftValue {
			if !appPolicyValuesEqual(leftValue[i], rightValue[i]) {
				return false
			}
		}
		return true
	case json.Number:
		rightValue, ok := right.(json.Number)
		if !ok {
			return false
		}
		leftNumber, leftOK := new(big.Rat).SetString(leftValue.String())
		rightNumber, rightOK := new(big.Rat).SetString(rightValue.String())
		return leftOK && rightOK && leftNumber.Cmp(rightNumber) == 0
	default:
		return reflect.DeepEqual(left, right)
	}
}

func decodeCompleteAppPolicy(raw string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var policy map[string]any
	if err := dec.Decode(&policy); err != nil {
		return nil, fmt.Errorf("decode cached app policy: %w", err)
	}
	if len(policy) < minCompleteAppPolicyFields {
		return nil, errDesktopAppPolicyUnavailable
	}
	if err := requireAppPolicyFieldTypes(policy); err != nil {
		return nil, err
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("cached app policy has trailing JSON")
	}
	return policy, nil
}

func requireAppPolicyFieldTypes(policy map[string]any) error {
	for _, key := range []string{"PlatformGroup", "SystemBarPlacement", "DevicePreferencesPersistentPresenceVariant"} {
		if _, ok := policy[key].(string); !ok {
			return errDesktopAppPolicyUnavailable
		}
	}
	if _, ok := policy["ShowUncheckedBadge"].(bool); !ok {
		return errDesktopAppPolicyUnavailable
	}
	return nil
}

func clonePolicyMap(policy map[string]any) map[string]any {
	out := make(map[string]any, len(policy)+4)
	for key, value := range policy {
		out[key] = value
	}
	return out
}
