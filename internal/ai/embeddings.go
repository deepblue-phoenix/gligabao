package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

//---------------------------------------------------------------------------------------------

const (
	defaultOpenAIBaseURL = "https://api.openai.com"
	// EmbeddingModel is the OpenAI embedding model used by default.
	EmbeddingModel = "text-embedding-3-small"
	// EmbeddingDimensions is the default output size of text-embedding-3-small.
	EmbeddingDimensions = 1536
	// maxEmbedBatch is the max number of inputs per OpenAI embeddings request.
	maxEmbedBatch = 2048
)

//---------------------------------------------------------------------------------------------

// Embedder calculates text embeddings using the OpenAI embeddings API.
type Embedder struct {
	apiKey     string
	baseURL    string
	model      string
	dimensions int // 0 = model default
	httpClient *http.Client
}

// EmbedderOption configures an Embedder.
type EmbedderOption func(*Embedder)

// WithEmbedBaseURL overrides the OpenAI API base URL (useful for tests/proxies).
func WithEmbedBaseURL(u string) EmbedderOption {
	return func(e *Embedder) { e.baseURL = strings.TrimRight(u, "/") }
}

// WithEmbedHTTPClient sets a custom HTTP client.
func WithEmbedHTTPClient(h *http.Client) EmbedderOption {
	return func(e *Embedder) { e.httpClient = h }
}

// WithEmbedDimensions requests shortened embeddings (supported by the 3-series models).
func WithEmbedDimensions(n int) EmbedderOption { return func(e *Embedder) { e.dimensions = n } }

// NewEmbedder creates an Embedder using text-embedding-3-small.
// If apiKey is empty, OPENAI_API_KEY is used.
func NewEmbedder(apiKey string, opts ...EmbedderOption) (*Embedder, error) {
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	if apiKey == "" {
		return nil, errors.New("ai: missing API key (set OPENAI_API_KEY)")
	}
	e := &Embedder{
		apiKey:     apiKey,
		baseURL:    defaultOpenAIBaseURL,
		model:      EmbeddingModel,
		httpClient: &http.Client{Timeout: 2 * time.Minute},
	}
	for _, o := range opts {
		o(e)
	}
	return e, nil
}

//---------------------------------------------------------------------------------------------

// Embed returns the embedding of a single text.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vs, err := e.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vs[0], nil
}

// EmbedBatch returns one embedding per input, in the same order.
func (e *Embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, errors.New("ai: no input texts")
	}
	for i, t := range texts {
		if strings.TrimSpace(t) == "" {
			return nil, fmt.Errorf("ai: input %d is empty", i)
		}
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += maxEmbedBatch {
		end := min(start+maxEmbedBatch, len(texts))
		vs, err := e.embedRequest(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vs...)
	}
	return out, nil
}

func (e *Embedder) embedRequest(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(embedRequest{
		Model:          e.model,
		Input:          texts,
		Dimensions:     e.dimensions,
		EncodingFormat: "float",
	})
	if err != nil {
		return nil, fmt.Errorf("ai: marshal embeddings request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ai: build embeddings request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+e.apiKey)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ai: send embeddings request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ai: read embeddings response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var ae openAIError
		if json.Unmarshal(data, &ae) == nil && ae.Error.Message != "" {
			return nil, fmt.Errorf("ai: openai %d %s: %s", resp.StatusCode, ae.Error.Type, ae.Error.Message)
		}
		return nil, fmt.Errorf("ai: openai status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var er embedResponse
	if err := json.Unmarshal(data, &er); err != nil {
		return nil, fmt.Errorf("ai: decode embeddings response: %w", err)
	}
	if len(er.Data) != len(texts) {
		return nil, fmt.Errorf("ai: expected %d embeddings, got %d", len(texts), len(er.Data))
	}
	sort.Slice(er.Data, func(i, j int) bool { return er.Data[i].Index < er.Data[j].Index })
	out := make([][]float32, len(er.Data))
	for i, d := range er.Data {
		out[i] = d.Embedding
	}
	return out, nil
}

//---------------------------------------------------------------------------------------------

// CosineSimilarity returns the cosine similarity of two equal-length vectors.
// It returns 0 if lengths differ or either vector has zero magnitude.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

//---------------------------------------------------------------------------------------------
// Wire types.

type embedRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	Dimensions     int      `json:"dimensions,omitempty"`
	EncodingFormat string   `json:"encoding_format,omitempty"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

type openAIError struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}
