package memory_image

// The image pipeline (package memory_image): Remember for images (RememberImage). It is independent of the text pipeline (remember.go):
// an image is sent to a vision LLM, which reads the text in it (OCR: title and text blocks) and classifies the whole
// image by a theme ("graph", "plot", ...). The image file is kept in the image directory (ImageDir, see package
// image_store.go), tracked by an image artifact row (gb_image_artifacts: metadata and the file's path, never the bytes),
// and what the model read is stored as an image extraction with its text blocks (gb_image_extractions,
// gb_image_text_blocks). Nothing is
// added to the graph (entities, edges, vectors) yet: grounding the extraction in the graph comes later.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"

	log "github.com/sirupsen/logrus"
)

//---------------------------------------------------------------------------------------------

// ImageDir is where RememberImage stores the image files (see image_store.go); the database only tracks them.
// It is read on every call; set it once at startup, or override it per call with WithImageDir.
var ImageDir = "build/images"

// WithImageDir overrides ImageDir for this call: the directory RememberImage and LoadImage keep the image files in.
func WithImageDir(dir string) memory_core.Option {
	return func(o *memory_core.CallOptions) { o.ImageDir = dir }
}

// imageDir is the image directory of a call.
func imageDir(o memory_core.CallOptions) string {
	if o.ImageDir != "" {
		return o.ImageDir
	}
	return ImageDir
}

// CallRememberImage is the type of the LLM calls of RememberImage, stored in gb_inferences.type.
const CallRememberImage = "remember_image"

// MaxImageBytes is the largest image accepted (the Anthropic API limit for one image).
const MaxImageBytes = 5 << 20

// ImageThemes are the themes an image is classified as; anything else the model answers becomes ThemeOther.
var ImageThemes = []string{"plot", "chart", "graph", "diagram", "table", "document", "screenshot", "slide", "map", "photo", "illustration", ThemeOther}

// ThemeOther is the theme of an image that fits none of the others.
const ThemeOther = "other"

// TextBlockKinds are the roles of the text blocks found in an image; anything else the model answers becomes KindOther.
var TextBlockKinds = []string{"title", "subtitle", "heading", "paragraph", "caption", "label", "axis", "legend", "annotation", "table", KindOther}

// KindOther is the kind of a text block that fits none of the others.
const KindOther = "other"

// ErrInvalidImage means the image is empty, too large, or not a JPEG, PNG, GIF or WebP.
var ErrInvalidImage = errors.New("invalid image")

// ResolveImage checks data and returns its media type, detected from the content: image/jpeg, image/png,
// image/gif or image/webp. It returns ErrInvalidImage for anything else, or for more than MaxImageBytes.
func ResolveImage(data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("%w: empty", ErrInvalidImage)
	}
	if len(data) > MaxImageBytes {
		return "", fmt.Errorf("%w: larger than %d MiB", ErrInvalidImage, MaxImageBytes>>20)
	}
	switch mt := http.DetectContentType(data); mt {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return mt, nil
	}
	return "", fmt.Errorf("%w: must be a JPEG, PNG, GIF or WebP", ErrInvalidImage)
}

//---------------------------------------------------------------------------------------------

// ImageTextBlock is one region of text found in an image, with its role (title, paragraph, label, ...).
type ImageTextBlock = graph.ImageTextBlock

// ImageExtraction is what the LLM read from an image.
type ImageExtraction struct {
	Theme      string           `json:"theme"`       // what the whole image is, one of ImageThemes
	Title      string           `json:"title"`       // the image's own title; "" if it has none
	Text       string           `json:"text"`        // all the text, the blocks joined by newlines
	TextBlocks []ImageTextBlock `json:"text_blocks"` // in reading order

	// The call that produced it.
	Provider     string  `json:"provider,omitempty"`
	Model        string  `json:"model,omitempty"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	Raw          string  `json:"-"` // the model's output as received
}

// ExtractImage sends the image (of a media type from ResolveImage) to the LLM, which reads its text and classifies it.
func ExtractImage(ctx context.Context, llm memory_core.LLM, mediaType string, data []byte) (*ImageExtraction, error) {
	resp, err := llm.Complete(ctx, ai.Request{
		System: imageSystemPrompt,
		Messages: []ai.Message{{
			Role:    ai.RoleUser,
			Content: "Read this image.",
			Images:  []ai.Image{{MediaType: mediaType, Data: data}},
		}},
		MaxTokens: 8192,
	})
	if err != nil {
		return nil, err
	}
	if resp.StopReason == "max_tokens" {
		return nil, errors.New("memory: LLM output truncated; too much text in the image")
	}
	x, err := parseImageExtraction(resp.Text)
	if err != nil {
		return nil, err
	}
	x.Provider, x.Model, x.Raw = resp.Provider, resp.Model, resp.Text
	x.InputTokens, x.OutputTokens, x.CostUSD = resp.InputTokens, resp.OutputTokens, resp.CostUSD
	return x, nil
}

// parseImageExtraction decodes the LLM's JSON, tolerating surrounding prose or code fences, and normalizes it:
// unknown themes and kinds become "other", blank blocks are dropped, and Text is built from the blocks.
func parseImageExtraction(s string) (*ImageExtraction, error) {
	js, err := memory_core.JSONObject(s)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Theme      string `json:"theme"`
		Title      string `json:"title"`
		TextBlocks []struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		} `json:"text_blocks"`
	}
	if err := json.Unmarshal([]byte(js), &raw); err != nil {
		return nil, fmt.Errorf("memory: decode LLM output: %w", err)
	}
	x := &ImageExtraction{Theme: oneOf(raw.Theme, ImageThemes, ThemeOther), Title: strings.TrimSpace(raw.Title), TextBlocks: []ImageTextBlock{}}
	var texts []string
	for _, b := range raw.TextBlocks {
		text := strings.TrimSpace(b.Text)
		if text == "" {
			continue
		}
		kind := oneOf(b.Kind, TextBlockKinds, KindOther)
		if x.Title == "" && kind == "title" {
			x.Title = text
		}
		x.TextBlocks = append(x.TextBlocks, ImageTextBlock{Kind: kind, Text: text})
		texts = append(texts, text)
	}
	x.Text = strings.Join(texts, "\n")
	return x, nil
}

// oneOf returns s trimmed and lowercased if it is one of allowed, else def.
func oneOf(s string, allowed []string, def string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, a := range allowed {
		if s == a {
			return s
		}
	}
	return def
}

//---------------------------------------------------------------------------------------------

// ImageResult summarizes what RememberImage stored.
type ImageResult struct {
	UserID  string `json:"user_id"`
	Dataset string `json:"dataset"`
	// Pipeline is always "image": it tells this result from that of the text pipeline (Result).
	Pipeline        string `json:"pipeline"`
	ImageArtifactID int64  `json:"image_artifact_id"`
	// SessionID is the session the image artifact is attached to (omitted if none). For a Cached result it is the
	// session of the stored artifact, which is the one that first remembered the image, not necessarily this call's.
	SessionID    string `json:"session_id,omitempty"`
	ExtractionID int64  `json:"extraction_id"`
	MediaType    string `json:"media_type"`
	SHA256       string `json:"sha256"`
	SizeBytes    int    `json:"size_bytes"`
	Path         string `json:"path"` // the file, relative to the image directory
	// Cached is true if the dataset already had this image (same SHA-256): nothing was sent to the LLM or stored, and
	// this is the stored result of the earlier call, including its model, tokens and cost, which were spent then.
	Cached    bool      `json:"cached"`
	ValidAt   time.Time `json:"valid_at"`   // event time of the image
	CreatedAt time.Time `json:"created_at"` // ingestion time
	ImageExtraction
	// TrajectoryID and Trajectory: see Result.
	TrajectoryID string           `json:"trajectory_id,omitempty"`
	Trajectory   *atif.Trajectory `json:"trajectory,omitempty"`
}

// PipelineImage is ImageResult.Pipeline.
const PipelineImage = "image"

// RememberImage reads the image with the LLM and stores it as an image artifact, with the extraction, in userID's
// named dataset (empty means the default dataset; it is created if missing); the file goes into the image directory
// (ImageDir, or WithImageDir) and only its path into the database. ref is the time the image was captured or
// observed; the zero value means the current time.
//
// An image the dataset already holds (same SHA-256, an image artifact with an extraction) is not read again: the
// stored result is returned with Cached set, and nothing is called or stored (ref is ignored; the file is restored
// if it went missing). Only the dataset is searched: datasets are isolated. If the LLM step fails nothing is stored (but the call's cost is
// still recorded, as for Remember). With WithSessionID the new image artifact is attached to that session (a Cached
// result keeps the session of the stored artifact). Only with WithEval is the call recorded as an ATIF trajectory.
func RememberImage(ctx context.Context, d *sql.DB, llm memory_core.LLM, userID, dataset string, data []byte, ref time.Time, opts ...memory_core.Option) (res *ImageResult, err error) {
	userID, err = memory_core.ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	dataset, err = memory_core.ResolveDatasetName(dataset)
	if err != nil {
		return nil, err
	}
	mediaType, err := ResolveImage(data)
	if err != nil {
		return nil, err
	}
	co := memory_core.NewCallOptions(opts)
	sessionID, err := memory_core.ResolveSessionID(co.SessionID)
	if err != nil {
		return nil, err
	}
	now := relational.Now()
	if ref.IsZero() {
		ref = now
	}
	ref = relational.Normalize(ref)
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	llm = memory_core.WithRecording(llm, d, CallRememberImage)

	tr := memory_core.StartTrace(CallRememberImage, co)
	defer func() {
		if traj := tr.Finish(err); res != nil && traj != nil {
			res.TrajectoryID, res.Trajectory = traj.TrajectoryID, traj
		}
	}()
	llm = tr.Wrap(llm)
	input := map[string]any{"user_id": userID, "dataset": dataset, "media_type": mediaType, "sha256": hash,
		"size_bytes": len(data), "reference_time": relational.FormatTime(ref), "session_id": sessionID}
	if len(co.Models) > 0 {
		input["models"] = co.Models
	}
	tr.Input(input)

	// Seen before in this dataset? Then the stored result is the answer (a dataset that does not exist has none).
	if dsID, err := relational.DatasetID(ctx, d, userID, dataset); err == nil {
		art, ext, err := graph.FindImageBySHA256(ctx, d, dsID, hash)
		switch {
		case err == nil:
			// Same content, so the same file name: this only writes the file if it is gone.
			if _, _, err := saveFile(imageDir(co), dsID, art.MediaType, hash, data); err != nil {
				return nil, err
			}
			res := newImageResult(userID, dataset, art, ext)
			tr.Tool("The dataset already holds this image (same SHA-256): returned its stored extraction, without calling the LLM.",
				"find_existing_image", map[string]any{"sha256": hash},
				map[string]any{"image_artifact_id": art.ID, "extraction_id": ext.ID, "theme": ext.Theme, "title": ext.Title, "text_blocks": ext.TextBlocks})
			return res, nil
		case !errors.Is(err, graph.ErrNotFound):
			return nil, err
		}
	} else if !errors.Is(err, relational.ErrNotFound) {
		return nil, err
	}

	x, err := ExtractImage(ctx, co.ModelLLM(llm, memory_core.StageExtractImage), mediaType, data)
	if err != nil {
		return nil, err
	}
	tr.Tool("The LLM read the image: its theme, title and text blocks.", "extract_image",
		map[string]any{"media_type": mediaType, "size_bytes": len(data)},
		map[string]any{"theme": x.Theme, "title": x.Title, "text_blocks": x.TextBlocks})

	dsID, err := relational.EnsureDataset(ctx, d, userID, dataset)
	if err != nil {
		return nil, err
	}
	// The file first, then the row that tracks it: a failed insert removes the file again (unless it was there already,
	// from the same image in this dataset), so only a crash in between can leave an untracked file.
	path, created, err := saveFile(imageDir(co), dsID, mediaType, hash, data)
	if err != nil {
		return nil, err
	}
	artID, extID, err := graph.InsertImage(ctx, d, graph.NewImage{
		DatasetID: dsID,
		SessionID: sessionID,
		MediaType: mediaType,
		SHA256:    hash,
		SizeBytes: int64(len(data)),
		Path:      path,
		ValidAt:   ref,
		CreatedAt: now,
		Extraction: graph.NewImageExtraction{
			Provider: x.Provider, Model: x.Model,
			Theme: x.Theme, Title: x.Title, Text: x.Text, TextBlocks: x.TextBlocks, Raw: x.Raw,
			InputTokens: x.InputTokens, OutputTokens: x.OutputTokens, CostUSD: x.CostUSD,
		},
	})
	if err != nil {
		if created {
			if removeErr := removeFile(imageDir(co), path); removeErr != nil {
				log.WithError(removeErr).WithField("path", path).Error("remove image file after failed insert")
			}
		}
		return nil, err
	}
	tr.Tool("Stored the image file and tracked it with an image artifact and its extraction.", "image_store",
		map[string]any{"user_id": userID, "dataset": dataset},
		map[string]any{"image_artifact_id": artID, "extraction_id": extID, "path": path, "text_blocks": len(x.TextBlocks)})
	return &ImageResult{
		UserID:          userID,
		Dataset:         dataset,
		Pipeline:        PipelineImage,
		ImageArtifactID: artID,
		SessionID:       sessionID,
		ExtractionID:    extID,
		MediaType:       mediaType,
		SHA256:          hash,
		SizeBytes:       len(data),
		Path:            path,
		ValidAt:         ref.UTC(),
		CreatedAt:       now.UTC(),
		ImageExtraction: *x,
	}, nil
}

// newImageResult builds the result for a stored image and extraction.
func newImageResult(userID, dataset string, a *graph.ImageArtifact, x *graph.ImageExtraction) *ImageResult {
	blocks := x.TextBlocks
	if blocks == nil {
		blocks = []ImageTextBlock{}
	}
	return &ImageResult{
		UserID:          userID,
		Dataset:         dataset,
		Pipeline:        PipelineImage,
		ImageArtifactID: a.ID,
		SessionID:       a.SessionID,
		ExtractionID:    x.ID,
		MediaType:       a.MediaType,
		SHA256:          a.SHA256,
		SizeBytes:       int(a.SizeBytes),
		Path:            a.Path,
		Cached:          true,
		ValidAt:         a.ValidAt,
		CreatedAt:       a.CreatedAt,
		ImageExtraction: ImageExtraction{
			Theme: x.Theme, Title: x.Title, Text: x.Text, TextBlocks: blocks,
			Provider: x.Provider, Model: x.Model,
			InputTokens: x.InputTokens, OutputTokens: x.OutputTokens, CostUSD: x.CostUSD, Raw: x.Raw,
		},
	}
}

// LoadImage returns the bytes of an image artifact of userID's named dataset (empty means the default dataset), read from
// the image directory (ImageDir, or WithImageDir). It returns an error wrapping ErrNotFound (graph or relational) if there is no such dataset or artifact.
func LoadImage(ctx context.Context, d *sql.DB, userID, dataset string, artifactID int64, opts ...memory_core.Option) ([]byte, error) {
	userID, err := memory_core.ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	dataset, err = memory_core.ResolveDatasetName(dataset)
	if err != nil {
		return nil, err
	}
	dsID, err := relational.DatasetID(ctx, d, userID, dataset)
	if err != nil {
		return nil, err
	}
	a, err := graph.GetImageArtifact(ctx, d, dsID, artifactID)
	if err != nil {
		return nil, err
	}
	return readFile(imageDir(memory_core.NewCallOptions(opts)), a.Path)
}
