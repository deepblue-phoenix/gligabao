package dbexport

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
)

// Reader reads an archive written by Write. Its Manifest says what is in it.
type Reader struct {
	Manifest Manifest

	zr     *zip.Reader
	closer io.Closer
}

// Open opens the archive file at path. Close it when done.
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("dbexport: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("dbexport: %w", err)
	}
	r, err := NewReader(f, info.Size())
	if err != nil {
		f.Close()
		return nil, err
	}
	r.closer = f
	return r, nil
}

// NewReader reads the archive of the given size from r. Closing the Reader does not close r.
func NewReader(r io.ReaderAt,
	size int64) (*Reader, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("dbexport: not an archive: %w", err)
	}
	rd := &Reader{
		zr: zr,
	}
	body, err := rd.read(ManifestFile)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &rd.Manifest); err != nil {
		return nil, fmt.Errorf("dbexport: manifest: %w", err)
	}
	if rd.Manifest.Format != Format {
		return nil, fmt.Errorf("dbexport: not a gligabao export (format %q)", rd.Manifest.Format)
	}
	if rd.Manifest.FormatVersion != FormatVersion {
		return nil, fmt.Errorf("dbexport: format version %d is not supported (this program reads version %d)",
			rd.Manifest.FormatVersion,
			FormatVersion)
	}
	return rd, nil
}

// Close closes the file of an archive opened with Open.
func (r *Reader) Close() error {
	if r.closer == nil {
		return nil
	}
	return r.closer.Close()
}

// open returns the entry called name.
func (r *Reader) open(name string) (io.ReadCloser, error) {
	for _, f := range r.zr.File {
		if f.Name == name {
			return f.Open()
		}
	}
	return nil, fmt.Errorf("dbexport: the archive has no %s", name)
}

func (r *Reader) read(name string) ([]byte, error) {
	rc, err := r.open(name)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// Table returns the description of the table in the manifest.
func (r *Reader) Table(name string) (Table, bool) {
	for _, t := range r.Manifest.Tables {
		if t.Name == name {
			return t, true
		}
	}
	return Table{}, false
}

// Row is a row of a table: the value of each column. The values are as decoded from JSON, except that numbers are json.Number (so that
// a 64-bit ID is exact): nil, bool, json.Number, string (a timestamp too: RFC 3339) or map[string]any ({"$base64": ...}).
type Row map[string]any

// Rows calls fn for each row of the table, in the order of its primary key. It stops at the first error of fn and returns it.
func (r *Reader) Rows(table string,
	fn func(Row) error) error {
	t, ok := r.Table(table)
	if !ok {
		return fmt.Errorf("dbexport: the archive has no table %s", table)
	}
	return r.lines(t.File, func(line []byte) error {
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		var row Row
		if err := dec.Decode(&row); err != nil {
			return fmt.Errorf("table %s: %w",
				table,
				err)
		}
		return fn(row)
	})
}

// Vectors calls fn for each vector of the dataset of the vector store (named as in the manifest), in ID order.
func (r *Reader) Vectors(dataset string,
	fn func(vector.Vector) error) error {
	for _, vd := range r.Manifest.Vectors {
		if vd.Name != dataset {
			continue
		}
		return r.lines(vd.File, func(line []byte) error {
			var vl vectorLine
			if err := json.Unmarshal(line, &vl); err != nil {
				return fmt.Errorf("vector dataset %q: %w",
					dataset,
					err)
			}
			return fn(vector.Vector{
				ID:       vl.ID,
				Values:   vl.Values,
				Metadata: vl.Metadata,
			})
		})
	}
	return fmt.Errorf("dbexport: the archive has no vector dataset %q", dataset)
}

// Images calls fn for each image file, with its path (relative to the image directory) and its content, which is only valid during the
// call.
func (r *Reader) Images(fn func(path string,
	content io.Reader) error) error {
	for _, img := range r.Manifest.Images {
		rc, err := r.open(img.File)
		if err != nil {
			return err
		}
		err = fn(img.Path, rc)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// Extra returns the extra file called name.
func (r *Reader) Extra(name string) ([]byte, error) {
	for _, n := range r.Manifest.Extra {
		if n == name {
			return r.read(extraDir + name)
		}
	}
	return nil, fmt.Errorf("dbexport: the archive has no extra file %s", name)
}

// lines calls fn with each line of the entry.
func (r *Reader) lines(name string,
	fn func([]byte) error) error {
	rc, err := r.open(name)
	if err != nil {
		return err
	}
	defer rc.Close()
	br := bufio.NewReaderSize(rc, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if fnErr := fn(line); fnErr != nil {
				return fnErr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("dbexport: read %s: %w",
				name,
				err)
		}
	}
}
