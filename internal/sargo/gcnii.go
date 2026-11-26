package sargo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// GCNIINode is one node of a GCNII* graph document; both fields are optional (the type is what the model sees, the name is only a label).
type GCNIINode struct {
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
}

// GCNIIGraph is the graph document of the GCNII* endpoints: N nodes numbered 0..N-1, undirected Edges between them, and optional Nodes
// (one per node). Class is the label of a graph in a training request (required there, ignored by inference).
type GCNIIGraph struct {
	N     int         `json:"n"`
	Edges [][2]int    `json:"edges"`
	Nodes []GCNIINode `json:"nodes,omitempty"`
	Class string      `json:"class,omitempty"`
}

// GCNIITrainRequest starts the training of the GCNII* model of one user (POST /v1/gcnii/train). The model is the user's: every other
// user keeps their own. Zero numbers take sargo's defaults (30 epochs, lr 0.01, batch 4, hidden 16, depth 16, random seed).
type GCNIITrainRequest struct {
	UserID string       `json:"user_id"`
	Graphs []GCNIIGraph `json:"graphs"`
	// Validation are graphs (each with a class) the model is not trained on; sargo scores the trained model on them (ValAccuracy,
	// ValLoss of the job). Optional.
	Validation []GCNIIGraph `json:"validation,omitempty"`
	Epochs     int          `json:"epochs,omitempty"`
	LR         float64      `json:"lr,omitempty"`
	Batch      int          `json:"batch,omitempty"`
	Hidden     int          `json:"hidden,omitempty"`
	Depth      int          `json:"depth,omitempty"`
	Seed       *uint64      `json:"seed,omitempty"`
}

// GCNII job states (GCNIIJob.Status).
const (
	GCNIIJobRunning = "running"
	GCNIIJobDone    = "done"
	GCNIIJobFailed  = "failed"
	// GCNIIJobCancelled is a job stopped with GCNIICancel; it saved no model.
	GCNIIJobCancelled = "cancelled"
)

// GCNIIJob is a training of one user, as sargo reports it.
type GCNIIJob struct {
	Status string `json:"status"`
	Graphs int    `json:"graphs"`
	// ValidationGraphs is how many graphs the job holds out for validation.
	ValidationGraphs int      `json:"validation_graphs,omitempty"`
	Classes          []string `json:"classes"`
	Epochs           int      `json:"epochs"`
	StartedUnix      int64    `json:"started_unix"`
	// Epoch is the last epoch a running job finished (0 before the first) and EpochLoss the mean training loss of it (nil before the first).
	Epoch     int      `json:"epoch,omitempty"`
	EpochLoss *float64 `json:"epoch_loss,omitempty"`
	// EpochLosses is the mean training loss of every finished epoch, in order (index 0 is epoch 1); it stays after the job ended.
	EpochLosses  []float64 `json:"epoch_losses,omitempty"`
	FinishedUnix int64     `json:"finished_unix,omitempty"`
	// Accuracy and Loss are measured on the training graphs, once the job is done.
	Accuracy *float64 `json:"accuracy,omitempty"`
	Loss     *float64 `json:"loss,omitempty"`
	// ValAccuracy and ValLoss are measured on the validation graphs (only if the request had some), once the job is done.
	ValAccuracy *float64 `json:"val_accuracy,omitempty"`
	ValLoss     *float64 `json:"val_loss,omitempty"`
	// Error is why a failed job failed.
	Error string `json:"error,omitempty"`
}

// GCNIITrainResponse is the answer to a started training (the job is running).
type GCNIITrainResponse struct {
	UserID string   `json:"user_id"`
	Job    GCNIIJob `json:"job"`
}

// GCNIIStatus is the training state of one user: whether they have a trained model, and their last job (nil if sargo started none since
// it was started).
type GCNIIStatus struct {
	UserID  string    `json:"user_id"`
	Trained bool      `json:"trained"`
	Job     *GCNIIJob `json:"job"`
}

// GCNIIInferRequest classifies one graph with the model of UserID. Classes, if given, names the outputs instead of the names of the
// training.
type GCNIIInferRequest struct {
	UserID  string     `json:"user_id"`
	Graph   GCNIIGraph `json:"graph"`
	Classes []string   `json:"classes,omitempty"`
}

// GCNIIInferResponse is the class of a graph.
type GCNIIInferResponse struct {
	Class         string             `json:"class"`
	Index         int                `json:"index"`
	Logits        []float64          `json:"logits"`
	Probabilities map[string]float64 `json:"probabilities"`
	Nodes         int                `json:"nodes"`
	Edges         int                `json:"edges"`
}

// GCNIIEmbedRequest embeds one graph with the model of UserID.
type GCNIIEmbedRequest struct {
	UserID    string     `json:"user_id"`
	Graph     GCNIIGraph `json:"graph"`
	Normalize bool       `json:"normalize,omitempty"`
}

// GCNIIEmbedResponse is the embedding of a graph: Dim floats.
type GCNIIEmbedResponse struct {
	Embedding []float64 `json:"embedding"`
	Dim       int       `json:"dim"`
}

// GCNIILinkNode is the new node of a link prediction: its Name and Type (both optional) and the nodes of the graph it is already known
// to be connected to (Neighbors, indices; without them the node is scored from its type alone).
type GCNIILinkNode struct {
	Name      string `json:"name,omitempty"`
	Type      string `json:"type,omitempty"`
	Neighbors []int  `json:"neighbors,omitempty"`
}

// GCNIILinkRequest scores a new node against every node of Graph with the model of UserID. TopK > 0 keeps only the best TopK.
type GCNIILinkRequest struct {
	UserID string        `json:"user_id"`
	Graph  GCNIIGraph    `json:"graph"`
	Node   GCNIILinkNode `json:"node"`
	TopK   int           `json:"top_k,omitempty"`
}

// GCNIILinkScore is the score (0..1, the cosine of the node embeddings) of the link of the new node with node Node of the graph;
// Connected is true for the links the request already gave.
type GCNIILinkScore struct {
	Node      int     `json:"node"`
	Name      string  `json:"name,omitempty"`
	Score     float64 `json:"score"`
	Connected bool    `json:"connected"`
}

// GCNIILinkResponse is the link prediction: Node is the index the new node got (= Nodes), Scores the nodes of the graph, best first.
type GCNIILinkResponse struct {
	Node   int              `json:"node"`
	Nodes  int              `json:"nodes"`
	Scores []GCNIILinkScore `json:"scores"`
}

//---------------------------------------------------------------------------------------------

// ErrInvalidGCNII wraps every error of the checks the client makes before sending a GCNII* request (nothing was sent).
var ErrInvalidGCNII = errors.New("sargo: invalid gcnii request")

func invalidGCNII(format string,
	args ...any) error {
	return fmt.Errorf("%w: "+format,
		append([]any{ErrInvalidGCNII}, args...)...)
}

// Validate checks the graph: node count and edge endpoints (sargo checks again, this saves the round trip).
func (g GCNIIGraph) Validate() error {
	if g.N < 1 {
		return invalidGCNII("graph: n must be >= 1")
	}
	if len(g.Nodes) != 0 && len(g.Nodes) != g.N {
		return invalidGCNII("graph: %d nodes entries for n = %d",
			len(g.Nodes),
			g.N)
	}
	for i, e := range g.Edges {
		if e[0] < 0 || e[1] < 0 || e[0] >= g.N || e[1] >= g.N {
			return invalidGCNII("graph: edge %d (%d, %d) is outside 0..%d",
				i,
				e[0],
				e[1],
				g.N-1)
		}
	}
	return nil
}

// Validate checks the request: a user, at least one graph, each valid and with a class, and non-negative options.
func (r *GCNIITrainRequest) Validate() error {
	if err := validateGCNIIUser(r.UserID); err != nil {
		return err
	}
	if len(r.Graphs) == 0 {
		return invalidGCNII("train: no graphs")
	}
	if r.Epochs < 0 || r.LR < 0 || r.Batch < 0 || r.Hidden < 0 || r.Depth < 0 {
		return invalidGCNII("train: epochs, lr, batch, hidden and depth must not be negative")
	}
	if err := validateLabelled("graphs", r.Graphs); err != nil {
		return err
	}
	return validateLabelled("validation", r.Validation)
}

// validateLabelled checks that every graph of the list is valid and has a class.
func validateLabelled(field string,
	graphs []GCNIIGraph) error {
	for i, g := range graphs {
		if err := g.Validate(); err != nil {
			return fmt.Errorf("%s[%d]: %w",
				field,
				i,
				err)
		}
		if strings.TrimSpace(g.Class) == "" {
			return invalidGCNII("train: %s[%d] has no class",
				field,
				i)
		}
	}
	return nil
}

func validateGCNIIUser(userID string) error {
	if strings.TrimSpace(userID) == "" {
		return invalidGCNII("user_id is required")
	}
	return nil
}

//---------------------------------------------------------------------------------------------

// GCNIITrain starts the training of the model of req.UserID (POST /v1/gcnii/train). It returns as soon as sargo has accepted the job;
// follow it with GCNIITrainStatus. A user that is already training gets an *APIError with status 409; sargo without per-user models
// ([gcnii] models_dir) 503.
func (c *Client) GCNIITrain(ctx context.Context,
	req *GCNIITrainRequest) (*GCNIITrainResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	var resp GCNIITrainResponse
	if err := c.postJSON(ctx,
		"/v1/gcnii/train",
		req,
		&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GCNIITrainStatus reports the training of one user (POST /v1/gcnii/train/status).
func (c *Client) GCNIITrainStatus(ctx context.Context,
	userID string) (*GCNIIStatus, error) {
	if err := validateGCNIIUser(userID); err != nil {
		return nil, err
	}
	var resp GCNIIStatus
	if err := c.postJSON(ctx,
		"/v1/gcnii/train/status",
		map[string]string{"user_id": userID},
		&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GCNIICancel stops the training of one user (POST /v1/gcnii/train/cancel): the job ends as GCNIIJobCancelled at its next batch, and the
// previous model of the user, if any, is kept. A user that is not training gets an *APIError with status 409.
func (c *Client) GCNIICancel(ctx context.Context,
	userID string) (*GCNIIStatus, error) {
	if err := validateGCNIIUser(userID); err != nil {
		return nil, err
	}
	var resp GCNIIStatus
	if err := c.postJSON(ctx,
		"/v1/gcnii/train/cancel",
		map[string]string{"user_id": userID},
		&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GCNIIInfer classifies a graph with the model of req.UserID (POST /v1/gcnii/infer). A user without a trained model gets an *APIError
// with status 404.
func (c *Client) GCNIIInfer(ctx context.Context,
	req *GCNIIInferRequest) (*GCNIIInferResponse, error) {
	if err := validateGCNIIUser(req.UserID); err != nil {
		return nil, err
	}
	if err := req.Graph.Validate(); err != nil {
		return nil, err
	}
	var resp GCNIIInferResponse
	if err := c.postJSON(ctx,
		"/v1/gcnii/infer",
		req,
		&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GCNIIEmbed embeds a graph with the model of req.UserID (POST /v1/gcnii/embed). A user without a trained model gets an *APIError with
// status 404.
func (c *Client) GCNIIEmbed(ctx context.Context,
	req *GCNIIEmbedRequest) (*GCNIIEmbedResponse, error) {
	if err := validateGCNIIUser(req.UserID); err != nil {
		return nil, err
	}
	if err := req.Graph.Validate(); err != nil {
		return nil, err
	}
	var resp GCNIIEmbedResponse
	if err := c.postJSON(ctx,
		"/v1/gcnii/embed",
		req,
		&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GCNIILink predicts the links of a new node to the nodes of a graph with the model of req.UserID (POST /v1/gcnii/link). A user without a
// trained model gets an *APIError with status 404.
func (c *Client) GCNIILink(ctx context.Context,
	req *GCNIILinkRequest) (*GCNIILinkResponse, error) {
	if err := validateGCNIIUser(req.UserID); err != nil {
		return nil, err
	}
	if err := req.Graph.Validate(); err != nil {
		return nil, err
	}
	if req.TopK < 0 {
		return nil, invalidGCNII("top_k must be >= 0")
	}
	for _, i := range req.Node.Neighbors {
		if i < 0 || i >= req.Graph.N {
			return nil, invalidGCNII("node: neighbor %d outside nodes 0..%d",
				i,
				req.Graph.N)
		}
	}
	var resp GCNIILinkResponse
	if err := c.postJSON(ctx,
		"/v1/gcnii/link",
		req,
		&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// postJSON POSTs in as JSON to path and decodes the reply into out.
func (c *Client) postJSON(ctx context.Context,
	path string,
	in any,
	out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("sargo: marshal request: %w", err)
	}
	data, err := c.do(ctx,
		"POST",
		path,
		body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("sargo: decode reply: %w", err)
	}
	return nil
}
