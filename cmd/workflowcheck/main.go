package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/httpapi"
	"example.com/paleo-prep-station/internal/repository"
	"example.com/paleo-prep-station/internal/station"
)

type specimenView struct {
	ID            string `json:"id"`
	CatalogNumber string `json:"catalog_number"`
	Status        string `json:"status"`
}

type planView struct {
	ID         string `json:"id"`
	SpecimenID string `json:"specimen_id"`
	Status     string `json:"status"`
}

type planSnapshot struct {
	Plan     planView `json:"plan"`
	Progress []struct {
		Percent int `json:"percent"`
	} `json:"progress"`
}

type releaseView struct {
	ID         string `json:"id"`
	SpecimenID string `json:"specimen_id"`
}

type batchItemView struct {
	ID            string    `json:"id"`
	CatalogNumber string    `json:"catalog_number"`
	Status        string    `json:"status"`
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type batchResponse struct {
	ManifestID string          `json:"manifest_id"`
	Status     string          `json:"status"`
	ItemCount  int             `json:"item_count"`
	Items      []batchItemView `json:"items"`
}

type issueView struct {
	Index         int    `json:"index"`
	CatalogNumber string `json:"catalog_number"`
	Code          string `json:"code"`
	Message       string `json:"message"`
}

type errorResponse struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Details []issueView `json:"details"`
}

type specimenList struct {
	Items []struct {
		ID            string `json:"id"`
		CatalogNumber string `json:"catalog_number"`
	} `json:"items"`
}

func main() {
	workflow := flag.String("workflow", "", "workflow id to check")
	flag.Parse()

	if err := run(*workflow); err != nil {
		fmt.Fprintln(os.Stderr, "workflow check failed:", err)
		os.Exit(1)
	}
	fmt.Printf("workflow check passed: %s\n", *workflow)
}

func run(workflow string) error {
	handler := httpapi.NewRouter(station.New(repository.NewMemory()))
	server := httptest.NewServer(handler)
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}

	switch workflow {
	case "register-specimen":
		specimen, err := registerSpecimen(client, server.URL)
		if err != nil {
			return err
		}
		if specimen.Status != "registered" {
			return fmt.Errorf("expected registered status, got %s", specimen.Status)
		}
		var fetched specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.ID != specimen.ID {
			return errors.New("registered specimen cannot be read back")
		}
		return nil

	case "register-specimen-batch":
		return checkBatchRegistration(client, server.URL)

	case "register-specimen-batch-atomic":
		return checkBatchAtomicity(client, server.URL)

	case "approve-prep-plan":
		specimen, err := registerSpecimen(client, server.URL)
		if err != nil {
			return err
		}
		plan, err := createPlan(client, server.URL, specimen.ID)
		if err != nil {
			return err
		}
		var approved planView
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/plans/"+plan.ID+"/approval",
			contracts.ApprovalRequest{ApprovedBy: "curator-lin"},
			http.StatusOK,
			&approved,
		); err != nil {
			return err
		}
		if approved.Status != "approved" {
			return fmt.Errorf("expected approved plan, got %s", approved.Status)
		}
		var fetchedSpecimen specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetchedSpecimen); err != nil {
			return err
		}
		if fetchedSpecimen.Status != "preparing" {
			return fmt.Errorf("expected preparing specimen, got %s", fetchedSpecimen.Status)
		}
		return nil

	case "log-preparation":
		specimen, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 35, "矩阵剥离"); err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 100, "表面加固"); err != nil {
			return err
		}
		var snapshot planSnapshot
		if err := requestJSON(client, http.MethodGet, server.URL+"/plans/"+plan.ID, nil, http.StatusOK, &snapshot); err != nil {
			return err
		}
		if snapshot.Plan.Status != "completed" || len(snapshot.Progress) != 2 {
			return errors.New("plan did not complete with exactly two progress entries")
		}
		var fetchedSpecimen specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetchedSpecimen); err != nil {
			return err
		}
		if fetchedSpecimen.Status != "stabilized" {
			return fmt.Errorf("expected stabilized specimen, got %s", fetchedSpecimen.Status)
		}
		return nil

	case "release-for-study":
		specimen, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 100, "表面加固"); err != nil {
			return err
		}
		var release releaseView
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/specimens/"+specimen.ID+"/release",
			contracts.ReleaseRequest{
				Purpose:    "比较解剖研究",
				Recipient:  "地层研究组",
				ApprovedBy: "keeper-zhou",
			},
			http.StatusCreated,
			&release,
		); err != nil {
			return err
		}
		if release.SpecimenID != specimen.ID {
			return errors.New("release record references the wrong specimen")
		}
		var fetched specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.Status != "available" {
			return fmt.Errorf("expected available specimen, got %s", fetched.Status)
		}
		return nil
	default:
		return fmt.Errorf("unknown workflow %q", workflow)
	}
}

func registerSpecimen(client *http.Client, baseURL string) (specimenView, error) {
	var specimen specimenView
	err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens",
		contracts.RegisterSpecimenRequest{
			CatalogNumber: "PPS-2026-001",
			FieldLabel:    "北坡-07",
			TaxonHint:     "大型脊椎动物",
			MatrixKind:    "粉砂岩",
			JacketNotes:   "夹克边角完好",
		},
		http.StatusCreated,
		&specimen,
	)
	return specimen, err
}

func checkBatchRegistration(client *http.Client, baseURL string) error {
	items := []contracts.RegisterSpecimenRequest{
		{
			CatalogNumber: "pps-2026-101",
			FieldLabel:    "  北坡-21  ",
			TaxonHint:     "蜥脚类",
			MatrixKind:    "泥岩",
		},
		{
			CatalogNumber: "PPS-2026-102",
			FieldLabel:    "北坡-22",
			MatrixKind:    "粉砂岩",
			JacketNotes:   "夹克编号待回填",
		},
	}

	var batch batchResponse
	if err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens/batch",
		contracts.RegisterSpecimensBatchRequest{ManifestID: "manifest-2026-10", Items: items},
		http.StatusCreated,
		&batch,
	); err != nil {
		return err
	}
	if batch.Status != "registered" || batch.ItemCount != 2 || len(batch.Items) != 2 {
		return fmt.Errorf("unexpected first batch result: %+v", batch)
	}
	for index, item := range batch.Items {
		if item.Status != "registered" || item.Version != 1 {
			return fmt.Errorf("batch item %d not in single-register initial state: %+v", index, item)
		}
		if !item.CreatedAt.Equal(item.UpdatedAt) || item.CreatedAt.IsZero() {
			return fmt.Errorf("batch item %d has inconsistent timestamps: %+v", index, item)
		}
		var fetched batchItemView
		if err := requestJSON(client, http.MethodGet, baseURL+"/specimens/"+item.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.Status != "registered" || fetched.Version != 1 {
			return fmt.Errorf("fetched batch specimen differs in state: %+v", fetched)
		}
	}
	if batch.Items[0].CatalogNumber != "PPS-2026-101" {
		return fmt.Errorf("catalog number not normalized, got %q", batch.Items[0].CatalogNumber)
	}

	// Re-submit the same manifest with the same content: idempotent replay,
	// no new specimens.
	var replay batchResponse
	if err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens/batch",
		contracts.RegisterSpecimensBatchRequest{
			ManifestID: "  MANIFEST-2026-10 ",
			Items:      items,
		},
		http.StatusOK,
		&replay,
	); err != nil {
		return err
	}
	if replay.Status != "already_registered" || replay.ItemCount != 2 {
		return fmt.Errorf("unexpected replay result: %+v", replay)
	}
	for index := range replay.Items {
		if replay.Items[index].ID != batch.Items[index].ID {
			return errors.New("manifest replay returned different specimen ids")
		}
	}
	var list specimenList
	if err := requestJSON(client, http.MethodGet, baseURL+"/specimens", nil, http.StatusOK, &list); err != nil {
		return err
	}
	if len(list.Items) != 2 {
		return fmt.Errorf("replay created extra specimens, list length %d", len(list.Items))
	}

	// Same manifest number, different contents: conflict, still no new records.
	changedBody := contracts.RegisterSpecimensBatchRequest{
		ManifestID: "manifest-2026-10",
		Items: []contracts.RegisterSpecimenRequest{
			items[0],
			{
				CatalogNumber: "PPS-2026-999",
				FieldLabel:    "北坡-99",
				MatrixKind:    "页岩",
			},
		},
	}
	raw, err := requestJSONResponse(
		client,
		http.MethodPost,
		baseURL+"/specimens/batch",
		changedBody,
		http.StatusConflict,
		nil,
	)
	if err != nil {
		return err
	}
	var conflict errorResponse
	if err := json.Unmarshal(raw, &conflict); err != nil {
		return err
	}
	if conflict.Code != "manifest_content_conflict" {
		return fmt.Errorf("unexpected conflict code %q", conflict.Code)
	}
	if err := requestJSON(client, http.MethodGet, baseURL+"/specimens", nil, http.StatusOK, &list); err != nil {
		return err
	}
	if len(list.Items) != 2 {
		return fmt.Errorf("content-conflict submission changed the store, list length %d", len(list.Items))
	}
	return nil
}

func checkBatchAtomicity(client *http.Client, baseURL string) error {
	// Seed one specimen through the ordinary single-entry route.
	if _, err := registerSpecimen(client, baseURL); err != nil {
		return err
	}

	// Case 1: duplicate catalog number inside the manifest.
	dupInBatch := contracts.RegisterSpecimensBatchRequest{
		ManifestID: "manifest-dup-in-batch",
		Items: []contracts.RegisterSpecimenRequest{
			{CatalogNumber: "PPS-2026-201", FieldLabel: "东坡-01", MatrixKind: "泥岩"},
			{CatalogNumber: "pps-2026-201", FieldLabel: "东坡-02", MatrixKind: "页岩"},
		},
	}
	raw, err := requestJSONResponse(client, http.MethodPost, baseURL+"/specimens/batch", dupInBatch, http.StatusConflict, nil)
	if err != nil {
		return err
	}
	var conflict errorResponse
	if err := json.Unmarshal(raw, &conflict); err != nil {
		return err
	}
	if conflict.Code != "duplicate_catalog_number_in_batch" || len(conflict.Details) != 1 {
		return fmt.Errorf("unexpected in-batch duplicate error: %+v", conflict)
	}
	if conflict.Details[0].Index != 1 || conflict.Details[0].CatalogNumber != "PPS-2026-201" {
		return fmt.Errorf("unexpected issue detail: %+v", conflict.Details[0])
	}

	// Case 2: collision with the store plus an invalid field in one entry.
	collision := contracts.RegisterSpecimensBatchRequest{
		ManifestID: "manifest-collision",
		Items: []contracts.RegisterSpecimenRequest{
			{CatalogNumber: "PPS-2026-210", FieldLabel: "东坡-10", MatrixKind: "泥岩"},
			{CatalogNumber: "PPS-2026-001", FieldLabel: "东坡-11", MatrixKind: "粉砂岩"},
			{CatalogNumber: "PPS-2026-211", FieldLabel: "", MatrixKind: "泥岩"},
		},
	}
	if raw, err = requestJSONResponse(client, http.MethodPost, baseURL+"/specimens/batch", collision, http.StatusBadRequest, nil); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &conflict); err != nil {
		return err
	}
	if conflict.Code != "invalid_batch_items" || len(conflict.Details) != 1 {
		return fmt.Errorf("unexpected invalid-batch error: %+v", conflict)
	}
	if conflict.Details[0].Index != 2 || conflict.Details[0].Code != "required_field" {
		return fmt.Errorf("unexpected invalid issue detail: %+v", conflict.Details[0])
	}
	// The same corrected manifest id can be retried because the failed attempt
	// never registered a manifest record.
	collision.Items[2].FieldLabel = "东坡-12"
	if _, err := requestJSONResponse(client, http.MethodPost, baseURL+"/specimens/batch", collision, http.StatusConflict, nil); err != nil {
		return err
	}

	// Case 3: malformed manifest shape.
	badManifest := contracts.RegisterSpecimensBatchRequest{
		ManifestID: "bad id!",
		Items:      collision.Items,
	}
	if _, err := requestJSONResponse(client, http.MethodPost, baseURL+"/specimens/batch", badManifest, http.StatusBadRequest, nil); err != nil {
		return err
	}
	empty := contracts.RegisterSpecimensBatchRequest{ManifestID: "manifest-empty"}
	if _, err := requestJSONResponse(client, http.MethodPost, baseURL+"/specimens/batch", empty, http.StatusBadRequest, nil); err != nil {
		return err
	}

	// Exactly the seeded specimen plus nothing from the failed batches.
	var list specimenList
	if err := requestJSON(client, http.MethodGet, baseURL+"/specimens", nil, http.StatusOK, &list); err != nil {
		return err
	}
	if len(list.Items) != 1 {
		return fmt.Errorf("failed batches left partial specimens, list length %d: %+v", len(list.Items), list.Items)
	}
	if list.Items[0].CatalogNumber != "PPS-2026-001" {
		return fmt.Errorf("unexpected remaining specimen: %+v", list.Items[0])
	}

	// The failed manifest ids are reusable: submitting the corrected
	// in-batch-duplicate manifest now succeeds.
	recovered := contracts.RegisterSpecimensBatchRequest{
		ManifestID: "manifest-dup-in-batch",
		Items: []contracts.RegisterSpecimenRequest{
			{CatalogNumber: "PPS-2026-201", FieldLabel: "东坡-01", MatrixKind: "泥岩"},
			{CatalogNumber: "PPS-2026-202", FieldLabel: "东坡-02", MatrixKind: "页岩"},
		},
	}
	var batch batchResponse
	if err := requestJSON(client, http.MethodPost, baseURL+"/specimens/batch", recovered, http.StatusCreated, &batch); err != nil {
		return err
	}
	if batch.Status != "registered" || batch.ItemCount != 2 {
		return fmt.Errorf("unexpected recovered batch result: %+v", batch)
	}
	return nil
}

func createPlan(client *http.Client, baseURL, specimenID string) (planView, error) {
	var plan planView
	err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens/"+specimenID+"/plans",
		contracts.CreatePlanRequest{
			Objective:      "暴露主要关节面",
			Method:         "手工显微剥离",
			Adhesive:       "可逆树脂",
			TargetExposure: 80,
		},
		http.StatusCreated,
		&plan,
	)
	return plan, err
}

func completedPlanSetup(client *http.Client, baseURL string) (specimenView, planView, error) {
	specimen, err := registerSpecimen(client, baseURL)
	if err != nil {
		return specimenView{}, planView{}, err
	}
	plan, err := createPlan(client, baseURL, specimen.ID)
	if err != nil {
		return specimenView{}, planView{}, err
	}
	var approved planView
	if err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/plans/"+plan.ID+"/approval",
		contracts.ApprovalRequest{ApprovedBy: "curator-lin"},
		http.StatusOK,
		&approved,
	); err != nil {
		return specimenView{}, planView{}, err
	}
	return specimen, approved, nil
}

func recordProgress(client *http.Client, baseURL, planID string, percent int, stage string) error {
	var ignored struct{}
	return requestJSON(
		client,
		http.MethodPost,
		baseURL+"/plans/"+planID+"/progress",
		contracts.ProgressRequest{
			Stage:      stage,
			Percent:    percent,
			Note:       "本地链路检查",
			RecordedBy: "preparator-chen",
		},
		http.StatusCreated,
		&ignored,
	)
}

func requestJSON(
	client *http.Client,
	method string,
	url string,
	body any,
	wantStatus int,
	target any,
) error {
	_, err := requestJSONResponse(client, method, url, body, wantStatus, target)
	return err
}

func requestJSONResponse(
	client *http.Client,
	method string,
	url string,
	body any,
	wantStatus int,
	target any,
) ([]byte, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, payload)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != wantStatus {
		return raw, fmt.Errorf("request %s %s returned %d: %s", method, url, response.StatusCode, string(raw))
	}
	if target == nil || len(raw) == 0 {
		return raw, nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return raw, fmt.Errorf("decode response for %s %s: %w", method, url, err)
	}
	return raw, nil
}
