package training

// The training datasets directory: every subdirectory with a manifest (dataset.toml) is one dataset. gligabao scans the
// directory for manifests (DiscoverDatasets) and reads each one to learn the dataset's name and where its data file is,
// relative to the manifest. Layout:
//
//	<datasets_training dir>/
//	    my-dataset/
//	        dataset.toml        the manifest
//	        data.jsonl          the file the manifest's `file` names
//	        generate.py         anything else (a generator script, raw sources): gligabao ignores it
//
// See docs/training/datasets.md for the manifest's keys.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

// ManifestFilename is the name of a dataset's manifest, inside the dataset's own subdirectory.
const ManifestFilename = "dataset.toml"

// FormatJSONL is the only data format there is for now: {"instruction", "input"?, "output"} objects, one per line.
const FormatJSONL = "jsonl"

// datasetNameRe is what a dataset's name may be. The name plus ".jsonl" is the file name the dataset has on the sidecar
// (sidecar/ai_train/ai_train_core/dataset.py), hence the length limit.
var datasetNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,121}$`)

func ValidDatasetName(name string) bool {
	return datasetNameRe.MatchString(name) && !strings.Contains(name, "..")
}

// Manifest is the content of a dataset.toml.
type Manifest struct {
	// Name is what a training job asks for. Optional: the subdirectory's name if empty.
	Name        string `toml:"name,omitempty" json:"name"`
	Description string `toml:"description,omitempty" json:"description"`
	// Format is the data format; only "jsonl" (the default).
	Format string `toml:"format,omitempty" json:"format"`
	// File is the data file, a relative path inside the dataset's directory. Required.
	File string `toml:"file" json:"file"`
	// Version and Generator are informational: the dataset's version, and the script in the directory that creates File.
	Version   string `toml:"version,omitempty" json:"version,omitempty"`
	Generator string `toml:"generator,omitempty" json:"generator,omitempty"`
}

// LocalDataset is a dataset found in the training datasets directory.
type LocalDataset struct {
	Manifest        // with the defaults filled in
	Dir      string // the dataset's directory
	Path     string // the data file (Dir joined with Manifest.File)
}

// Filename is the file name the dataset has on the sidecar's data volume, and in its record: the name plus ".jsonl".
func (d LocalDataset) Filename() string { return d.Name + ".jsonl" }

// DatasetProblem is a subdirectory of the training datasets directory that has a manifest but is not usable as a dataset.
type DatasetProblem struct {
	Dir   string `json:"dir"` // the subdirectory's name
	Error string `json:"error"`
}

// ParseManifest reads the manifest at path (in the dataset directory dir) and checks it, not the data file's existence.
func ParseManifest(path string) (*LocalDataset, error) {
	dir := filepath.Dir(path)
	var m Manifest
	if _, err := toml.DecodeFile(path, &m); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}
	if m.Name == "" {
		m.Name = filepath.Base(dir)
	}
	if !ValidDatasetName(m.Name) {
		return nil, fmt.Errorf("invalid dataset name %q: letters, digits, . _ - (starting with a letter or digit), at most 122 characters; set `name` in the manifest", m.Name)
	}
	if m.Format == "" {
		m.Format = FormatJSONL
	}
	if m.Format != FormatJSONL {
		return nil, fmt.Errorf("unsupported format %q (only %q)", m.Format, FormatJSONL)
	}
	if m.File == "" {
		return nil, errors.New("the manifest has no `file` (the data file, relative to the manifest)")
	}
	if !filepath.IsLocal(m.File) || !strings.HasSuffix(m.File, ".jsonl") {
		return nil, fmt.Errorf("`file` %q must be a relative path inside the dataset's directory, ending in .jsonl", m.File)
	}
	return &LocalDataset{Manifest: m, Dir: dir, Path: filepath.Join(dir, filepath.FromSlash(m.File))}, nil
}

// DiscoverDatasets scans the directories dirs, in order, for the datasets: each direct subdirectory that has a dataset.toml is
// read. It returns the usable ones, by name, and the problems of the others (a bad manifest, a missing data file, a name already
// taken by another dataset of the same directory). A subdirectory without a manifest is not a dataset and not a problem. A missing
// directory has no datasets. A dataset whose name was found in an earlier directory is hidden by it: it is neither returned nor a
// problem. With more than one directory, a problem's Dir is the path of the subdirectory (not just its name).
func DiscoverDatasets(dirs ...string) ([]LocalDataset, []DatasetProblem) {
	var found []LocalDataset
	var problems []DatasetProblem
	shown := map[string]bool{} // names found so far, in any directory
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		problemDir := func(sub string) string { // how a problem names the subdirectory sub of dir
			if len(dirs) > 1 {
				return filepath.Join(dir, sub)
			}
			return sub
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				problems = append(problems, DatasetProblem{Dir: problemDir("."), Error: err.Error()})
			}
			continue
		}
		taken := map[string]string{} // name -> subdirectory, in this directory
		for _, e := range entries {
			sub := filepath.Join(dir, e.Name())
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if st, err := os.Stat(sub); err != nil || !st.IsDir() { // follows a symlink to a directory
				continue
			}
			manifest := filepath.Join(sub, ManifestFilename)
			if _, err := os.Stat(manifest); err != nil {
				continue
			}
			ds, err := ParseManifest(manifest)
			if err == nil {
				if st, serr := os.Stat(ds.Path); serr != nil || !st.Mode().IsRegular() {
					err = fmt.Errorf("the data file %s is missing", ds.Manifest.File)
					if ds.Generator != "" {
						err = fmt.Errorf("%w: run %s in the dataset's directory to create it", err, ds.Generator)
					}
				} else if other, dup := taken[ds.Name]; dup {
					err = fmt.Errorf("the name %q is already taken by the dataset in %s", ds.Name, other)
				}
			}
			if err != nil {
				problems = append(problems, DatasetProblem{Dir: problemDir(e.Name()), Error: err.Error()})
				continue
			}
			taken[ds.Name] = e.Name()
			if shown[ds.Name] {
				continue // hidden by a dataset of an earlier directory
			}
			shown[ds.Name] = true
			found = append(found, *ds)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })
	return found, problems
}

// FileStats describes a data file's content.
type FileStats struct {
	Size    int64
	modTime time.Time
	SHA256  string
	Records int
}

// statsCache remembers the stats of data files by path, valid while a file's size and modification time stay the same:
// listing the datasets (the admin UI does it often) must not read every file each time.
var statsCache sync.Map // path -> FileStats

// ContentStats validates content as a dataset (ValidateDataset) and returns its stats.
func ContentStats(content []byte) (FileStats, error) {
	records, err := ValidateDataset(content)
	if err != nil {
		return FileStats{}, err
	}
	sum := sha256.Sum256(content)
	return FileStats{Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:]), Records: records}, nil
}

// DatasetFileStats returns the stats of the data file at path; an error says why it is not a usable dataset.
func DatasetFileStats(path string) (FileStats, error) {
	st, err := os.Stat(path)
	if err != nil {
		return FileStats{}, err
	}
	if st.Size() > MaxDatasetBytes {
		return FileStats{}, fmt.Errorf("the data file is over %d MB", MaxDatasetBytes>>20)
	}
	if v, ok := statsCache.Load(path); ok {
		if s := v.(FileStats); s.Size == st.Size() && s.modTime.Equal(st.ModTime()) {
			return s, nil
		}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return FileStats{}, err
	}
	s, err := ContentStats(content)
	if err != nil {
		return FileStats{}, fmt.Errorf("invalid data file: %w", err)
	}
	s.modTime = st.ModTime()
	statsCache.Store(path, s)
	return s, nil
}
