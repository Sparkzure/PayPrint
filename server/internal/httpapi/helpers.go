package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

func paramString(r *http.Request, name string) string {
	return strings.TrimSpace(chi.URLParam(r, name))
}

func queryInt(r *http.Request, name string, def int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

func queryString(r *http.Request, name string) string {
	return strings.TrimSpace(r.URL.Query().Get(name))
}

func queryBool(r *http.Request, name string, def bool) bool {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return def
	}
	return v
}
