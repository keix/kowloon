package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbed_Happy_ReordersByIndex(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization=%q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type=%q", got)
		}
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Model != "text-embedding-3-small" {
			t.Errorf("Model=%q", req.Model)
		}
		if len(req.Input) != 2 {
			t.Errorf("Input len=%d", len(req.Input))
		}

		// Respond out of order to exercise index-based reorder.
		_ = json.NewEncoder(w).Encode(response{Data: []dataEntry{
			{Embedding: []float32{0.2, 0.2}, Index: 1},
			{Embedding: []float32{0.1, 0.1}, Index: 0},
		}})
	}))
	defer server.Close()

	p := New(Config{APIKey: "test-key", Endpoint: server.URL})
	vecs, err := p.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 {
		t.Fatalf("vecs=%d", len(vecs))
	}
	if vecs[0][0] != 0.1 || vecs[1][0] != 0.2 {
		t.Errorf("order not restored: %v", vecs)
	}
}

func TestEmbed_ApiError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(errorResponse{Error: struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		}{Message: "Invalid API key", Type: "invalid_request_error"}})
	}))
	defer server.Close()

	p := New(Config{APIKey: "bad-key", Endpoint: server.URL})
	_, err := p.Embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("err=%v", err)
	}
}

func TestEmbed_CountMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(response{Data: []dataEntry{
			{Embedding: []float32{1}, Index: 0},
		}})
	}))
	defer server.Close()

	p := New(Config{APIKey: "k", Endpoint: server.URL})
	_, err := p.Embed(context.Background(), []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "1 embeddings for 2 inputs") {
		t.Errorf("err=%v", err)
	}
}

func TestEmbed_Empty(t *testing.T) {
	p := New(Config{APIKey: "k", Endpoint: "http://does-not-matter"})
	vecs, err := p.Embed(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if vecs != nil {
		t.Errorf("vecs=%v, want nil", vecs)
	}
}

func TestModelDim_Defaults(t *testing.T) {
	p := New(Config{APIKey: "k"})
	if p.Model() != DefaultModel {
		t.Errorf("Model()=%q, want %q", p.Model(), DefaultModel)
	}
	if p.Dim() != 1536 {
		t.Errorf("Dim()=%d, want 1536", p.Dim())
	}
}

func TestModelDim_InferredFromModel(t *testing.T) {
	cases := map[string]int{
		"text-embedding-3-small": 1536,
		"text-embedding-3-large": 3072,
		"text-embedding-ada-002": 1536,
	}
	for model, wantDim := range cases {
		t.Run(model, func(t *testing.T) {
			p := New(Config{APIKey: "k", Model: model})
			if p.Dim() != wantDim {
				t.Errorf("%s: Dim()=%d, want %d", model, p.Dim(), wantDim)
			}
		})
	}
}

func TestModelDim_ExplicitOverride(t *testing.T) {
	p := New(Config{APIKey: "k", Model: "text-embedding-3-large", Dim: 256})
	if p.Dim() != 256 {
		t.Errorf("explicit Dim=256, got %d", p.Dim())
	}
}
