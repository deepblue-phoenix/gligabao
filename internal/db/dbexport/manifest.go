package dbexport

import "time"

// Archive constants.
const (
	// Format is the Manifest.Format of every archive.
	Format = "gligabao-export"
	// FormatVersion changes when the layout of the archive does in a way that breaks a reader.
	FormatVersion = 1

	// ManifestFile is the entry with the Manifest.
	ManifestFile = "manifest.json"

	tablesDir  = "tables/"
	vectorsDir = "vectors/"
	imagesDir  = "images/"
	extraDir   = "extra/"
)

// Manifest says what an archive holds.
type Manifest struct {
	Format        string `json:"format"` // Format
	FormatVersion int    `json:"format_version"`
	// Source names the program that wrote the archive and Version its version (both informational).
	Source    string    `json:"source,omitempty"`
	Version   string    `json:"version,omitempty"`
	CreatedAt time.Time `json:"created_at"`

	// Backend is the relational database of the source ("sqlite" or "postgres"); the tables are only exported from SQLite, whose types
	// are those in Columns.
	Backend string `json:"backend"`
	// VectorStore is the vector store the vectors come from ("chromem" or "pinecone") and EmbeddingDimensions their size.
	VectorStore         string `json:"vector_store"`
	EmbeddingDimensions int    `json:"embedding_dimensions"`

	// Tables are the tables, parents first.
	Tables []Table `json:"tables"`
	// Vectors are the datasets of the vector store.
	Vectors []VectorDataset `json:"vectors"`
	// Images are the image files.
	Images []ImageFile `json:"images"`
	// Skipped lists what was not exported, and why: only what is local is (SQLite, Chromem); a remote database or vector store persists
	// its data itself.
	Skipped []Skipped `json:"skipped"`
	// Extra are the names of the extra files (entries "extra/<name>").
	Extra []string `json:"extra"`
}

// Skipped is a part of the state that is not in the archive. What is "tables" or "vectors".
type Skipped struct {
	What   string `json:"what"`
	Reason string `json:"reason"`
}

// Table is one table of the archive.
type Table struct {
	Name string `json:"name"`
	// File is the entry with the rows.
	File    string   `json:"file"`
	Rows    int64    `json:"rows"`
	Columns []Column `json:"columns"`
	// PrimaryKey are the columns of the primary key, in order (empty if the table has none).
	PrimaryKey  []string     `json:"primary_key"`
	ForeignKeys []ForeignKey `json:"foreign_keys"`
}

// Column is a column of a table, in the order of the table. Type is the type the source database declares for it.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// ForeignKey says that Columns of the table reference RefColumns of RefTable.
type ForeignKey struct {
	Columns    []string `json:"columns"`
	RefTable   string   `json:"ref_table"`
	RefColumns []string `json:"ref_columns"`
}

// VectorDataset is a dataset of the vector store.
type VectorDataset struct {
	// Name is the dataset's name in the vector store ("" is the store's default dataset).
	Name  string `json:"name"`
	File  string `json:"file"`
	Count int64  `json:"count"`
	// DatasetID and Index say which gligabao dataset the vectors belong to and what they are embeddings of, for a name made by
	// memory_dataset.VectorDataset; DatasetID is nil for any other name.
	DatasetID *int64 `json:"dataset_id,omitempty"`
	Index     string `json:"index,omitempty"`
}

// ImageFile is a file of the image directory; Path is relative to it, with forward slashes (as in gb_image_artifacts.path).
type ImageFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	File string `json:"file"`
}
