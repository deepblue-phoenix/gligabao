package memory_gitexport

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"crypto/sha1" //nolint:gosec // git object IDs are SHA-1
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Sink receives the files of a bare git repository: "HEAD", "config", "refs/heads/main" and the loose objects
// "objects/ab/cdef...". Paths use "/" and are relative to the repository.
type Sink interface {
	WriteFile(path string, data []byte) error
}

// DirSink writes the repository into a directory (created if needed).
type DirSink struct{ Dir string }

// WriteFile implements Sink.
func (s DirSink) WriteFile(path string, data []byte) error {
	full := filepath.Join(s.Dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o644)
}

// TarGzSink writes the repository as a gzip-compressed tar archive, every entry under the directory Prefix
// (e.g. "main.git"). The archive is deterministic: fixed times, owners and modes. Close it to finish the archive.
type TarGzSink struct {
	Prefix string
	gz     *gzip.Writer
	tw     *tar.Writer
}

// NewTarGzSink returns a TarGzSink that writes to w.
func NewTarGzSink(w io.Writer, prefix string) *TarGzSink {
	gz := gzip.NewWriter(w)
	return &TarGzSink{Prefix: prefix, gz: gz, tw: tar.NewWriter(gz)}
}

// WriteFile implements Sink.
func (s *TarGzSink) WriteFile(path string, data []byte) error {
	name := path
	if s.Prefix != "" {
		name = s.Prefix + "/" + path
	}
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
	if err := s.tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := s.tw.Write(data)
	return err
}

// Close finishes the tar and gzip streams (it does not close the underlying writer).
func (s *TarGzSink) Close() error {
	if err := s.tw.Close(); err != nil {
		return err
	}
	return s.gz.Close()
}

//---------------------------------------------------------------------------------------------

// store writes git objects (as loose, zlib-compressed objects) to a sink, once each.
type store struct {
	sink Sink
	seen map[string]bool
	buf  bytes.Buffer
}

func newStore(sink Sink) *store { return &store{sink: sink, seen: map[string]bool{}} }

// put stores one object and returns its ID (40 hex characters).
func (s *store) put(kind string, content []byte) (string, error) {
	header := kind + " " + strconv.Itoa(len(content)) + "\x00"
	h := sha1.New() //nolint:gosec
	h.Write([]byte(header))
	h.Write(content)
	id := hex.EncodeToString(h.Sum(nil))
	if s.seen[id] {
		return id, nil
	}
	s.seen[id] = true
	s.buf.Reset()
	zw := zlib.NewWriter(&s.buf)
	if _, err := zw.Write([]byte(header)); err != nil {
		return "", err
	}
	if _, err := zw.Write(content); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	if err := s.sink.WriteFile("objects/"+id[:2]+"/"+id[2:], append([]byte(nil), s.buf.Bytes()...)); err != nil {
		return "", err
	}
	return id, nil
}

// commit stores a commit object. parent is "" for the first commit. Author and committer are the same.
func (s *store) commit(tree, parent string, at time.Time, message string) (string, error) {
	sig := fmt.Sprintf("%s <%s> %d +0000", authorName, authorEmail, at.Unix())
	var b strings.Builder
	b.WriteString("tree " + tree + "\n")
	if parent != "" {
		b.WriteString("parent " + parent + "\n")
	}
	b.WriteString("author " + sig + "\ncommitter " + sig + "\n\n")
	b.WriteString(strings.TrimRight(message, "\n") + "\n")
	return s.put("commit", []byte(b.String()))
}

//---------------------------------------------------------------------------------------------

// dir is a directory of the repository's tree. Only the directories on the path of a changed file are rewritten
// (dirty); the others keep the tree ID of the last commit.
type dir struct {
	files map[string]string // file name -> blob ID
	dirs  map[string]*dir
	id    string // tree ID, valid when !dirty
	dirty bool
}

func newDir() *dir { return &dir{files: map[string]string{}, dirs: map[string]*dir{}, dirty: true} }

// set puts the blob at path ("a/b/c.md"), creating directories as needed.
func (d *dir) set(path, blob string) {
	d.dirty = true
	i := strings.IndexByte(path, '/')
	if i < 0 {
		d.files[path] = blob
		return
	}
	sub := d.dirs[path[:i]]
	if sub == nil {
		sub = newDir()
		d.dirs[path[:i]] = sub
	}
	sub.set(path[i+1:], blob)
}

// write stores the tree objects of the dirty directories (children first) and returns the tree ID.
func (d *dir) write(s *store) (string, error) {
	if !d.dirty {
		return d.id, nil
	}
	type entry struct {
		name, key, mode, id string
	}
	entries := make([]entry, 0, len(d.files)+len(d.dirs))
	for name, id := range d.files {
		entries = append(entries, entry{name, name, "100644", id})
	}
	for name := range d.dirs {
		entries = append(entries, entry{name, name + "/", "40000", ""}) // git sorts a directory as if its name ended in "/"
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	for i := range entries { // in order, so the objects are written in a fixed order
		if e := &entries[i]; e.mode == "40000" {
			id, err := d.dirs[e.name].write(s)
			if err != nil {
				return "", err
			}
			e.id = id
		}
	}
	var b bytes.Buffer
	for _, e := range entries {
		raw, err := hex.DecodeString(e.id)
		if err != nil {
			return "", err
		}
		b.WriteString(e.mode + " " + e.name + "\x00")
		b.Write(raw)
	}
	id, err := s.put("tree", b.Bytes())
	if err != nil {
		return "", err
	}
	d.id, d.dirty = id, false
	return id, nil
}
