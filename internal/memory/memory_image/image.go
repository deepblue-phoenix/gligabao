package memory_image

// The image pipeline (package memory_image): Remember for images (RememberImage). It is independent of the text pipeline (remember.go):
// an image is sent to a vision LLM, which reads the text in it (OCR: title and text blocks) and classifies the whole
// image by a theme ("graph", "plot", ...). The image file is kept in the image store (see package imagestore: a directory, ImageDir, or an S3 bucket; see
// config image_store), tracked by an image artifact row (gb_image_artifacts: metadata and the file's path, never the bytes),
// and what the model read is stored as an image extraction with its text blocks (gb_image_extractions,
// gb_image_text_blocks). The model also describes what the image shows and how its elements relate (the links of a graph
// or diagram, what a plot says); that description is then remembered as a text artifact by the text pipeline
// (memory_remember.Remember), which extracts and stores the graph of the image (entities, edges, vectors) like for any
// text. gb_image_graphs links the extraction to that artifact.

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

	"github.com/deepblue-phoenix/gligabao/internal/atif"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/imagestore"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"

	log "github.com/sirupsen/logrus"
)

// ImageExtraction is what the LLM read from an image.
type ImageExtraction struct {
	Theme      string           `json:"theme"`       // what the whole image is, one of ImageThemes
	Title      string           `json:"title"`       // the image's own title; "" if it has none
	Text       string           `json:"text"`        // all the text, the blocks joined by newlines
	TextBlocks []ImageTextBlock `json:"text_blocks"` // in reading order
	// Description is a prose account of what the image shows and how its elements relate; "" if there is nothing to
	// relate (e.g. a photo, or an image with only text). It is what the graph of the image is extracted from.
	Description string `json:"description"`

	// The call that produced it.
	Provider     string  `json:"provider,omitempty"`
	Model        string  `json:"model,omitempty"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	Raw          string  `json:"-"` // the model's output as received
}

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
	// ArtifactID is the text artifact (gb_artifacts) the description was remembered as, which owns the graph of the
	// image (Entities and Edges, and the artifact graph endpoints); 0 (omitted) if the image has no description.
	ArtifactID int64 `json:"artifact_id,omitempty"`
	// Entities and Edges are the graph just extracted from the image; omitted for a Cached result (read the graph of
	// ArtifactID instead).
	Entities []memory_remember.Entity `json:"entities,omitempty"`
	Edges    []memory_remember.Edge   `json:"edges,omitempty"`
	// TrajectoryID and Trajectory: see Result.
	TrajectoryID string           `json:"trajectory_id,omitempty"`
	Trajectory   *atif.Trajectory `json:"trajectory,omitempty"`
}

//---------------------------------------------------------------------------------------------

// ImageDir is where RememberImage stores the image files (see image_store.go); the database only tracks them.
// It is read on every call; set it once at startup, or override it per call with WithImageDir.
var ImageDir = "build/images"

// WithImageDir overrides ImageDir for this call: the directory RememberImage and LoadImage keep the image files in.
func WithImageDir(dir string) memory_core.Option {
	return func(o *memory_core.CallOptions) { o.ImageDir = dir }
}

// WithImageStore sets where this call keeps the image files (the file system or S3, see package imagestore); it takes
// precedence over ImageDir and WithImageDir. A nil store is ignored.
func WithImageStore(s imagestore.Store) memory_core.Option {
	return func(o *memory_core.CallOptions) { o.ImageStore = s }
}

// storeOf is the image store of a call: the one of WithImageStore, else the directory of WithImageDir / ImageDir.
func storeOf(o memory_core.CallOptions) imagestore.Store {
	if o.ImageStore != nil {
		return o.ImageStore
	}
	if o.ImageDir != "" {
		return imagestore.NewFS(o.ImageDir)
	}
	return imagestore.NewFS(ImageDir)
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
		return "", fmt.Errorf("%w: larger than %d MiB",
			ErrInvalidImage,
			MaxImageBytes>>20)
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

// ExtractImage sends the image (of a media type from ResolveImage) to the LLM, which reads its text and classifies it.
func ExtractImage(ctx context.Context,
	lm memory_core.LLM,
	mediaType string,
	data []byte) (*ImageExtraction, error) {
	resp, err := lm.Complete(ctx, llm.Request{
		System: imageSystemPrompt,
		Messages: []llm.Message{{
			Role:    llm.RoleUser,
			Content: "Read this image.",
			Images: []llm.Image{{
				MediaType: mediaType,
				Data:      data,
			}},
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
		Theme       string `json:"theme"`
		Title       string `json:"title"`
		Description string `json:"description"`
		TextBlocks  []struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		} `json:"text_blocks"`
	}
	if err := json.Unmarshal([]byte(js), &raw); err != nil {
		return nil, fmt.Errorf("memory: decode LLM output: %w", err)
	}
	x := &ImageExtraction{
		Theme: oneOf(raw.Theme,
			ImageThemes,
			ThemeOther),
		Title:       strings.TrimSpace(raw.Title),
		Description: strings.TrimSpace(raw.Description),
		TextBlocks:  []ImageTextBlock{},
	}
	var texts []string
	for _, b := range raw.TextBlocks {
		text := strings.TrimSpace(b.Text)
		if text == "" {
			continue
		}
		kind := oneOf(b.Kind,
			TextBlockKinds,
			KindOther)
		if x.Title == "" && kind == "title" {
			x.Title = text
		}
		x.TextBlocks = append(x.TextBlocks, ImageTextBlock{
			Kind: kind,
			Text: text,
		})
		texts = append(texts, text)
	}
	x.Text = strings.Join(texts, "\n")
	return x, nil
}

// oneOf returns s trimmed and lowercased if it is one of allowed, else def.
func oneOf(s string,
	allowed []string,
	def string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, a := range allowed {
		if s == a {
			return s
		}
	}
	return def
}

//---------------------------------------------------------------------------------------------

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
func RememberImage(ctx context.Context,
	d *sql.DB,
	lm memory_core.LLM,
	userID,
	dataset string,
	data []byte,
	ref time.Time,
	opts ...memory_core.Option) (res *ImageResult, err error) {
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
	baseLM := lm // Remember records and stages its own calls: the graph step gets the LLM unwrapped
	lm = memory_core.WithRecording(lm,
		d,
		CallRememberImage)

	tr := memory_core.StartTrace(CallRememberImage,
		userID,
		co)
	defer func() {
		if traj := tr.Finish(err); res != nil && traj != nil {
			res.TrajectoryID, res.Trajectory = traj.TrajectoryID, traj
		}
	}()
	lm = tr.Wrap(lm)
	input := map[string]any{"user_id": userID, "dataset": dataset, "media_type": mediaType, "sha256": hash,
		"size_bytes": len(data), "reference_time": relational.FormatTime(ref), "session_id": sessionID}
	if len(co.Models) > 0 {
		input["models"] = co.Models
	}
	tr.Input(input)
	co.Report("received_image", "info", fmt.Sprintf("received a %s image of %d bytes", mediaType, len(data)),
		map[string]any{"media_type": mediaType, "size_bytes": len(data)})

	// Seen before in this dataset? Then the stored result is the answer (a dataset that does not exist has none).
	if dsID, err := memory_core.DatasetID(ctx,
		d,
		userID,
		dataset); err == nil {
		art, ext, err := graph.FindImageBySHA256(ctx,
			d,
			dsID,
			hash)
		switch {
		case err == nil:
			co.Report("find_existing_image", "succeeded", "this image was extracted before: using the stored result", nil)
			// Same content, so the same file name: this only writes the file if it is gone.
			if _, _, err := imagestore.Save(ctx,
				storeOf(co),
				userID,
				dsID,
				art.MediaType,
				hash,
				data); err != nil {
				return nil, err
			}
			res := newImageResult(userID,
				dataset,
				art,
				ext)
			if err := rememberGraphOfCached(ctx,
				d,
				baseLM,
				res,
				data,
				ext.ID,
				ref,
				opts...); err != nil {
				return nil, err
			}
			tr.Tool("The dataset already holds this image (same SHA-256): returned its stored extraction, without calling the LLM.",
				"find_existing_image",
				map[string]any{"sha256": hash},
				map[string]any{"image_artifact_id": art.ID, "extraction_id": ext.ID, "theme": ext.Theme, "title": ext.Title, "text_blocks": ext.TextBlocks})
			return res, nil
		case !errors.Is(err, graph.ErrNotFound):
			return nil, err
		}
	} else if !errors.Is(err, memory_core.ErrDatasetNotFound) {
		return nil, err
	}

	co.Report("extract_image", "started", "the LLM is reading the image", nil)
	x, err := ExtractImage(ctx,
		co.ModelLLM(lm, memory_core.StageExtractImage),
		mediaType,
		data)
	if err != nil {
		co.Report("extract_image", "failed", err.Error(), nil)
		return nil, err
	}
	co.Report("extract_image", "succeeded", "the LLM read the image",
		map[string]any{"theme": x.Theme, "title": x.Title, "text_blocks": len(x.TextBlocks), "model": x.Model,
			"input_tokens": x.InputTokens, "output_tokens": x.OutputTokens})
	co.Report("image_store", "started", "storing the image", nil)
	tr.Tool("The LLM read the image: its theme, title and text blocks.",
		"extract_image",
		map[string]any{"media_type": mediaType, "size_bytes": len(data)},
		map[string]any{"theme": x.Theme, "title": x.Title, "text_blocks": x.TextBlocks})

	dsID, err := memory_core.EnsureDataset(ctx,
		d,
		userID,
		dataset)
	if err != nil {
		return nil, err
	}
	// The file first, then the row that tracks it: a failed insert removes the file again (unless it was there already,
	// from the same image in this dataset), so only a crash in between can leave an untracked file.
	path, created, err := imagestore.Save(ctx,
		storeOf(co),
		userID,
		dsID,
		mediaType,
		hash,
		data)
	if err != nil {
		return nil, err
	}
	artID, extID, err := graph.InsertImage(ctx,
		d,
		graph.NewImage{
			DatasetID: dsID,
			SessionID: sessionID,
			MediaType: mediaType,
			SHA256:    hash,
			SizeBytes: int64(len(data)),
			Path:      path,
			ValidAt:   ref,
			CreatedAt: now,
			Extraction: graph.NewImageExtraction{
				Provider:     x.Provider,
				Model:        x.Model,
				Theme:        x.Theme,
				Title:        x.Title,
				Text:         x.Text,
				TextBlocks:   x.TextBlocks,
				Description:  x.Description,
				Raw:          x.Raw,
				InputTokens:  x.InputTokens,
				OutputTokens: x.OutputTokens,
				CostUSD:      x.CostUSD,
			},
		})
	if err != nil {
		if created {
			if removeErr := storeOf(co).Remove(ctx,
				userID,
				path); removeErr != nil {
				log.WithError(removeErr).WithField("path", path).Error("remove image file after failed insert")
			}
		}
		return nil, err
	}
	co.Report("image_store", "succeeded", "stored the image and its extraction", nil)
	tr.Tool("Stored the image file and tracked it with an image artifact and its extraction.",
		"image_store",
		map[string]any{"user_id": userID, "dataset": dataset},
		map[string]any{"image_artifact_id": artID, "extraction_id": extID, "path": path, "text_blocks": len(x.TextBlocks)})
	res = &ImageResult{
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
	}
	if x.Description != "" {
		co.Report("image_graph", "started", "extracting the graph of the image description", nil)
		// The image is stored: if this fails, a repeat of the call finds it and tries the graph again (Cached).
		if err := rememberGraph(ctx,
			d,
			baseLM,
			res,
			data,
			extID,
			ref,
			opts...); err != nil {
			co.Report("image_graph", "failed", err.Error(), nil)
			return nil, err
		}
		co.Report("image_graph", "succeeded", "extracted the graph of the image",
			map[string]any{"entities": len(res.Entities), "edges": len(res.Edges)})
		tr.Tool("Remembered the description of the image as a text artifact, which extracted its graph.",
			"image_graph",
			map[string]any{"extraction_id": extID},
			map[string]any{"artifact_id": res.ArtifactID, "entities": len(res.Entities), "edges": len(res.Edges)})
	}
	return res, nil
}

// graphText is the text that the graph of an image is extracted from: a header with the theme and title, then the
// model's description of the image.
func graphText(x ImageExtraction) string {
	head := "Image (" + x.Theme + ")"
	if x.Title != "" {
		head += ": " + x.Title
	}
	return head + "\n\n" + x.Description
}

// rememberGraph remembers the description of the extraction extID as a text artifact with the text pipeline (the
// graph of the image: entities and edges, vectors, sessions as for any text), links it to the extraction
// (gb_image_graphs) and sets ArtifactID, Entities and Edges of res. The graph extractor hook (if any) gets it as ContentImage, with data. lm is the unwrapped LLM: Remember records and
// stages its own calls.
func rememberGraph(ctx context.Context,
	d *sql.DB,
	lm memory_core.LLM,
	res *ImageResult,
	data []byte,
	extID int64,
	ref time.Time,
	opts ...memory_core.Option) error {
	r, err := memory_remember.Remember(ctx,
		d,
		lm,
		res.UserID,
		res.Dataset,
		graph.ArtifactKnowledge,
		graphText(res.ImageExtraction),
		ref,
		append(opts[:len(opts):len(opts)],
			memory_core.WithContentType(memory_core.ContentImage),
			memory_core.WithGraphExtractImage(&memory_core.GraphExtractImage{
				Data:      data,
				MediaType: res.MediaType,
				SHA256:    res.SHA256,
			}))...)
	if err != nil {
		return fmt.Errorf("remember the graph of the image: %w", err)
	}
	if err := graph.SetImageGraphArtifact(ctx,
		d,
		extID,
		r.ArtifactID); err != nil {
		return err
	}
	res.ArtifactID, res.Entities, res.Edges = r.ArtifactID, r.Entities, r.Edges
	return nil
}

// rememberGraphOfCached completes a Cached result: it sets ArtifactID from the stored graph record, and if the image has
// a description but its graph was never stored (the earlier call failed after storing the image), it extracts the graph now.
func rememberGraphOfCached(ctx context.Context,
	d *sql.DB,
	lm memory_core.LLM,
	res *ImageResult,
	data []byte,
	extID int64,
	ref time.Time,
	opts ...memory_core.Option) error {
	g, err := graph.GetImageGraph(ctx,
		d,
		extID)
	if errors.Is(err, graph.ErrNotFound) {
		return nil // no description: nothing to build a graph from
	}
	if err != nil {
		return err
	}
	res.Description = g.Description
	if g.ArtifactID != 0 {
		res.ArtifactID = g.ArtifactID
		return nil
	}
	return rememberGraph(ctx,
		d,
		lm,
		res,
		data,
		extID,
		ref,
		opts...)
}

// newImageResult builds the result for a stored image and extraction.
func newImageResult(userID,
	dataset string,
	a *graph.ImageArtifact,
	x *graph.ImageExtraction) *ImageResult {
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
			Theme:        x.Theme,
			Title:        x.Title,
			Text:         x.Text,
			TextBlocks:   blocks,
			Provider:     x.Provider,
			Model:        x.Model,
			InputTokens:  x.InputTokens,
			OutputTokens: x.OutputTokens,
			CostUSD:      x.CostUSD,
			Raw:          x.Raw,
		},
	}
}

// LoadImage returns the bytes of an image artifact of userID's named dataset (empty means the default dataset), read from
// the image directory (ImageDir, or WithImageDir). It returns an error wrapping ErrNotFound (graph or relational) if there is no such dataset or artifact.
func LoadImage(ctx context.Context,
	d *sql.DB,
	userID,
	dataset string,
	artifactID int64,
	opts ...memory_core.Option) ([]byte, error) {
	userID, err := memory_core.ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	dataset, err = memory_core.ResolveDatasetName(dataset)
	if err != nil {
		return nil, err
	}
	dsID, err := memory_core.DatasetID(ctx,
		d,
		userID,
		dataset)
	if err != nil {
		return nil, err
	}
	a, err := graph.GetImageArtifact(ctx,
		d,
		dsID,
		artifactID)
	if err != nil {
		return nil, err
	}
	return storeOf(memory_core.NewCallOptions(opts)).Read(ctx,
		userID,
		a.Path)
}
