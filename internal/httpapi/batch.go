package httpapi

import (
	"net/http"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/station"
)

func (api *API) registerSpecimensBatch(w http.ResponseWriter, r *http.Request) {
	var request contracts.RegisterSpecimensBatchRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	result, err := api.service.RegisterSpecimensBatch(r.Context(), request)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Status == station.BatchAlreadyRegistered {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}
