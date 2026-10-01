package station

import (
	"context"
	"fmt"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

// maxBatchItems bounds a single manifest so validation stays a single
// in-memory transaction.
const maxBatchItems = 500

type BatchStatus string

const (
	// BatchRegistered means this call committed a brand-new batch.
	BatchRegistered BatchStatus = "registered"
	// BatchAlreadyRegistered means the same manifest with the same content was
	// already committed; the original result is returned without creating
	// anything new.
	BatchAlreadyRegistered BatchStatus = "already_registered"
)

// BatchRegistration is the result of a manifest submission.
type BatchRegistration struct {
	ManifestID string            `json:"manifest_id"`
	Status     BatchStatus       `json:"status"`
	ItemCount  int               `json:"item_count"`
	Items      []domain.Specimen `json:"items"`
}

// RegisterSpecimensBatch registers every record of one manifest in a single
// transaction: either all specimens and the manifest record commit together,
// or nothing is persisted and generated identifiers are discarded. A repeated
// submission of the same manifest number is idempotent when the contents are
// identical and rejected as a conflict when they differ.
func (s *Service) RegisterSpecimensBatch(
	ctx context.Context,
	request contracts.RegisterSpecimensBatchRequest,
) (BatchRegistration, error) {
	if err := ctx.Err(); err != nil {
		return BatchRegistration{}, err
	}

	manifestID, err := domain.NormalizeManifestID(request.ManifestID)
	if err != nil {
		return BatchRegistration{}, err
	}
	if len(request.Items) == 0 {
		return BatchRegistration{}, contracts.Invalid("empty_batch", "清册至少需要包含一条登记记录")
	}
	if len(request.Items) > maxBatchItems {
		return BatchRegistration{}, contracts.Invalid(
			"batch_too_large",
			fmt.Sprintf("一份清册最多包含 %d 条登记记录", maxBatchItems),
		)
	}

	var result BatchRegistration
	err = s.repo.Mutate(func(m *repository.Mutation) error {
		now := s.now()
		specimens := make([]domain.Specimen, 0, len(request.Items))
		invalidIssues := make([]contracts.BatchItemIssue, 0)

		// Normalize and validate every entry through the same constructor used
		// by single registration, so initial state, version and timestamps are
		// identical. Generated IDs are thrown away if the batch fails.
		for index, item := range request.Items {
			value, itemErr := domain.NewSpecimen(
				s.nextID("specimen"),
				item.CatalogNumber,
				item.FieldLabel,
				item.TaxonHint,
				item.MatrixKind,
				item.JacketNotes,
				now,
			)
			if itemErr != nil {
				issue, ok := toBatchIssue(index, item.CatalogNumber, itemErr)
				if !ok {
					return itemErr
				}
				invalidIssues = append(invalidIssues, issue)
				continue
			}
			specimens = append(specimens, value)
		}
		if len(invalidIssues) > 0 {
			return contracts.InvalidBatch("invalid_batch_items", "清册中存在不合法的登记记录", invalidIssues)
		}

		// Duplicate catalog numbers inside the manifest itself.
		firstIndex := make(map[string]int, len(specimens))
		duplicateIssues := make([]contracts.BatchItemIssue, 0)
		for index, value := range specimens {
			if prior, seen := firstIndex[value.CatalogNumber]; seen {
				duplicateIssues = append(duplicateIssues, contracts.BatchItemIssue{
					Index:         index,
					CatalogNumber: value.CatalogNumber,
					Code:          "duplicate_catalog_number_in_batch",
					Message:       fmt.Sprintf("目录号与清册内第 %d 条记录重复", prior+1),
				})
				continue
			}
			firstIndex[value.CatalogNumber] = index
		}

		fingerprint := domain.ManifestFingerprint(specimens)

		// A resubmission of a manifest number that already committed is handled
		// before new-conflict checks so replays stay deterministic.
		if committed, exists := m.GetManifest(manifestID); exists {
			if committed.Fingerprint != fingerprint {
				return contracts.Conflict(
					"manifest_content_conflict",
					"清册号已提交但本次内容与首次提交不一致，整批未登记",
				)
			}
			items := make([]domain.Specimen, 0, len(committed.SpecimenIDs))
			for _, specimenID := range committed.SpecimenIDs {
				value, ok := m.GetSpecimen(specimenID)
				if !ok {
					return contracts.Internal(
						"manifest_replay_corrupt",
						"已提交清册引用的标本不存在",
						nil,
					)
				}
				items = append(items, value)
			}
			result = BatchRegistration{
				ManifestID: manifestID,
				Status:     BatchAlreadyRegistered,
				ItemCount:  len(items),
				Items:      items,
			}
			return nil
		}

		if len(duplicateIssues) > 0 {
			return contracts.BatchConflict(
				"duplicate_catalog_number_in_batch",
				"清册中存在重复目录号，整批未登记",
				duplicateIssues,
			)
		}

		// Conflict with specimens already registered by earlier requests.
		collisionIssues := make([]contracts.BatchItemIssue, 0)
		for index, value := range specimens {
			if _, exists := m.FindSpecimenByCatalog(value.CatalogNumber); exists {
				collisionIssues = append(collisionIssues, contracts.BatchItemIssue{
					Index:         index,
					CatalogNumber: value.CatalogNumber,
					Code:          "duplicate_catalog_number",
					Message:       "目录号已存在，不能覆盖原标本",
				})
			}
		}
		if len(collisionIssues) > 0 {
			return contracts.BatchConflict(
				"duplicate_catalog_number",
				"清册目录号与库内已有标本冲突，整批未登记",
				collisionIssues,
			)
		}

		ids := make([]string, 0, len(specimens))
		for _, value := range specimens {
			m.PutSpecimen(value)
			ids = append(ids, value.ID)
		}
		record, recordErr := domain.NewManifestRecord(manifestID, fingerprint, ids, now)
		if recordErr != nil {
			return recordErr
		}
		m.PutManifest(record)

		result = BatchRegistration{
			ManifestID: manifestID,
			Status:     BatchRegistered,
			ItemCount:  len(specimens),
			Items:      specimens,
		}
		return nil
	})
	return result, err
}

func toBatchIssue(index int, rawCatalog string, err error) (contracts.BatchItemIssue, bool) {
	appErr, ok := contracts.AsError(err)
	if !ok || appErr.Kind != contracts.KindInvalid {
		return contracts.BatchItemIssue{}, false
	}
	return contracts.BatchItemIssue{
		Index:         index,
		CatalogNumber: rawCatalog,
		Code:          appErr.Code,
		Message:       appErr.Message,
	}, true
}
