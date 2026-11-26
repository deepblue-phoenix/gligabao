package memory_core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
)

// The graph extractor hook: a callback of the program that embeds gligabao (gligabao.ServeOptions.GraphExtractor, see docs/hooks.md) that
// gets the text of a remember call, and the kind of content it is, before the extract_graph stage runs. It can build the graph itself (for
// code: AST parsing, cleanup, ...) or pass, in which case the LLM extracts the graph as usual. Nothing else of the pipeline changes: what
// the hook returns goes through the same stages (reconciliation with stored facts, store, vectors, ...) as an LLM extraction.

// Content types: what kind of content the text of a remember call is (ContentType of GraphExtractRequest, content_type of the request).
const (
	// ContentText is plain text; the default.
	ContentText = "text"
	// ContentCode is source code.
	ContentCode = "code"
	// ContentImage is an image: the call is a remember of an image (see memory_image.RememberImage). The text the hook gets is what the LLM read
	// from the image (its theme and title, then its description), and GraphExtractRequest.Image is the image itself.
	ContentImage = "image"
)

// ErrInvalidContentType means the content type of a remember call is not one of the known ones.
var ErrInvalidContentType = errors.New("invalid content_type")

// ResolveContentType trims and lowercases the content type of a remember of a text and applies ContentText to an empty one. It returns
// ErrInvalidContentType for anything other than ContentText or ContentCode: ContentImage is not a type of a text, it is what a remember
// of an image is (see ResolveImageContentType).
func ResolveContentType(t string) (string, error) {
	switch c := strings.ToLower(strings.TrimSpace(t)); c {
	case "":
		return ContentText, nil
	case ContentText, ContentCode:
		return c, nil
	}
	return "", fmt.Errorf("%w: content_type must be %q or %q",
		ErrInvalidContentType,
		ContentText,
		ContentCode)
}

// ResolveImageContentType validates the content type of a remember of an image: empty or ContentImage (an image is always ContentImage; the
// field only exists so that a client can say it). It returns ErrInvalidContentType for anything else.
func ResolveImageContentType(t string) (string, error) {
	switch c := strings.ToLower(strings.TrimSpace(t)); c {
	case "", ContentImage:
		return ContentImage, nil
	}
	return "", fmt.Errorf("%w: content_type of an image must be %q or empty",
		ErrInvalidContentType,
		ContentImage)
}

// GraphExtractImage is the image of a GraphExtractRequest of ContentImage.
type GraphExtractImage struct {
	// Data is the image file as received (read-only: do not modify it).
	Data []byte
	// MediaType is image/jpeg, image/png, image/gif or image/webp.
	MediaType string
	// SHA256 is the hex SHA-256 of Data (what identifies the image in its dataset).
	SHA256 string
}

// GraphExtractRequest is what a GraphExtractor is asked to extract the graph of.
type GraphExtractRequest struct {
	// Text is what to extract from: the text of the call, or one chunk of it when the text is a document that was split.
	Text string
	// ContentType is the kind of content of the call: ContentText (the default), ContentCode or ContentImage.
	ContentType string
	// Image is the image when ContentType is ContentImage (Text is then what the LLM read from it); nil otherwise.
	Image *GraphExtractImage
	// UserID and Dataset (already resolved) are where the call runs.
	UserID, Dataset string
	// Chunk and Chunks say which chunk of how many Text is (Chunks is 1 for a text that was not split).
	Chunk, Chunks int
}

// GraphExtractResult is what a GraphExtractor answers.
type GraphExtractResult struct {
	// Pass says the hook does not handle this request: the LLM extracts the graph as it would without the hook, and Entities and Edges
	// are ignored. When false the hook has handled it: the LLM extraction does not run, and Entities and Edges are the graph (empty is a
	// valid graph).
	Pass bool
	// Entities and Edges are the extracted graph (when not Pass). An edge names its source and target by entity name; an edge
	// without a Source, Target or Relation is dropped; without a Description one is made from them; a ValidAt that is zero is the reference
	// time of the call.
	Entities []graph.Entity
	Edges    []graph.Edge
}

// GraphExtractor extracts the graph of a text, or passes (GraphExtractResult.Pass). An error fails the call (the hook was in charge). It must
// honour ctx and may be called concurrently.
type GraphExtractor func(ctx context.Context,
	req GraphExtractRequest) (GraphExtractResult, error)

// WithGraphExtractor sets the graph extractor hook of Remember (called by the extract_graph stage, in front of the LLM). nil (the default)
// leaves it off: the LLM always extracts.
func WithGraphExtractor(f GraphExtractor) Option {
	return func(o *CallOptions) { o.GraphExtractor = f }
}

// WithContentType sets the content type of the call (ContentText or ContentCode), which the graph extractor hook gets. It is used as given:
// validate it with ResolveContentType first. Empty is ContentText. It has no effect without WithGraphExtractor.
func WithContentType(t string) Option { return func(o *CallOptions) { o.ContentType = t } }

// WithGraphExtractImage sets the image of the call, which the graph extractor hook gets as GraphExtractRequest.Image. memory_image sets it, together
// with WithContentType(ContentImage), on the remember of the description of an image.
func WithGraphExtractImage(img *GraphExtractImage) Option {
	return func(o *CallOptions) { o.GraphExtractImage = img }
}

// CallGraphExtractor runs f, turning a panic into an error.
func CallGraphExtractor(ctx context.Context,
	f GraphExtractor,
	req GraphExtractRequest) (res GraphExtractResult, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			res, err = GraphExtractResult{}, fmt.Errorf("panic: %v", rec)
		}
	}()
	return f(ctx, req)
}
