package releasemeta

import (
	"errors"
	"fmt"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

type ReasonCode string

const (
	ReasonOK                 ReasonCode = "ok"
	ReasonDevelopment        ReasonCode = "development_unrestricted"
	ReasonBootstrapMissing   ReasonCode = "release_bootstrap_missing"
	ReasonRootInvalid        ReasonCode = "tuf_root_invalid"
	ReasonRoleLayout         ReasonCode = "tuf_role_layout_invalid"
	ReasonSignatureThreshold ReasonCode = "tuf_signature_threshold"
	ReasonMetadataExpired    ReasonCode = "tuf_metadata_expired"
	ReasonMetadataRollback   ReasonCode = "tuf_metadata_rollback"
	ReasonMetadataMixMatch   ReasonCode = "tuf_metadata_mix_match"
	ReasonMetadataTooLarge   ReasonCode = "tuf_metadata_too_large"
	ReasonMetadataMalformed  ReasonCode = "tuf_metadata_malformed"
	ReasonMetadataMissing    ReasonCode = "tuf_metadata_missing"
	ReasonUnsafePath         ReasonCode = "unsafe_path"
	ReasonChannelMismatch    ReasonCode = "release_channel_mismatch"
	ReasonTargetMissing      ReasonCode = "release_target_missing"
	ReasonTargetMetadata     ReasonCode = "release_target_metadata_invalid"
	ReasonArtifactMismatch   ReasonCode = "release_artifact_mismatch"
	ReasonEvidenceMissing    ReasonCode = "release_evidence_missing"
	ReasonEvidenceMismatch   ReasonCode = "release_evidence_mismatch"
	ReasonEvidenceMalformed  ReasonCode = "release_evidence_malformed"
	ReasonPolicyInvalid      ReasonCode = "security_policy_invalid"
	ReasonStateUnsafe        ReasonCode = "rollback_state_unsafe"
	ReasonStateMissing       ReasonCode = "rollback_state_missing"
	ReasonStateRollback      ReasonCode = "rollback_state_regression"
	ReasonStateConflict      ReasonCode = "rollback_state_equivocation"
	ReasonInternal           ReasonCode = "verification_internal_error"
)

type Error struct {
	Code ReasonCode
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	if e.Msg != "" {
		return string(e.Code) + ": " + e.Msg
	}
	return string(e.Code)
}

func (e *Error) Unwrap() error { return e.Err }

func fail(code ReasonCode, format string, args ...any) error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

func wrap(code ReasonCode, err error, context string) error {
	return &Error{Code: code, Msg: context, Err: err}
}

func CodeOf(err error) ReasonCode {
	if err == nil {
		return ReasonOK
	}
	var releaseErr *Error
	if errors.As(err, &releaseErr) {
		return releaseErr.Code
	}
	var unsigned *metadata.ErrUnsignedMetadata
	if errors.As(err, &unsigned) {
		return ReasonSignatureThreshold
	}
	var expired *metadata.ErrExpiredMetadata
	if errors.As(err, &expired) {
		return ReasonMetadataExpired
	}
	var version *metadata.ErrBadVersionNumber
	if errors.As(err, &version) {
		return ReasonMetadataRollback
	}
	var mismatch *metadata.ErrLengthOrHashMismatch
	if errors.As(err, &mismatch) {
		return ReasonMetadataMixMatch
	}
	var tooLarge *metadata.ErrDownloadLengthMismatch
	if errors.As(err, &tooLarge) {
		return ReasonMetadataTooLarge
	}
	var download *metadata.ErrDownloadHTTP
	if errors.As(err, &download) {
		return ReasonMetadataMissing
	}
	var repository *metadata.ErrRepository
	if errors.As(err, &repository) {
		return ReasonMetadataMalformed
	}
	return ReasonInternal
}
