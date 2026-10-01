package station

import (
	"context"
	"strconv"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

// ManifestResult is the observable outcome of a batch submission. Replayed is
// true when the manifest had already been committed and no new specimen was
// created.
type ManifestResult struct {
	ManifestID string            `json:"manifest_id"`
	Replayed   bool              `json:"replayed"`
	Items      []domain.Specimen `json:"items"`
}

// RegisterSpecimenBatch validates and commits one whole catalog manifest in a
// single repository mutation: either every entry becomes a registered
// specimen together with the manifest record, or nothing is persisted and no
// identifier is observable afterwards.
func (s *Service) RegisterSpecimenBatch(
	ctx context.Context,
	request contracts.RegisterManifestRequest,
) (ManifestResult, error) {
	if err := ctx.Err(); err != nil {
		return ManifestResult{}, err
	}

	if len(request.Items) == 0 {
		return ManifestResult{}, contracts.Invalid("empty_manifest", "清册至少需要包含一条登记条目")
	}
	if len(request.Items) > domain.MaxBatchItems {
		return ManifestResult{}, contracts.Invalid(
			"manifest_too_large",
			"单份清册最多包含 "+strconv.Itoa(domain.MaxBatchItems)+" 条登记条目",
		)
	}
	manifestID, err := domain.NormalizeManifestID(request.ManifestID)
	if err != nil {
		return ManifestResult{}, err
	}
	fingerprint := domain.ManifestFingerprint(request.Items)

	var result ManifestResult
	err = s.repo.Mutate(func(m *repository.Mutation) error {
		now := s.now()

		specimens := make([]domain.Specimen, 0, len(request.Items))
		invalidDetails := make([]contracts.ErrorDetail, 0)

		// First occurrence of each normalized catalog number inside this
		// manifest; every later occurrence is reported as an in-batch
		// duplicate (1-based positions).
		firstByCatalog := make(map[string]int)
		duplicateDetails := make([]contracts.ErrorDetail, 0)

		for position, item := range request.Items {
			index := position + 1
			value, buildErr := domain.NewSpecimen(
				s.nextID("specimen"),
				item.CatalogNumber,
				item.FieldLabel,
				item.TaxonHint,
				item.MatrixKind,
				item.JacketNotes,
				now,
			)
			if buildErr != nil {
				invalidDetails = append(invalidDetails, detailFromError(index, buildErr))
				continue
			}
			if first, duplicated := firstByCatalog[value.CatalogNumber]; duplicated {
				duplicateDetails = append(duplicateDetails, contracts.ErrorDetail{
					Index: index,
					Field: "catalog_number",
					Code:  "duplicate_catalog_number_in_batch",
					Reason: "目录号与清册内第 " + strconv.Itoa(first) +
						" 条记录重复：" + value.CatalogNumber,
				})
				continue
			}
			firstByCatalog[value.CatalogNumber] = index
			specimens = append(specimens, value)
		}

		// Structural entry failures are reported independently of manifest
		// history so a malformed resubmission can never look like a replay.
		if len(invalidDetails) > 0 {
			return contracts.InvalidBatch("invalid_manifest", "清册中存在不能登记的条目", invalidDetails)
		}

		// A manifest that had already been committed is resolved before any
		// store-conflict check: an identical payload is an idempotent replay
		// (its catalog numbers naturally already exist), a changed payload is
		// a manifest-id conflict.
		if stored, exists := m.GetManifest(manifestID); exists {
			if stored.Fingerprint != fingerprint {
				return contracts.ConflictBatch("manifest_conflict", "清册与已提交数据冲突", []contracts.ErrorDetail{{
					Field:  "manifest_id",
					Code:   "manifest_already_submitted",
					Reason: "清册编号已提交过且本次内容与原清册不一致",
				}})
			}
			replayed := make([]domain.Specimen, 0, len(stored.SpecimenIDs))
			for _, specimenID := range stored.SpecimenIDs {
				value, ok := m.GetSpecimen(specimenID)
				if !ok {
					return contracts.Internal(
						"manifest_replay_corrupt",
						"已提交清册引用的标本缺失："+specimenID,
						nil,
					)
				}
				replayed = append(replayed, value)
			}
			result = ManifestResult{ManifestID: manifestID, Replayed: true, Items: replayed}
			return nil
		}

		// Every entry is structurally valid; verify uniqueness inside the
		// batch and against catalog numbers already in the store.
		conflictDetails := duplicateDetails
		for _, value := range specimens {
			if _, exists := m.FindSpecimenByCatalog(value.CatalogNumber); exists {
				conflictDetails = append(conflictDetails, contracts.ErrorDetail{
					Index:  indexOf(specimens, value.CatalogNumber),
					Field:  "catalog_number",
					Code:   "duplicate_catalog_number",
					Reason: "目录号已存在，不能覆盖原标本：" + value.CatalogNumber,
				})
			}
		}
		if len(conflictDetails) > 0 {
			return contracts.ConflictBatch("manifest_conflict", "清册与已提交数据冲突", conflictDetails)
		}

		specimenIDs := make([]string, 0, len(specimens))
		for _, value := range specimens {
			m.PutSpecimen(value)
			specimenIDs = append(specimenIDs, value.ID)
		}
		manifest, err := domain.NewRegistrationManifest(manifestID, fingerprint, specimenIDs, now)
		if err != nil {
			return err
		}
		m.PutManifest(manifest)
		result = ManifestResult{ManifestID: manifestID, Replayed: false, Items: specimens}
		return nil
	})
	if err != nil {
		return ManifestResult{}, err
	}
	return result, nil
}

// indexOf returns the 1-based position of a committed candidate inside the
// batch, used to attribute store-conflict details accurately.
func indexOf(specimens []domain.Specimen, catalogNumber string) int {
	for position, value := range specimens {
		if value.CatalogNumber == catalogNumber {
			return position + 1
		}
	}
	return 0
}

// detailFromError converts a domain construction error for one entry into a
// batch detail, preserving its stable code.
func detailFromError(index int, err error) contracts.ErrorDetail {
	detail := contracts.ErrorDetail{
		Index:  index,
		Field:  "catalog_number",
		Code:   "invalid_entry",
		Reason: err.Error(),
	}
	if appErr, ok := contracts.AsError(err); ok {
		detail.Code = appErr.Code
		// The domain message already names the offending field in Chinese;
		// drop the placeholder field so it is not mistaken for one.
		if appErr.Code == "required_field" || appErr.Code == "field_too_long" {
			detail.Field = ""
		}
	}
	return detail
}
