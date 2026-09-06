package releasemeta

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/theupdateframework/go-tuf/v2/metadata/trustedmetadata"
)

const stateFileName = "trusted-state.json"

type roleState struct {
	Version int64  `json:"version"`
	SHA256  string `json:"sha256"`
}

type rollbackState struct {
	Schema           string               `json:"schema"`
	Roles            map[string]roleState `json:"roles"`
	ReleaseSequences map[string]uint64    `json:"release_sequences"`
	PolicySequences  map[string]uint64    `json:"policy_sequences"`
}

func loadRollbackState(dir string) (rollbackState, bool, error) {
	empty := rollbackState{Schema: "tipsy.tuf-state.v1", Roles: map[string]roleState{}, ReleaseSequences: map[string]uint64{}, PolicySequences: map[string]uint64{}}
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return empty, true, nil
	}
	if err != nil {
		return empty, false, wrap(ReasonStateUnsafe, err, "inspect state directory")
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || !ownedByCurrentUser(info) {
		return empty, false, fail(ReasonStateUnsafe, "state directory must be owner-private")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return empty, false, wrap(ReasonStateUnsafe, err, "read state directory")
	}
	if len(entries) == 0 {
		return empty, false, fail(ReasonStateMissing, "existing state directory has no trusted state")
	}
	data, err := readNamedNoFollow(dir, stateFileName, 128<<10)
	if err != nil {
		return empty, false, wrap(ReasonStateUnsafe, err, "read trusted state")
	}
	info, err = os.Lstat(filepath.Join(dir, stateFileName))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !ownedByCurrentUser(info) {
		return empty, false, fail(ReasonStateUnsafe, "trusted state must be owner-private")
	}
	var state rollbackState
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return empty, false, wrap(ReasonStateUnsafe, err, "decode trusted state")
	}
	canonical, err := json.Marshal(state)
	if err != nil || !bytes.Equal(canonical, data) || state.Schema != empty.Schema || state.Roles == nil || state.ReleaseSequences == nil || state.PolicySequences == nil {
		return empty, false, fail(ReasonStateUnsafe, "trusted state is not canonical")
	}
	return state, false, nil
}

func persistRollbackState(dir string, state rollbackState, first bool) error {
	if first {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return wrap(ReasonStateUnsafe, err, "create state directory")
		}
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 || !ownedByCurrentUser(info) {
		return fail(ReasonStateUnsafe, "state directory changed or is unsafe")
	}
	data, err := json.Marshal(state)
	if err != nil {
		return wrap(ReasonInternal, err, "encode state")
	}
	temporary, err := os.CreateTemp(dir, ".trusted-state-*")
	if err != nil {
		return wrap(ReasonStateUnsafe, err, "create temporary state")
	}
	temporaryName := temporary.Name()
	remove := true
	defer func() {
		_ = temporary.Close()
		if remove {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return wrap(ReasonStateUnsafe, err, "protect temporary state")
	}
	if _, err := temporary.Write(data); err != nil {
		return wrap(ReasonStateUnsafe, err, "write temporary state")
	}
	if err := temporary.Sync(); err != nil {
		return wrap(ReasonStateUnsafe, err, "sync temporary state")
	}
	if err := temporary.Close(); err != nil {
		return wrap(ReasonStateUnsafe, err, "close temporary state")
	}
	if err := os.Rename(temporaryName, filepath.Join(dir, stateFileName)); err != nil {
		return wrap(ReasonStateUnsafe, err, "commit trusted state")
	}
	remove = false
	directory, err := os.Open(dir)
	if err != nil {
		return wrap(ReasonStateUnsafe, err, "open state directory")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return wrap(ReasonStateUnsafe, err, "sync state directory")
	}
	return nil
}

func reconcileRollbackState(previous rollbackState, trusted trustedmetadata.TrustedMetadata, fetcher *localFetcher, initialRoot []byte, channel string, releaseSequence, robloxSequence, runtimeSequence uint64) (rollbackState, error) {
	next := rollbackState{Schema: previous.Schema, Roles: cloneRoleMap(previous.Roles), ReleaseSequences: cloneSequenceMap(previous.ReleaseSequences), PolicySequences: cloneSequenceMap(previous.PolicySequences)}
	current := map[string]roleState{}
	add := func(role string, version int64, data []byte) error {
		if len(data) == 0 {
			return fail(ReasonStateUnsafe, "missing accepted bytes for %s", role)
		}
		digest := sha256.Sum256(data)
		current[role] = roleState{Version: version, SHA256: hex.EncodeToString(digest[:])}
		return nil
	}
	rootVersion := trusted.Root.Signed.Version
	rootBytes := initialRoot
	if candidate := fetcher.read(fmt.Sprintf("%d.root.json", rootVersion)); len(candidate) != 0 {
		rootBytes = candidate
	}
	if err := add("root", rootVersion, rootBytes); err != nil {
		return previous, err
	}
	if err := add("timestamp", trusted.Timestamp.Signed.Version, fetcher.read("timestamp.json")); err != nil {
		return previous, err
	}
	if err := add("snapshot", trusted.Snapshot.Signed.Version, fetcher.read(fmt.Sprintf("%d.snapshot.json", trusted.Snapshot.Signed.Version))); err != nil {
		return previous, err
	}
	for role, signed := range trusted.Targets {
		if signed == nil {
			continue
		}
		if err := add(role, signed.Signed.Version, fetcher.read(fmt.Sprintf("%d.%s.json", signed.Signed.Version, role))); err != nil {
			return previous, err
		}
	}
	for role, value := range current {
		if old, exists := previous.Roles[role]; exists {
			if value.Version < old.Version {
				return previous, fail(ReasonStateRollback, "%s version %d is below %d", role, value.Version, old.Version)
			}
			if value.Version == old.Version && value.SHA256 != old.SHA256 {
				return previous, fail(ReasonStateConflict, "%s version %d changed bytes", role, value.Version)
			}
		}
		next.Roles[role] = value
	}
	if releaseSequence < previous.ReleaseSequences[channel] {
		return previous, fail(ReasonStateRollback, "%s release sequence regressed", channel)
	}
	if robloxSequence < previous.PolicySequences["roblox"] || runtimeSequence < previous.PolicySequences["runtime"] {
		return previous, fail(ReasonStateRollback, "policy sequence regressed")
	}
	next.ReleaseSequences[channel] = releaseSequence
	next.PolicySequences["roblox"] = robloxSequence
	next.PolicySequences["runtime"] = runtimeSequence
	return next, nil
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}

func cloneRoleMap(input map[string]roleState) map[string]roleState {
	result := make(map[string]roleState, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func cloneSequenceMap(input map[string]uint64) map[string]uint64 {
	result := make(map[string]uint64, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
