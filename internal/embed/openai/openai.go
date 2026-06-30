// Package openai is the OpenAI Embeddings implementation of
// embed.Provider. It talks directly to the REST endpoint over net/http
// — the OpenAI Go SDK is intentionally avoided to keep the dependency
// surface narrow (one provider, one endpoint, ~150 LOC).
//
// Defaults target text-embedding-3-small (1536d); WithConfig overrides
// the model + dim pair for callers that want 3-large or a future
// reduced-dimension variant. The endpoint is also overridable so
// Azure OpenAI deployments and the test server slot in without
// touching the call site.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/keix/kowloon/internal/embed"
)

const (
	DefaultModel    = "text-embedding-3-small"
	DefaultModelDim = 1536
	DefaultEndpoint = "https://api.openai.com/v1/embeddings"
)

type Config struct {
	// APIKey is the OpenAI API key. The caller (typically main.go)
	// is responsible for validating it is non-empty before
	// constructing the Provider — New does not check.
	APIKey string

	// Model defaults to DefaultModel. When overriding, Dim must
	// also be set to match the model's native dimensionality (or
	// the value passed in the `dimensions` request parameter once
	// that surface lands).
	Model string
	Dim   int

	// Endpoint defaults to DefaultEndpoint. Override for Azure
	// OpenAI deployments or tests.
	Endpoint string

	// HTTPClient defaults to http.DefaultClient. Override to set
	// timeouts or custom transports.
	HTTPClient *http.Client
}

type Provider struct {
	apiKey     string
	model      string
	dim        int
	endpoint   string
	httpClient *http.Client
}

func New(c Config) *Provider {
	if c.Model == "" {
		c.Model = DefaultModel
	}
	if c.Dim == 0 {
		c.Dim = DefaultModelDim
	}
	if c.Endpoint == "" {
		c.Endpoint = DefaultEndpoint
	}
	if c.HTTPClient == nil {
		c.HTTPClient = http.DefaultClient
	}
	return &Provider{
		apiKey:     c.APIKey,
		model:      c.Model,
		dim:        c.Dim,
		endpoint:   c.Endpoint,
		httpClient: c.HTTPClient,
	}
}

var _ embed.Provider = (*Provider)(nil)

func (p *Provider) Model() string { return p.model }
func (p *Provider) Dim() int      { return p.dim }

type request struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

type dataEntry struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type response struct {
	Data []dataEntry `json:"data"`
}

type errorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func (p *Provider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(request{Input: texts, Model: p.model})
	if err != nil {
		return nil, fmt.Errorf("openai embed: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai embed: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai embed: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai embed: read body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var er errorResponse
		if jsonErr := json.Unmarshal(payload, &er); jsonErr == nil && er.Error.Message != "" {
			return nil, fmt.Errorf("openai embed: %s: %s", resp.Status, er.Error.Message)
		}
		return nil, fmt.Errorf("openai embed: %s: %s", resp.Status, string(payload))
	}

	var out response
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("openai embed: decode response: %w", err)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("openai embed: got %d embeddings for %d inputs", len(out.Data), len(texts))
	}

	// OpenAI's response is conventionally in-order, but the index field
	// is the wire contract — re-order defensively so callers get a
	// guaranteed input-order slice.
	vectors := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("openai embed: out-of-range index %d", d.Index)
		}
		if vectors[d.Index] != nil {
			return nil, fmt.Errorf("openai embed: duplicate index %d", d.Index)
		}
		vectors[d.Index] = d.Embedding
	}
	for i, v := range vectors {
		if v == nil {
			return nil, fmt.Errorf("openai embed: missing embedding for index %d", i)
		}
	}
	return vectors, nil
}
