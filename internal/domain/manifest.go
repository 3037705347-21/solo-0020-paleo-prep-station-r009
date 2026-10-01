package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
)

// MaxBatchItems bounds how many entries a single manifest may carry.
const MaxBatchItems = 200

// RegistrationManifest records that one catalog manifest was committed as a
// batch. It exists solely to make resubmission detectable: a manifest ID with
// the same content fingerprint is an idempotent replay, while the same ID with
// different content is a conflicting reuse.
type RegistrationManifest struct {
	ID          string    `json:"id"`
	Fingerprint string    `json:"fingerprint"`
	SpecimenIDs []string  `json:"specimen_ids"`
	SubmittedAt time.Time `json:"submitted_at"`
}

// NewRegistrationManifest constructs a manifest record after all of its
// specimens have been validated and committed.
func NewRegistrationManifest(
	id string,
	fingerprint string,
	specimenIDs []string,
	now time.Time,
) (RegistrationManifest, error) {
	if id == "" {
		return RegistrationManifest{}, contracts.Internal("missing_manifest_id", "清册标识未生成", nil)
	}
	if fingerprint == "" {
		return RegistrationManifest{}, contracts.Internal("missing_manifest_fingerprint", "清册指纹未生成", nil)
	}
	ids := append([]string(nil), specimenIDs...)
	return RegistrationManifest{
		ID:          id,
		Fingerprint: fingerprint,
		SpecimenIDs: ids,
		SubmittedAt: now.UTC(),
	}, nil
}

// manifestFieldSep can never appear inside a trimmed free-text value because
// it is a control character; length prefixes still make the encoding
// unambiguous even for binary input.
const manifestFieldSep = "\x00"

// ManifestFingerprint hashes the normalized contents of a batch submission.
// Normalization mirrors NewSpecimen (trimmed, upper-cased catalog numbers) so
// that whitespace-only differences do not count as a different manifest.
func ManifestFingerprint(items []contracts.RegisterSpecimenRequest) string {
	var builder strings.Builder
	builder.WriteString("v1")
	for _, item := range items {
		catalog, err := NormalizeCatalogNumber(item.CatalogNumber)
		if err != nil {
			// Manifests containing invalid items are rejected before a
			// fingerprint is ever computed, but keep the helper total.
			catalog = strings.TrimSpace(item.CatalogNumber)
		}
		writePrefixed(&builder, catalog)
		writePrefixed(&builder, strings.TrimSpace(item.FieldLabel))
		writePrefixed(&builder, strings.TrimSpace(item.TaxonHint))
		writePrefixed(&builder, strings.TrimSpace(item.MatrixKind))
		writePrefixed(&builder, strings.TrimSpace(item.JacketNotes))
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}

func writePrefixed(builder *strings.Builder, value string) {
	builder.WriteString(manifestFieldSep)
	builder.WriteString(strconv.Itoa(len(value)))
	builder.WriteString(manifestFieldSep)
	builder.WriteString(value)
}
