package router

import (
	"net/http"

	"github.com/2SSK/tenantflow/internal/handler"
)

func New() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /status", handler.Status)

	return mux
}
