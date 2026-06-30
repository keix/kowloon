// Package httpapi is the HTTP front of kowloon-api. It owns routing,
// (un)marshalling, and status-code mapping. The actual indexing /
// searching work is delegated to a Service implementation — typically
// internal/indexer.Indexer in prod, a stub in tests or before the
// indexer wires up.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/keix/kowloon"
)

// Service is the work the HTTP layer dispatches into. Splitting it from
// the underlying Index / Source / Embed interfaces means the indexer can
// own composition (S3 fetch → schema convert → embed → upsert) without
// the HTTP layer having to know any of it.
type Service interface {
	IndexResult(ctx context.Context, req kowloon.IndexResultRequest) (kowloon.IndexResultResponse, error)
	Search(ctx context.Context, req kowloon.SearchRequest) (kowloon.SearchResponse, error)
	ResolveMerchant(ctx context.Context, req kowloon.ResolveMerchantRequest) (kowloon.ResolveMerchantResponse, error)
	DeleteJob(ctx context.Context, jobID string) error
}

// ErrNotImplemented is the sentinel a stub Service returns to signal
// "the route is wired but no implementation is attached yet". The HTTP
// layer maps it to 501 so stubbed builds are distinguishable from
// real 5xx failures.
var ErrNotImplemented = errors.New("kowloon: not implemented")

// ErrBadRequest wraps validation failures the Service layer detects
// (e.g. unknown ResultType, malformed source URI). The HTTP layer maps
// it to 400 so the indexer does not need to know about HTTP status
// codes.
type ErrBadRequest struct{ Err error }

func (e ErrBadRequest) Error() string { return e.Err.Error() }
func (e ErrBadRequest) Unwrap() error { return e.Err }

type Server struct {
	svc Service
}

func NewServer(svc Service) *Server {
	return &Server{svc: svc}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /v1/index-result", s.indexResult)
	mux.HandleFunc("POST /v1/search", s.search)
	mux.HandleFunc("POST /v1/resolve/merchant", s.resolveMerchant)
	mux.HandleFunc("DELETE /v1/jobs/{job_id}", s.deleteJob)
	return mux
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) indexResult(w http.ResponseWriter, r *http.Request) {
	var req kowloon.IndexResultRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validateIndexResult(req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	resp, err := s.svc.IndexResult(r.Context(), req)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	var req kowloon.SearchRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, errors.New("text is required"))
		return
	}

	resp, err := s.svc.Search(r.Context(), req)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) resolveMerchant(w http.ResponseWriter, r *http.Request) {
	var req kowloon.ResolveMerchantRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.MerchantRaw) == "" {
		writeError(w, http.StatusBadRequest, errors.New("merchant_raw is required"))
		return
	}

	resp, err := s.svc.ResolveMerchant(r.Context(), req)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) deleteJob(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimSpace(r.PathValue("job_id"))
	if jobID == "" {
		writeError(w, http.StatusBadRequest, errors.New("job_id is required"))
		return
	}
	if err := s.svc.DeleteJob(r.Context(), jobID); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validateIndexResult(req kowloon.IndexResultRequest) error {
	switch {
	case strings.TrimSpace(req.JobID) == "":
		return errors.New("job_id is required")
	case strings.TrimSpace(req.TenantID) == "":
		return errors.New("tenant_id is required")
	case strings.TrimSpace(req.ResultURI) == "":
		return errors.New("result_uri is required")
	case req.ResultType == "":
		return errors.New("result_type is required")
	case strings.TrimSpace(req.SchemaVersion) == "":
		return errors.New("schema_version is required")
	}
	return nil
}

func readJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(dst)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// writeServiceError maps Service-layer errors onto HTTP status codes.
// The mapping is intentionally narrow: only the two well-known
// sentinels get special handling, everything else surfaces as 500.
// Callers below the Service interface should wrap user-input failures
// in ErrBadRequest so they reach 400 here rather than 500.
func writeServiceError(w http.ResponseWriter, err error) {
	var badRequest ErrBadRequest
	switch {
	case errors.Is(err, ErrNotImplemented):
		writeError(w, http.StatusNotImplemented, err)
	case errors.As(err, &badRequest):
		writeError(w, http.StatusBadRequest, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}
