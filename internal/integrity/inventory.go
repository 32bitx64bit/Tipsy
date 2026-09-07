// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package integrity owns authenticated runtime-generation metadata and pinned
// descriptor verification. It does not load or execute guest code.
package integrity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

const (
	InventorySchema  = "tipsy.runtime-generation.v2"
	ActiveSchema     = "tipsy.runtime-active.v1"
	MaxInventorySize = 4 << 20
	MaxInventoryFile = 200000

	keylessReleaseSignerLineageID = "compiled-keyless-release"

	OriginAPK              = "apk"
	OriginDerived          = "derived"
	OriginOfficialExternal = "official-external"
)

var (
	generationPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
	lineageIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

type FileRecord struct {
	Path         string `json:"path"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	Origin       string `json:"origin"`
	APKEntry     string `json:"apk_entry,omitempty"`
	APKDigest    string `json:"apk_digest,omitempty"`
	PolicyOrigin string `json:"policy_origin,omitempty"`
	Executable   bool   `json:"executable"`
}

type Inventory struct {
	Schema            string       `json:"schema"`
	PackageName       string       `json:"package_name"`
	VersionName       string       `json:"version_name"`
	VersionCode       int64        `json:"version_code"`
	PolicySequence    uint64       `json:"policy_sequence"`
	AuthorizationMode string       `json:"authorization_mode"`
	PolicyAuthorized  bool         `json:"policy_authorized"`
	SignerLineageID   string       `json:"signer_lineage_id"`
	SignerLineage     []string     `json:"signer_lineage_sha256"`
	Splits            []string     `json:"splits"`
	Files             []FileRecord `json:"files"`
}

type ActiveRecord struct {
	Schema          string `json:"schema"`
	Generation      string `json:"generation"`
	InventorySHA256 string `json:"inventory_sha256"`
}

func CanonicalInventory(in Inventory) ([]byte, string, error) {
	if err := validateInventory(in); err != nil {
		return nil, "", err
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, "", fmt.Errorf("integrity: encode inventory: %w", err)
	}
	if len(raw) > MaxInventorySize {
		return nil, "", fmt.Errorf("integrity: inventory exceeds %d bytes", MaxInventorySize)
	}
	digest := sha256.Sum256(raw)
	return raw, hex.EncodeToString(digest[:]), nil
}

func DecodeInventory(raw []byte) (Inventory, string, error) {
	var in Inventory
	if len(raw) == 0 || len(raw) > MaxInventorySize {
		return in, "", fmt.Errorf("integrity: invalid inventory size")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, "", fmt.Errorf("integrity: decode inventory: %w", err)
	}
	canonical, digest, err := CanonicalInventory(in)
	if err != nil {
		return in, "", err
	}
	if !slices.Equal(canonical, raw) {
		return in, "", fmt.Errorf("integrity: inventory is not canonical")
	}
	return in, digest, nil
}

func validateInventory(in Inventory) error {
	if in.Schema != InventorySchema || in.PackageName != "com.roblox.client" || in.VersionName == "" || in.VersionCode <= 0 {
		return fmt.Errorf("integrity: invalid package inventory identity")
	}
	if !lineageIDPattern.MatchString(in.SignerLineageID) || len(in.SignerLineage) == 0 || len(in.SignerLineage) > 32 {
		return fmt.Errorf("integrity: invalid signer lineage")
	}
	switch in.AuthorizationMode {
	case "official-verified":
		if !in.PolicyAuthorized {
			return fmt.Errorf("integrity: official generation lacks authenticated policy")
		}
		if in.SignerLineageID == keylessReleaseSignerLineageID {
			if in.PolicySequence != 0 {
				return fmt.Errorf("integrity: keyless official generation has invalid policy sequence")
			}
		} else if in.PolicySequence == 0 {
			return fmt.Errorf("integrity: official generation lacks authenticated policy")
		}
	case "development-unrestricted":
		if in.PolicyAuthorized || in.PolicySequence != 0 || in.SignerLineageID == keylessReleaseSignerLineageID {
			return fmt.Errorf("integrity: development generation claims official policy")
		}
	default:
		return fmt.Errorf("integrity: invalid generation authorization mode")
	}
	for _, digest := range in.SignerLineage {
		if !validDigest(digest) {
			return fmt.Errorf("integrity: invalid signer lineage digest")
		}
	}
	lineageSet := make(map[string]struct{}, len(in.SignerLineage))
	for _, digest := range in.SignerLineage {
		if _, duplicate := lineageSet[digest]; duplicate {
			return fmt.Errorf("integrity: signer lineage repeats a certificate")
		}
		lineageSet[digest] = struct{}{}
	}
	if len(in.Splits) == 0 || !slices.IsSorted(in.Splits) || hasDuplicate(in.Splits) {
		return fmt.Errorf("integrity: splits must be sorted and unique")
	}
	if len(in.Files) == 0 || len(in.Files) > MaxInventoryFile {
		return fmt.Errorf("integrity: invalid inventory file count")
	}
	previous := ""
	for _, file := range in.Files {
		if !safeRelative(file.Path) || file.Path <= previous || file.Size < 0 || !validDigest(file.SHA256) {
			return fmt.Errorf("integrity: invalid inventory file record")
		}
		switch file.Origin {
		case OriginAPK:
			if !validDigest(file.APKDigest) || !safeAPKEntry(file.APKEntry) || file.APKEntry == "@derived/meta" || file.PolicyOrigin != "" {
				return fmt.Errorf("integrity: invalid APK-derived file provenance")
			}
		case OriginDerived:
			if file.Path != "meta.json" || file.APKEntry != "@derived/meta" || file.APKDigest != "" || file.PolicyOrigin != "" || file.Executable {
				return fmt.Errorf("integrity: invalid derived file provenance")
			}
		case OriginOfficialExternal:
			if file.APKEntry != "" || file.APKDigest != "" || !safePolicyOrigin(file.PolicyOrigin) || file.Executable {
				return fmt.Errorf("integrity: invalid official external file provenance")
			}
		default:
			return fmt.Errorf("integrity: unknown file provenance")
		}
		if file.Executable && file.Origin != OriginAPK {
			return fmt.Errorf("integrity: executable file is not APK-derived")
		}
		previous = file.Path
	}
	return nil
}

func ValidateGenerationID(id string) error {
	if !generationPattern.MatchString(id) {
		return fmt.Errorf("integrity: invalid generation id")
	}
	return nil
}

func safeRelative(name string) bool {
	if name == "" || len(name) > 512 || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00") || path.Clean(name) != name {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func safeAPKEntry(name string) bool {
	if name == "@apk" || name == "@derived/meta" {
		return true
	}
	return safeRelative(name)
}

func safePolicyOrigin(name string) bool {
	// Policy origins are authenticated target identifiers, never source URLs,
	// account identifiers, or download query strings.
	return safeRelative(name) && !strings.Contains(name, ":") && len(name) <= 256
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func hasDuplicate(values []string) bool {
	for i := 1; i < len(values); i++ {
		if values[i] == values[i-1] {
			return true
		}
	}
	return false
}
