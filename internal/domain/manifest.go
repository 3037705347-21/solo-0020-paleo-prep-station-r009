package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
)

var manifestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ManifestRecord keeps a successfully committed registration batch so that a
// resubmission of the same manifest can be answered idempotently.
type ManifestRecord struct {
	ID          string
	Fingerprint string
	SpecimenIDs []string
	CreatedAt   time.Time
}

// NormalizeManifestID trims and upper-cases a client-provided manifest number.
func NormalizeManifestID(value string) (string, error) {
	cleaned := strings.TrimSpace(value)
	if cleaned == "" {
		return "", contracts.Invalid("required_field", "清册号不能为空")
	}
	if !manifestIDPattern.MatchString(cleaned) {
		return "", contracts.Invalid(
			"invalid_manifest_id",
			"清册号只能包含字母、数字、点、下划线和连字符，长度不超过 64",
		)
	}
	return strings.ToUpper(cleaned), nil
}

// NewManifestRecord constructs the committed-batch record for a manifest.
func NewManifestRecord(
	id string,
	fingerprint string,
	specimenIDs []string,
	now time.Time,
) (ManifestRecord, error) {
	if id == "" {
		return ManifestRecord{}, contracts.Internal("missing_manifest_id", "清册标识缺失", nil)
	}
	if fingerprint == "" {
		return ManifestRecord{}, contracts.Internal("missing_manifest_fingerprint", "清册内容指纹缺失", nil)
	}
	if len(specimenIDs) == 0 {
		return ManifestRecord{}, contracts.Internal("empty_manifest_batch", "清册没有已登记标本", nil)
	}
	ids := append([]string(nil), specimenIDs...)
	return ManifestRecord{
		ID:          id,
		Fingerprint: fingerprint,
		SpecimenIDs: ids,
		CreatedAt:   now.UTC(),
	}, nil
}

// ManifestFingerprint hashes the normalized contents of a batch. Item order is
// part of the fingerprint, so only an identical resubmission of the same
// manifest is accepted as a replay.
func ManifestFingerprint(items []Specimen) string {
	hasher := sha256.New()
	for index, item := range items {
		hasher.Write([]byte{byte(index >> 24), byte(index >> 16), byte(index >> 8), byte(index)})
		hasher.Write([]byte(item.CatalogNumber))
		hasher.Write([]byte{0})
		hasher.Write([]byte(item.FieldLabel))
		hasher.Write([]byte{0})
		hasher.Write([]byte(item.TaxonHint))
		hasher.Write([]byte{0})
		hasher.Write([]byte(item.MatrixKind))
		hasher.Write([]byte{0})
		hasher.Write([]byte(item.JacketNotes))
		hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}
