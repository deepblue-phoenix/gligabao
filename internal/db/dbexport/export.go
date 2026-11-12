package dbexport

import (
	"archive/zip"
	"bufio"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_dataset"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

// Source is what is exported: the databases of gligabao (the relational/graph database, the vector store and the image directory).
// It is what a db.DB holds; it is a type of its own so that this package does not depend on how the databases are opened.
//
// Only what is local is exported. A remote database (PostgreSQL) or vector store (Pinecone) persists its data itself and is left out;
// the manifest says so (Manifest.Skipped). The image files and the extra files are always exported.
type Source struct {
	// Relational is the relational and graph database, of the given Backend (config.DBBackendSQLite or config.DBBackendPostgres). Its
	// tables are exported only for SQLite.
	Relational *sql.DB
	Backend    string
	// Vector is the vector store. Its vectors are exported only if it keeps them locally (it is a vector.Exporter: Chromem).
	Vector              vector.Store
	VectorStore         string // the name of the store, informational (config.VectorStoreChromem, ...)
	EmbeddingDimensions int
	// ImageDir is the directory of the image files ("" or one that does not exist: no images).
	ImageDir string
}

// Extra is a file the caller adds to the archive, as entry "extra/<Name>". Name is a relative path with forward slashes.
type Extra struct {
	Name string
	Data []byte
}

// Options are the settings of Write.
type Options struct {
	// Source and Version name the program that writes the archive and its version (informational, in the manifest).
	Source  string
	Version string
	// Extra files to add.
	Extra []Extra
}

// Write writes the archive of everything in src to w and returns its manifest (also the last entry of the archive). The tables are read
// in one transaction, so they are consistent with each other. Nothing in src should be written to while Write runs: the vectors and the
// images are not part of that transaction.
//
// On an error the archive is incomplete: use WriteFile to get it written only when it is whole.
func Write(ctx context.Context,
	w io.Writer,
	src Source,
	opts Options) (*Manifest, error) {
	if src.Backend == config.DBBackendSQLite && src.Relational == nil {
		return nil, errors.New("dbexport: the source has no relational database")
	}
	for _, e := range opts.Extra {
		if err := checkRelative(e.Name); err != nil {
			return nil, fmt.Errorf("dbexport: extra file %q: %w",
				e.Name,
				err)
		}
	}

	m := &Manifest{
		Format:              Format,
		FormatVersion:       FormatVersion,
		Source:              opts.Source,
		Version:             opts.Version,
		CreatedAt:           time.Now().UTC().Truncate(time.Second),
		Backend:             src.Backend,
		VectorStore:         src.VectorStore,
		EmbeddingDimensions: src.EmbeddingDimensions,
		Tables:              []Table{},
		Vectors:             []VectorDataset{},
		Images:              []ImageFile{},
		Skipped:             []Skipped{},
		Extra:               []string{},
	}
	zw := zip.NewWriter(w)

	if src.Backend == config.DBBackendSQLite {
		if err := writeTables(ctx,
			zw,
			src.Relational,
			m); err != nil {
			return nil, err
		}
	} else {
		m.Skipped = append(m.Skipped, Skipped{"tables", fmt.Sprintf("the relational database is %s, a remote one: its data is persisted there", src.Backend)})
	}
	if exporter, ok := src.Vector.(vector.Exporter); ok {
		if err := writeVectors(ctx,
			zw,
			exporter,
			m); err != nil {
			return nil, err
		}
	} else {
		m.Skipped = append(m.Skipped, Skipped{"vectors", fmt.Sprintf("the vector store is %s, a remote one: its data is persisted there", src.VectorStore)})
	}
	if err := writeImages(ctx,
		zw,
		src.ImageDir,
		m); err != nil {
		return nil, err
	}
	for _, e := range opts.Extra {
		if err := addFile(zw,
			extraDir+e.Name,
			zip.Deflate,
			e.Data); err != nil {
			return nil, err
		}
		m.Extra = append(m.Extra, e.Name)
	}

	body, err := json.MarshalIndent(m,
		"",
		"  ")
	if err != nil {
		return nil, fmt.Errorf("dbexport: manifest: %w", err)
	}
	if err := addFile(zw,
		ManifestFile,
		zip.Deflate,
		append(body, '\n')); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("dbexport: finish the archive: %w", err)
	}
	return m, nil
}

// WriteFile is Write into the file at path: it is written to a temporary file next to it that replaces it only when the archive is
// whole, so a failed export leaves nothing behind.
func WriteFile(ctx context.Context,
	path string,
	src Source,
	opts Options) (*Manifest, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("dbexport: %w", err)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".export-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("dbexport: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	bw := bufio.NewWriterSize(tmp, 1<<20)
	m, err := Write(ctx,
		bw,
		src,
		opts)
	if err == nil {
		err = bw.Flush()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return nil, fmt.Errorf("dbexport: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return nil, fmt.Errorf("dbexport: %w", err)
	}
	return m, nil
}

//---------------------------------------------------------------------------------------------

func checkRelative(name string) error {
	if name == "" || name != path.Clean(name) || strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") {
		return errors.New("not a clean relative path")
	}
	return nil
}

// addFile adds an entry with the given content.
func addFile(zw *zip.Writer,
	name string,
	method uint16,
	data []byte) error {
	w, err := zw.CreateHeader(&zip.FileHeader{
		Name:     name,
		Method:   method,
		Modified: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("dbexport: add %s: %w",
			name,
			err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("dbexport: add %s: %w",
			name,
			err)
	}
	return nil
}

// streamFile starts an entry (compressed) and returns a buffered writer to it; call the returned flush when done.
func streamFile(zw *zip.Writer,
	name string,
	method uint16) (*bufio.Writer, error) {
	w, err := zw.CreateHeader(&zip.FileHeader{
		Name:     name,
		Method:   method,
		Modified: time.Now().UTC(),
	})
	if err != nil {
		return nil, fmt.Errorf("dbexport: add %s: %w",
			name,
			err)
	}
	return bufio.NewWriterSize(w, 256<<10), nil
}

//---------------------------------------------------------------------------------------------
// Tables

func writeTables(ctx context.Context,
	zw *zip.Writer,
	rel *sql.DB,
	m *Manifest) error {
	tx, err := rel.BeginTx(ctx, nil) // one transaction for all the tables
	if err != nil {
		return fmt.Errorf("dbexport: begin: %w", err)
	}
	defer tx.Rollback()

	tables, err := introspect(ctx, tx)
	if err != nil {
		return err
	}
	for _, t := range tables {
		t.File = tablesDir + t.Name + ".jsonl"
		if t.Columns == nil {
			t.Columns = []Column{}
		}
		if t.PrimaryKey == nil {
			t.PrimaryKey = []string{}
		}
		if t.ForeignKeys == nil {
			t.ForeignKeys = []ForeignKey{}
		}
		w, err := streamFile(zw,
			t.File,
			zip.Deflate)
		if err != nil {
			return err
		}
		if t.Rows, err = dumpTable(ctx,
			tx,
			t,
			w); err != nil {
			return fmt.Errorf("dbexport: table %s: %w",
				t.Name,
				err)
		}
		if err := w.Flush(); err != nil {
			return fmt.Errorf("dbexport: table %s: %w",
				t.Name,
				err)
		}
		m.Tables = append(m.Tables, t)
	}
	return nil
}

// dumpTable writes the rows of the table to w as JSON lines, in primary-key order, and returns how many there are.
func dumpTable(ctx context.Context,
	tx *sql.Tx,
	t Table,
	w io.Writer) (int64, error) {
	if len(t.Columns) == 0 {
		return 0, nil
	}
	cols := make([]string, len(t.Columns))
	names := make([][]byte, len(t.Columns)) // the JSON of each column's name, with the colon
	for i, c := range t.Columns {
		cols[i] = quote(c.Name)
		b, _ := json.Marshal(c.Name)
		names[i] = append(b, ':')
	}
	query := `SELECT ` + strings.Join(cols, ", ") + ` FROM ` + quote(t.Name)
	switch {
	case len(t.PrimaryKey) > 0:
		order := make([]string, len(t.PrimaryKey))
		for i, c := range t.PrimaryKey {
			order[i] = quote(c)
		}
		query += ` ORDER BY ` + strings.Join(order, ", ")
	default:
		query += ` ORDER BY rowid`
	}
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	var n int64
	line := make([]byte,
		0,
		1024)
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}
		line = append(line[:0], '{')
		for i, v := range vals {
			if i > 0 {
				line = append(line, ',')
			}
			enc, err := encodeValue(v)
			if err != nil {
				return n, fmt.Errorf("column %s: %w",
					t.Columns[i].Name,
					err)
			}
			line = append(append(line, names[i]...), enc...)
		}
		line = append(line,
			'}',
			'\n')
		if _, err := w.Write(line); err != nil {
			return n, err
		}
		if n++; n%1000 == 0 {
			if err := ctx.Err(); err != nil {
				return n, err
			}
		}
	}
	return n, rows.Err()
}

// encodeValue is the JSON of a value as the driver scanned it (see the package documentation).
func encodeValue(v any) ([]byte, error) {
	switch x := v.(type) {
	case nil:
		return []byte("null"), nil
	case []byte:
		if utf8.Valid(x) {
			return json.Marshal(string(x))
		}
		return json.Marshal(map[string]string{"$base64": base64.StdEncoding.EncodeToString(x)})
	case time.Time:
		return json.Marshal(x.UTC().Format(time.RFC3339Nano))
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return []byte("null"), nil
		}
		return json.Marshal(x)
	case int64:
		return strconv.AppendInt(nil,
			x,
			10), nil
	default:
		return json.Marshal(v)
	}
}

//---------------------------------------------------------------------------------------------
// Vectors

// vectorLine is a line of a vectors file.
type vectorLine struct {
	ID       string         `json:"id"`
	Values   []float32      `json:"values"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

func writeVectors(ctx context.Context,
	zw *zip.Writer,
	e vector.Exporter,
	m *Manifest) error {
	datasets, err := e.Datasets(ctx)
	if err != nil {
		return fmt.Errorf("dbexport: list the vector datasets: %w", err)
	}
	used := map[string]bool{}
	for _, name := range datasets {
		file := vectorsDir + uniqueName(vectorFileName(name), used) + ".jsonl"
		w, err := streamFile(zw,
			file,
			zip.Deflate)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		var count int64
		err = e.Export(ctx,
			name,
			func(v vector.Vector) error {
				count++
				return enc.Encode(vectorLine{
					ID:       v.ID,
					Values:   v.Values,
					Metadata: v.Metadata,
				})
			})
		if err == nil {
			err = w.Flush()
		}
		if err != nil {
			return fmt.Errorf("dbexport: vector dataset %q: %w",
				name,
				err)
		}
		vd := VectorDataset{
			Name:  name,
			File:  file,
			Count: count,
		}
		if id, idx, ok := memory_dataset.ParseVectorDataset(name); ok {
			vd.DatasetID, vd.Index = &id, string(idx)
		}
		m.Vectors = append(m.Vectors, vd)
	}
	return nil
}

// vectorFileName is a name for the entry of a vector dataset: the dataset's name made safe for a path ("" is the default dataset).
func vectorFileName(dataset string) string {
	if dataset == "" {
		return "_default"
	}
	return url.PathEscape(dataset)
}

// uniqueName is name, or name-2, name-3, ... if it is used already; it marks the one it returns as used.
func uniqueName(name string,
	used map[string]bool) string {
	n := name
	for i := 2; used[n]; i++ {
		n = name + "-" + strconv.Itoa(i)
	}
	used[n] = true
	return n
}

//---------------------------------------------------------------------------------------------
// Images

func writeImages(ctx context.Context,
	zw *zip.Writer,
	dir string,
	m *Manifest) error {
	if dir == "" {
		return nil
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil // nothing was ever stored: the directory is created with the first image
	}
	var files []string
	err := filepath.WalkDir(dir, func(p string,
		d fs.DirEntry,
		err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".tmp-") {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return fmt.Errorf("dbexport: read the image directory: %w", err)
	}
	sort.Strings(files)
	for _, rel := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		size, err := copyImage(zw,
			imagesDir+rel,
			filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return fmt.Errorf("dbexport: image %s: %w",
				rel,
				err)
		}
		m.Images = append(m.Images, ImageFile{
			Path: rel,
			Size: size,
			File: imagesDir + rel,
		})
	}
	return nil
}

// copyImage adds the file at src as the entry name, stored as it is (images are compressed already).
func copyImage(zw *zip.Writer,
	name,
	src string) (int64, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	w, err := zw.CreateHeader(&zip.FileHeader{
		Name:     name,
		Method:   zip.Store,
		Modified: time.Now().UTC(),
	})
	if err != nil {
		return 0, err
	}
	return io.Copy(w, f)
}
