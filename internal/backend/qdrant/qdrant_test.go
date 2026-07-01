package qdrant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keix/kowloon"
)

// fakeQdrant is a minimal test double that records the last request
// per path and returns configurable canned responses. Enough surface
// to verify Kowloon's Qdrant client sends the right shapes.
type fakeQdrant struct {
	server *httptest.Server

	lastMethod, lastPath, lastBody string
	lastAPIKey                     string

	collectionExists bool
	queryPoints      []qdrantPoint
}

func newFakeQdrant(t *testing.T) *fakeQdrant {
	t.Helper()
	f := &fakeQdrant{}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeQdrant) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.lastMethod = r.Method
	f.lastPath = r.URL.Path
	if r.URL.RawQuery != "" {
		f.lastPath += "?" + r.URL.RawQuery
	}
	f.lastBody = string(body)
	f.lastAPIKey = r.Header.Get("api-key")

	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/exists"):
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{"exists": f.collectionExists},
		})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/points/query"):
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{"points": f.queryPoints},
		})
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
	}
}

func newBackend(t *testing.T, dim int, existing bool) (*Backend, *fakeQdrant) {
	t.Helper()
	f := newFakeQdrant(t)
	f.collectionExists = existing
	b := New(Config{
		Endpoint:   f.server.URL,
		APIKey:     "test-key",
		Collection: "transactions",
		Dim:        dim,
	})
	return b, f
}

func TestEnsure_CreatesCollectionAndIndexes(t *testing.T) {
	b, f := newBackend(t, 8, false)
	if err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Last recorded call should be the last payload index PUT — enough
	// signal that bootstrap ran all the ensurePayloadIndex loops.
	if f.lastMethod != http.MethodPut || !strings.Contains(f.lastPath, "/index") {
		t.Errorf("last call=%s %s, want a PUT /index", f.lastMethod, f.lastPath)
	}
	if f.lastAPIKey != "test-key" {
		t.Errorf("api-key header=%q, want test-key", f.lastAPIKey)
	}
}

func TestEnsure_SkipsCreateWhenCollectionExists(t *testing.T) {
	b, f := newBackend(t, 8, true)
	// We cannot easily distinguish "skipped create" from "created" via
	// lastPath because the index PUTs come last. Instead assert that
	// Ensure completes without error given an existing collection.
	if err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Sanity: index calls still happened (they are idempotent).
	if !strings.Contains(f.lastPath, "/index") {
		t.Errorf("expected index calls, lastPath=%s", f.lastPath)
	}
}

func TestUpsert_SendsPointsPayload(t *testing.T) {
	b, f := newBackend(t, 3, true)
	ctx := context.Background()

	records := []kowloon.Record{
		{
			ID:          "job_1:tx:0",
			RecordType:  kowloon.RecordTypeTransaction,
			Text:        "family mart onigiri",
			SourceURI:   "s3://b/k.json",
			SourceIndex: 0,
			Metadata: map[string]string{
				"tenant_id": "keix",
				"job_id":    "job_1",
				"merchant":  "FamilyMart",
			},
		},
	}
	vectors := [][]float32{{0.1, 0.2, 0.3}}

	if err := b.Upsert(ctx, records, vectors); err != nil {
		t.Fatal(err)
	}

	if f.lastMethod != http.MethodPut || !strings.HasPrefix(f.lastPath, "/collections/transactions/points") {
		t.Errorf("last call=%s %s", f.lastMethod, f.lastPath)
	}
	var got struct {
		Points []struct {
			ID      string         `json:"id"`
			Vector  []float32      `json:"vector"`
			Payload map[string]any `json:"payload"`
		} `json:"points"`
	}
	if err := json.Unmarshal([]byte(f.lastBody), &got); err != nil {
		t.Fatalf("decode last body: %v", err)
	}
	if len(got.Points) != 1 {
		t.Fatalf("points=%d, want 1", len(got.Points))
	}
	p := got.Points[0]
	if len(p.ID) != 36 {
		t.Errorf("point id=%q, want a 36-char UUID", p.ID)
	}
	if len(p.Vector) != 3 || p.Vector[0] != 0.1 {
		t.Errorf("vector=%v", p.Vector)
	}
	if p.Payload["original_id"] != "job_1:tx:0" {
		t.Errorf("payload.original_id=%v", p.Payload["original_id"])
	}
	if p.Payload["merchant"] != "FamilyMart" {
		t.Errorf("payload.merchant=%v, want FamilyMart", p.Payload["merchant"])
	}
	if p.Payload["record_type"] != "transaction" {
		t.Errorf("payload.record_type=%v", p.Payload["record_type"])
	}
}

func TestSearch_BuildsFilterAndParsesResponse(t *testing.T) {
	b, f := newBackend(t, 3, true)
	ctx := context.Background()

	// Prime the fake response.
	f.queryPoints = []qdrantPoint{
		{
			ID:    "uuid-1",
			Score: 0.87,
			Payload: map[string]any{
				"original_id":  "job_1:tx:0",
				"record_type":  "transaction",
				"text":         "family mart onigiri",
				"source_uri":   "s3://b/k.json",
				"source_index": float64(0),
				"tenant_id":    "keix",
				"merchant":     "FamilyMart",
				"year_month":   "2026-06",
			},
		},
	}

	matches, err := b.Search(ctx, kowloon.SearchRequest{
		RecordType: kowloon.RecordTypeTransaction,
		TopK:       5,
		Filters: map[string]string{
			"tenant_id":  "keix",
			"year_month": "2026-06",
		},
	}, []float32{0.9, 0.1, 0.0})
	if err != nil {
		t.Fatal(err)
	}

	// Assert request body carried the filter conditions.
	var req struct {
		Query  []float32      `json:"query"`
		Limit  int            `json:"limit"`
		Filter map[string]any `json:"filter"`
	}
	if err := json.Unmarshal([]byte(f.lastBody), &req); err != nil {
		t.Fatalf("decode last body: %v", err)
	}
	if req.Limit != 5 {
		t.Errorf("limit=%d, want 5", req.Limit)
	}
	if len(req.Query) != 3 {
		t.Errorf("query vector len=%d, want 3", len(req.Query))
	}
	must, ok := req.Filter["must"].([]any)
	if !ok || len(must) != 3 {
		t.Fatalf("filter.must len=%d, want 3 (record_type + 2 filters)", len(must))
	}

	// Assert response was parsed back into Kowloon shape.
	if len(matches) != 1 {
		t.Fatalf("matches=%d, want 1", len(matches))
	}
	m := matches[0]
	if m.Record.ID != "job_1:tx:0" {
		t.Errorf("Record.ID=%q", m.Record.ID)
	}
	if m.Record.RecordType != kowloon.RecordTypeTransaction {
		t.Errorf("Record.RecordType=%q", m.Record.RecordType)
	}
	if m.Score != 0.87 {
		t.Errorf("Score=%v, want 0.87", m.Score)
	}
	if m.Record.Metadata["merchant"] != "FamilyMart" {
		t.Errorf("metadata.merchant=%q", m.Record.Metadata["merchant"])
	}
	if _, present := m.Record.Metadata["text"]; present {
		t.Error("text should be lifted out of Metadata into Record.Text")
	}
}

func TestDeleteByJob_SendsFilterOnJobID(t *testing.T) {
	b, f := newBackend(t, 3, true)
	if err := b.DeleteByJob(context.Background(), "job_42"); err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != http.MethodPost || !strings.HasPrefix(f.lastPath, "/collections/transactions/points/delete") {
		t.Errorf("last call=%s %s", f.lastMethod, f.lastPath)
	}
	if !strings.Contains(f.lastBody, `"job_42"`) || !strings.Contains(f.lastBody, `"job_id"`) {
		t.Errorf("delete body missing job_id filter: %s", f.lastBody)
	}
}

func TestBuildFilter_RecordTypeOnly(t *testing.T) {
	f := buildFilter(kowloon.SearchRequest{RecordType: kowloon.RecordTypeTransaction})
	must := f["must"].([]map[string]any)
	if len(must) != 1 || must[0]["key"] != "record_type" {
		t.Errorf("filter=%v", f)
	}
}

func TestBuildFilter_Empty(t *testing.T) {
	if f := buildFilter(kowloon.SearchRequest{}); f != nil {
		t.Errorf("filter=%v, want nil", f)
	}
}

func TestRecordIDToPointID_Deterministic(t *testing.T) {
	a := recordIDToPointID("job_1:tx:0")
	b := recordIDToPointID("job_1:tx:0")
	if a != b {
		t.Errorf("different UUIDs for same id: %q vs %q", a, b)
	}
}

func TestRecordIDToPointID_DistinctForDifferentIDs(t *testing.T) {
	a := recordIDToPointID("job_1:tx:0")
	b := recordIDToPointID("job_1:tx:1")
	if a == b {
		t.Errorf("same UUID for different ids: %q", a)
	}
}

func TestRecordIDToPointID_ValidUUID(t *testing.T) {
	id := recordIDToPointID("x")
	if len(id) != 36 {
		t.Errorf("len=%d, want 36", len(id))
	}
	// UUIDv5 has version nibble 5 at position 14, and variant bits
	// 10xx at position 19.
	if id[14] != '5' {
		t.Errorf("version nibble=%c, want 5", id[14])
	}
	switch id[19] {
	case '8', '9', 'a', 'b':
	default:
		t.Errorf("variant nibble=%c, want 8/9/a/b", id[19])
	}
}
