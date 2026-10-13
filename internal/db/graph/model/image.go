package model

import "time"

// The image pipeline's records. Images are kept apart from the text artifacts (gb_artifacts) and the graph: an image
// artifact has its own table, and what the LLM read from it is stored as image extractions, not as entities or edges
// (grounding them in the graph comes later).

// NewImage is an image artifact with its first extraction, stored together in one transaction.
type NewImage struct {
	DatasetID int64
	MediaType string // image/jpeg, image/png, image/gif or image/webp
	SHA256    string // hex SHA-256 of the file
	SizeBytes int64
	Path      string    // where the file is, relative to the image directory (see package imagestore)
	ValidAt   time.Time // event time: when the image was captured or observed
	CreatedAt time.Time // ingestion time

	Extraction NewImageExtraction
}

// NewImageExtraction is what an LLM read from an image.
type NewImageExtraction struct {
	Provider, Model string
	Theme           string           // what the whole image is: "graph", "plot", ...
	Title           string           // the image's own title, if it has one
	Text            string           // all the text in the image, in reading order
	TextBlocks      []ImageTextBlock // the text regions, in reading order
	Raw             string           // the model's output as received
	InputTokens     int
	OutputTokens    int
	CostUSD         float64
}

// ImageTextBlock is one region of text found in an image. Kind is the role of the text, e.g. "title" or "paragraph".
type ImageTextBlock struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// ImageArtifact is a stored image: its metadata and where its file is. The bytes are not in the database.
type ImageArtifact struct {
	ID        int64
	DatasetID int64
	MediaType string
	SHA256    string
	SizeBytes int64
	Path      string // relative to the image directory (see package imagestore)
	ValidAt   time.Time
	CreatedAt time.Time
}

// ImageExtraction is a stored extraction with its text blocks.
type ImageExtraction struct {
	ID              int64
	ImageArtifactID int64
	Provider, Model string
	Theme           string
	Title           string
	Text            string
	TextBlocks      []ImageTextBlock
	Raw             string
	InputTokens     int
	OutputTokens    int
	CostUSD         float64
	CreatedAt       time.Time
}
