package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	pineconeAPIVersion = "2025-04"
	// maxUpsertBatch is the number of vectors per upsert request (Pinecone allows up to 1000 / 2MB).
	maxUpsertBatch = 100
	// maxDeleteBatch is Pinecone's max number of IDs per delete request.
	maxDeleteBatch = 1000
	defaultTopK    = 10
)

// Pinecone is a Store backed by a Pinecone index, using its REST data-plane API. A dataset is a Pinecone
// namespace of the index (its own isolation primitive: namespaces are created on first write and are searched
// separately), so all datasets share one index and its dimension.
type Pinecone struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

var _ Store = (*Pinecone)(nil)

// PineconeOption configures a Pinecone store.
type PineconeOption func(*Pinecone)

// WithPineconeHTTPClient sets a custom HTTP client.
func WithPineconeHTTPClient(h *http.Client) PineconeOption {
	return func(p *Pinecone) { p.httpClient = h }
}

// NewPinecone creates a store for the index served at indexHost, the index's data-plane host
// shown in the Pinecone console (e.g. "my-index-abc123.svc.aped-1234.pinecone.io").
// A scheme prefix is accepted (useful for tests/proxies); https is assumed otherwise.
func NewPinecone(apiKey, indexHost string, opts ...PineconeOption) (*Pinecone, error) {
	if apiKey == "" {
		return nil, errors.New("vector: missing Pinecone API key (set PINECONE_API_KEY)")
	}
	host := strings.TrimRight(strings.TrimSpace(indexHost), "/")
	if host == "" {
		return nil, errors.New("vector: missing Pinecone index host (set pinecone_index_host)")
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	p := &Pinecone{
		apiKey:     apiKey,
		baseURL:    host,
		httpClient: &http.Client{Timeout: time.Minute},
	}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

//---------------------------------------------------------------------------------------------

// Upsert inserts or overwrites vectors by ID.
func (p *Pinecone) Upsert(ctx context.Context, dataset string, vectors []Vector) error {
	for i, v := range vectors {
		if v.ID == "" {
			return fmt.Errorf("vector: vector %d has no ID", i)
		}
		if len(v.Values) == 0 {
			return fmt.Errorf("vector: vector %q has no values", v.ID)
		}
	}
	for start := 0; start < len(vectors); start += maxUpsertBatch {
		end := min(start+maxUpsertBatch, len(vectors))
		req := pcUpsertRequest{Namespace: dataset, Vectors: make([]pcVector, 0, end-start)}
		for _, v := range vectors[start:end] {
			req.Vectors = append(req.Vectors, pcVector{ID: v.ID, Values: v.Values, Metadata: v.Metadata})
		}
		if err := p.do(ctx, "/vectors/upsert", req, nil); err != nil {
			return err
		}
	}
	return nil
}

// Query returns the vectors closest to the given embedding, best first.
func (p *Pinecone) Query(ctx context.Context, dataset string, vector []float32, opts QueryOptions) ([]Match, error) {
	if len(vector) == 0 {
		return nil, errors.New("vector: empty query vector")
	}
	topK := opts.TopK
	if topK <= 0 {
		topK = defaultTopK
	}
	var resp pcQueryResponse
	err := p.do(ctx, "/query", pcQueryRequest{
		Namespace:       dataset,
		Vector:          vector,
		TopK:            topK,
		Filter:          opts.Filter,
		IncludeValues:   opts.IncludeValues,
		IncludeMetadata: true,
	}, &resp)
	if err != nil {
		return nil, err
	}
	out := make([]Match, len(resp.Matches))
	for i, m := range resp.Matches {
		out[i] = Match{ID: m.ID, Score: m.Score, Values: m.Values, Metadata: m.Metadata}
	}
	return out, nil
}

// Delete removes vectors by ID.
func (p *Pinecone) Delete(ctx context.Context, dataset string, ids []string) error {
	for start := 0; start < len(ids); start += maxDeleteBatch {
		end := min(start+maxDeleteBatch, len(ids))
		if err := p.do(ctx, "/vectors/delete", pcDeleteRequest{Namespace: dataset, IDs: ids[start:end]}, nil); err != nil {
			return err
		}
	}
	return nil
}

// DeleteAll removes every vector in the dataset's namespace. A dataset that does not exist is not an error.
func (p *Pinecone) DeleteAll(ctx context.Context, dataset string) error {
	err := p.do(ctx, "/vectors/delete", pcDeleteRequest{Namespace: dataset, DeleteAll: true}, nil)
	var he *pineconeError
	if errors.As(err, &he) && he.Status == http.StatusNotFound {
		return nil
	}
	return err
}

//---------------------------------------------------------------------------------------------

type pineconeError struct {
	Status  int
	Message string
}

func (e *pineconeError) Error() string {
	return fmt.Sprintf("vector: pinecone %d: %s", e.Status, e.Message)
}

// do POSTs in as JSON to path and decodes the response into out (if non-nil).
func (p *Pinecone) do(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("vector: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("vector: build request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("api-key", p.apiKey)
	req.Header.Set("x-pinecone-api-version", pineconeAPIVersion)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("vector: send request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("vector: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(data))
		var pe struct {
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &pe) == nil {
			if pe.Error.Message != "" {
				msg = pe.Error.Message
			} else if pe.Message != "" {
				msg = pe.Message
			}
		}
		return &pineconeError{Status: resp.StatusCode, Message: msg}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("vector: decode response: %w", err)
		}
	}
	return nil
}

//---------------------------------------------------------------------------------------------
// Wire types.

type pcVector struct {
	ID       string         `json:"id"`
	Values   []float32      `json:"values"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type pcUpsertRequest struct {
	Namespace string     `json:"namespace,omitempty"`
	Vectors   []pcVector `json:"vectors"`
}

type pcQueryRequest struct {
	Namespace       string         `json:"namespace,omitempty"`
	Vector          []float32      `json:"vector"`
	TopK            int            `json:"topK"`
	Filter          map[string]any `json:"filter,omitempty"`
	IncludeValues   bool           `json:"includeValues"`
	IncludeMetadata bool           `json:"includeMetadata"`
}

type pcQueryResponse struct {
	Matches []struct {
		ID       string         `json:"id"`
		Score    float32        `json:"score"`
		Values   []float32      `json:"values"`
		Metadata map[string]any `json:"metadata"`
	} `json:"matches"`
}

type pcDeleteRequest struct {
	Namespace string   `json:"namespace,omitempty"`
	IDs       []string `json:"ids,omitempty"`
	DeleteAll bool     `json:"deleteAll,omitempty"`
}
