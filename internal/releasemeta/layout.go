package releasemeta

import (
	"fmt"
	"slices"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/trustedmetadata"
)

type delegatedRequirement struct {
	threshold int
	keyCount  int
	paths     []string
}

var requiredDelegations = map[string]delegatedRequirement{
	"stable":         {threshold: 2, keyCount: 3, paths: []string{"stable/evidence/*/*", "stable/linux/x86_64/*"}},
	"beta":           {threshold: 1, keyCount: 2, paths: []string{"beta/evidence/*/*", "beta/linux/x86_64/*"}},
	"recovery":       {threshold: 2, keyCount: 3, paths: []string{"recovery/*"}},
	"roblox-policy":  {threshold: 2, keyCount: 3, paths: []string{"policies/roblox/*"}},
	"runtime-policy": {threshold: 2, keyCount: 3, paths: []string{"policies/runtime/*"}},
}

func validateRepositoryLayout(trusted trustedmetadata.TrustedMetadata) error {
	root := trusted.Root
	if root == nil || !root.Signed.ConsistentSnapshot {
		return fail(ReasonRoleLayout, "consistent_snapshot is required")
	}
	for role, expected := range map[string]struct{ threshold, keys int }{
		metadata.ROOT: {3, 5}, metadata.TARGETS: {2, 3}, metadata.SNAPSHOT: {1, 1}, metadata.TIMESTAMP: {1, 1},
	} {
		actual, ok := root.Signed.Roles[role]
		if !ok || actual.Threshold != expected.threshold || len(actual.KeyIDs) != expected.keys || hasDuplicate(actual.KeyIDs) {
			return fail(ReasonRoleLayout, "%s must be %d-of-%d", role, expected.threshold, expected.keys)
		}
		for _, keyID := range actual.KeyIDs {
			if _, ok := root.Signed.Keys[keyID]; !ok {
				return fail(ReasonRoleLayout, "%s references a missing key", role)
			}
		}
	}
	if intersects(root.Signed.Roles[metadata.SNAPSHOT].KeyIDs, root.Signed.Roles[metadata.TIMESTAMP].KeyIDs) {
		return fail(ReasonRoleLayout, "snapshot and timestamp keys must be distinct")
	}
	top := trusted.Targets[metadata.TARGETS]
	if top == nil || top.Signed.Delegations == nil || len(top.Signed.Targets) != 0 || top.Signed.Delegations.SuccinctRoles != nil {
		return fail(ReasonRoleLayout, "top-level targets must contain only explicit delegations")
	}
	if len(top.Signed.Delegations.Roles) != len(requiredDelegations) {
		return fail(ReasonRoleLayout, "unexpected delegation count")
	}
	roleKeySets := map[string][]string{}
	seenRoles := map[string]bool{}
	for _, role := range top.Signed.Delegations.Roles {
		requirement, ok := requiredDelegations[role.Name]
		if !ok || seenRoles[role.Name] || !role.Terminating || role.PathHashPrefixes != nil ||
			role.Threshold != requirement.threshold || len(role.KeyIDs) != requirement.keyCount || hasDuplicate(role.KeyIDs) {
			return fail(ReasonRoleLayout, "invalid delegation %q", role.Name)
		}
		paths := append([]string(nil), role.Paths...)
		slices.Sort(paths)
		if !slices.Equal(paths, requirement.paths) {
			return fail(ReasonRoleLayout, "delegation %q has broad or incorrect paths", role.Name)
		}
		for _, keyID := range role.KeyIDs {
			if _, ok := top.Signed.Delegations.Keys[keyID]; !ok {
				return fail(ReasonRoleLayout, "delegation %q references a missing key", role.Name)
			}
		}
		roleKeySets[role.Name] = role.KeyIDs
		seenRoles[role.Name] = true
	}
	for first, firstKeys := range roleKeySets {
		for second, secondKeys := range roleKeySets {
			if first < second && intersects(firstKeys, secondKeys) {
				return fail(ReasonRoleLayout, "delegations %q and %q share keys", first, second)
			}
		}
	}
	return nil
}

func targetOwner(trusted trustedmetadata.TrustedMetadata, targetPath string) (string, error) {
	owner := ""
	for role, signed := range trusted.Targets {
		if signed == nil {
			continue
		}
		if _, ok := signed.Signed.Targets[targetPath]; ok {
			if owner != "" {
				return "", fail(ReasonRoleLayout, "target appears in roles %q and %q", owner, role)
			}
			owner = role
		}
	}
	if owner == "" {
		return "", fail(ReasonTargetMissing, "%s", targetPath)
	}
	return owner, nil
}

func requireTargetOwner(trusted trustedmetadata.TrustedMetadata, targetPath, expected string) error {
	owner, err := targetOwner(trusted, targetPath)
	if err != nil {
		return err
	}
	if owner != expected {
		return fail(ReasonChannelMismatch, "%s belongs to %s, expected %s", targetPath, owner, expected)
	}
	return nil
}

func hasDuplicate(values []string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" || seen[value] {
			return true
		}
		seen[value] = true
	}
	return false
}

func intersects(left, right []string) bool {
	values := map[string]bool{}
	for _, value := range left {
		values[value] = true
	}
	for _, value := range right {
		if values[value] {
			return true
		}
	}
	return false
}

func metadataVersionSummary(trusted trustedmetadata.TrustedMetadata) string {
	return fmt.Sprintf("root=%d timestamp=%d snapshot=%d targets=%d", trusted.Root.Signed.Version, trusted.Timestamp.Signed.Version, trusted.Snapshot.Signed.Version, trusted.Targets[metadata.TARGETS].Signed.Version)
}
