package handler

import (
	"net/http"
)

type StatusResponse struct {
	Status string `json:"status"`
}

func Status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, StatusResponse{Status: "ok"})
}
