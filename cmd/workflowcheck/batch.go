package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
)

type manifestResponse struct {
	ManifestID string             `json:"manifest_id"`
	Replayed   bool               `json:"replayed"`
	Items      []fullSpecimenView `json:"items"`
}

type fullSpecimenView struct {
	ID            string    `json:"id"`
	CatalogNumber string    `json:"catalog_number"`
	Status        string    `json:"status"`
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type batchErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details []struct {
		Index  int    `json:"index"`
		Field  string `json:"field"`
		Code   string `json:"code"`
		Reason string `json:"reason"`
	} `json:"details"`
}

// checkRegisterManifest drives the real HTTP batch registration endpoint
// through its success, replay and atomic-failure paths.
func checkRegisterManifest(client *http.Client, baseURL string) error {
	first, err := submitManifest(client, baseURL, "M-2026-001", []contracts.RegisterSpecimenRequest{
		{
			CatalogNumber: "m-2026-010",
			FieldLabel:    "北坡-10",
			MatrixKind:    "粉砂岩",
			TaxonHint:     "大型脊椎动物",
		},
		{
			CatalogNumber: "M-2026-011",
			FieldLabel:    "北坡-11",
			MatrixKind:    "泥岩",
		},
	})
	if err != nil {
		return err
	}
	if first.Replayed {
		return errors.New("first manifest submission must not be reported as replay")
	}
	if len(first.Items) != 2 {
		return fmt.Errorf("expected 2 registered specimens, got %d", len(first.Items))
	}
	for index, item := range first.Items {
		if item.Status != "registered" {
			return fmt.Errorf("item %d status = %s, want registered", index+1, item.Status)
		}
	}
	// Catalog numbers are normalized to upper case before identity checks.
	if first.Items[0].CatalogNumber != "M-2026-010" {
		return fmt.Errorf("catalog number not normalized: %s", first.Items[0].CatalogNumber)
	}
	committedIDs := map[string]bool{}
	for _, item := range first.Items {
		committedIDs[item.ID] = true
		var fetched specimenView
		if err := requestJSON(client, http.MethodGet, baseURL+"/specimens/"+item.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.ID != item.ID || fetched.Status != "registered" {
			return fmt.Errorf("batch specimen %s is not readable with initial state", item.ID)
		}
	}

	// Resubmitting the identical manifest is an idempotent replay: same
	// specimens, 200 instead of 201, no additional rows.
	replay, err := submitManifestExpecting(client, baseURL, "M-2026-001", []contracts.RegisterSpecimenRequest{
		{
			CatalogNumber: " M-2026-010 ",
			FieldLabel:    "北坡-10",
			MatrixKind:    "粉砂岩",
			TaxonHint:     "大型脊椎动物",
		},
		{
			CatalogNumber: "M-2026-011",
			FieldLabel:    "北坡-11",
			MatrixKind:    "泥岩",
		},
	}, http.StatusOK)
	if err != nil {
		return fmt.Errorf("identical manifest replay rejected: %w", err)
	}
	if !replay.Replayed || len(replay.Items) != 2 {
		return errors.New("identical manifest must replay with the original specimens")
	}
	for _, item := range replay.Items {
		if !committedIDs[item.ID] {
			return fmt.Errorf("replay created a new specimen id %s", item.ID)
		}
	}
	if err := expectSpecimenCount(client, baseURL, 2); err != nil {
		return err
	}

	// Mid-batch duplicate catalog numbers fail the whole manifest at 409 and
	// must not leave even the valid prefix behind.
	midDup := []contracts.RegisterSpecimenRequest{
		{CatalogNumber: "M-2026-020", FieldLabel: "北坡-20", MatrixKind: "粉砂岩"},
		{CatalogNumber: "M-2026-021", FieldLabel: "北坡-21", MatrixKind: "泥岩"},
		{CatalogNumber: "M-2026-020", FieldLabel: "重复条目", MatrixKind: "泥灰岩"},
	}
	if err := expectManifestFailure(client, baseURL, "M-2026-002", midDup, http.StatusConflict, "manifest_conflict", "duplicate_catalog_number_in_batch", 3); err != nil {
		return err
	}
	if err := expectSpecimenCount(client, baseURL, 2); err != nil {
		return err
	}

	// Case-only collision after normalization is still an in-batch duplicate.
	caseDup := []contracts.RegisterSpecimenRequest{
		{CatalogNumber: "m-2026-030", FieldLabel: "北坡-30", MatrixKind: "粉砂岩"},
		{CatalogNumber: "M-2026-030", FieldLabel: "北坡-30b", MatrixKind: "泥岩"},
	}
	if err := expectManifestFailure(client, baseURL, "M-2026-003", caseDup, http.StatusConflict, "manifest_conflict", "duplicate_catalog_number_in_batch", 2); err != nil {
		return err
	}

	// One invalid field makes the entire batch 400, even if another entry
	// clashes with an existing specimen.
	invalidMix := []contracts.RegisterSpecimenRequest{
		{CatalogNumber: "M-2026-010", FieldLabel: "与库内冲突", MatrixKind: "粉砂岩"},
		{CatalogNumber: "M-2026-040", FieldLabel: "   ", MatrixKind: "泥岩"},
	}
	if err := expectManifestFailure(client, baseURL, "M-2026-004", invalidMix, http.StatusBadRequest, "invalid_manifest", "required_field", 2); err != nil {
		return err
	}

	// Conflict with a catalog number already stored must roll back the
	// otherwise valid sibling entries as well.
	storeConflict := []contracts.RegisterSpecimenRequest{
		{CatalogNumber: "M-2026-010", FieldLabel: "覆盖尝试", MatrixKind: "粉砂岩"},
		{CatalogNumber: "M-2026-041", FieldLabel: "北坡-41", MatrixKind: "泥岩"},
	}
	if err := expectManifestFailure(client, baseURL, "M-2026-005", storeConflict, http.StatusConflict, "manifest_conflict", "duplicate_catalog_number", 1); err != nil {
		return err
	}
	if err := expectSpecimenAbsent(client, baseURL, "M-2026-041"); err != nil {
		return err
	}
	if err := expectSpecimenCount(client, baseURL, 2); err != nil {
		return err
	}

	// Reusing a manifest ID with different content is a conflict, not a
	// replay, and never creates specimens.
	reused := []contracts.RegisterSpecimenRequest{
		{CatalogNumber: "M-2026-050", FieldLabel: "北坡-50", MatrixKind: "粉砂岩"},
	}
	if err := expectManifestFailure(client, baseURL, "M-2026-001", reused, http.StatusConflict, "manifest_conflict", "manifest_already_submitted", 0); err != nil {
		return err
	}
	if err := expectSpecimenAbsent(client, baseURL, "M-2026-050"); err != nil {
		return err
	}

	// Batch-created specimens carry the same initial facts as a single
	// registration: registered status, version 1 and equal timestamps.
	batchCreated := first.Items[0]
	var detail struct {
		Status    string    `json:"status"`
		Version   int       `json:"version"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	if err := requestJSON(client, http.MethodGet, baseURL+"/specimens/"+batchCreated.ID, nil, http.StatusOK, &detail); err != nil {
		return err
	}
	if detail.Status != "registered" || detail.Version != 1 || !detail.CreatedAt.Equal(detail.UpdatedAt) {
		return fmt.Errorf("batch specimen initial facts differ from single registration: %+v", detail)
	}
	if !first.Items[0].CreatedAt.Equal(first.Items[1].CreatedAt) {
		return errors.New("specimens in one batch commit must share the same creation time")
	}

	// The single-entry endpoint stays compatible with batch submissions.
	var single specimenView
	if err := requestJSON(client, http.MethodPost, baseURL+"/specimens", contracts.RegisterSpecimenRequest{
		CatalogNumber: "M-2026-099",
		FieldLabel:    "单条-99",
		MatrixKind:    "泥岩",
	}, http.StatusCreated, &single); err != nil {
		return err
	}
	if single.Status != "registered" {
		return fmt.Errorf("single registration status = %s, want registered", single.Status)
	}
	return nil
}

func submitManifest(
	client *http.Client,
	baseURL string,
	manifestID string,
	items []contracts.RegisterSpecimenRequest,
) (manifestResponse, error) {
	return submitManifestExpecting(client, baseURL, manifestID, items, http.StatusCreated)
}

func submitManifestExpecting(
	client *http.Client,
	baseURL string,
	manifestID string,
	items []contracts.RegisterSpecimenRequest,
	wantStatus int,
) (manifestResponse, error) {
	var response manifestResponse
	err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens/batch",
		contracts.RegisterManifestRequest{ManifestID: manifestID, Items: items},
		wantStatus,
		&response,
	)
	return response, err
}

func expectManifestFailure(
	client *http.Client,
	baseURL string,
	manifestID string,
	items []contracts.RegisterSpecimenRequest,
	wantStatus int,
	wantCode string,
	wantDetailCode string,
	wantIndex int,
) error {
	encoded, err := json.Marshal(contracts.RegisterManifestRequest{ManifestID: manifestID, Items: items})
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, baseURL+"/specimens/batch", bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	// A replay returns 200; first-time failure paths here expect 201 to be
	// refused. Accept either rejected status for decoding purposes.
	if response.StatusCode != wantStatus {
		return fmt.Errorf("manifest %s returned %d (want %d): %s", manifestID, response.StatusCode, wantStatus, string(raw))
	}
	var body batchErrorBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return err
	}
	if body.Code != wantCode {
		return fmt.Errorf("manifest %s error code = %s, want %s", manifestID, body.Code, wantCode)
	}
	for _, detail := range body.Details {
		if detail.Code == wantDetailCode && detail.Index == wantIndex {
			return nil
		}
	}
	return fmt.Errorf("manifest %s missing detail code=%s index=%d in %s", manifestID, wantDetailCode, wantIndex, string(raw))
}

func expectSpecimenCount(client *http.Client, baseURL string, want int) error {
	var list struct {
		Items []specimenView `json:"items"`
	}
	if err := requestJSON(client, http.MethodGet, baseURL+"/specimens", nil, http.StatusOK, &list); err != nil {
		return err
	}
	if len(list.Items) != want {
		return fmt.Errorf("expected %d specimens after rollback, store contains %d", want, len(list.Items))
	}
	return nil
}

func expectSpecimenAbsent(client *http.Client, baseURL, catalogNumber string) error {
	var list struct {
		Items []specimenView `json:"items"`
	}
	if err := requestJSON(client, http.MethodGet, baseURL+"/specimens", nil, http.StatusOK, &list); err != nil {
		return err
	}
	for _, item := range list.Items {
		if strings.EqualFold(item.CatalogNumber, catalogNumber) {
			return fmt.Errorf("rolled-back catalog number %s was persisted as specimen %s", catalogNumber, item.ID)
		}
	}
	return nil
}
