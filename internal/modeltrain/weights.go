package modeltrain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"

	"github.com/deepblue-phoenix/gligabao/internal/evals/userfiles"
	"github.com/deepblue-phoenix/gligabao/internal/sys/userpath"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
)

// The weights of the trained GCNII* models. Sargo trains and serves the model of each user from a local directory (its working copy,
// see rust/sargo/src/gcnii/users.rs), which is lost with the machine. Weights keeps every finished model in a durable store, and each
// one has a row in gb_trained_models (the table of the LoRA models of the sidecar), so the models are tracked the same way:
//
//	<prefix>/<model type>/<user segment>/<job id>/model.weights      the weights file of sargo
//	<prefix>/<model type>/<user segment>/<job id>/classes.json       the names of the outputs
//
// The store is the S3 bucket of the weights (prefix "model_weights" by default, so "model_weights/gcnii/<user>/<job id>/..."), or, with
// no bucket, a local directory. A model is never overwritten: every training has a job id of its own. When a machine has lost sargo's
// working copy, Restore writes the latest model of the user back into it.

const (
	// WeightsFile and ClassesFile are the files of a model, in the directory of sargo and in the store.
	WeightsFile = "model.weights"
	ClassesFile = "classes.json"
	// MarkerFile is next to them in sargo's directory of the user and holds the ID of the row (gb_trained_models) whose weights are there,
	// so an instance can tell that its copy is not the latest one (another instance of the cluster trained a newer model).
	MarkerFile = "model_id"
)

// WeightsConfig is where the weights are kept: the bucket S3 (S3.Prefix is the prefix of every model type, default
// DefaultWeightsPrefix), or LocalDir without one.
type WeightsConfig struct {
	S3       userfiles.S3Config
	LocalDir string
}

// DefaultWeightsPrefix is the "subdirectory" of the bucket the weights go to when no prefix is configured.
const DefaultWeightsPrefix = "model_weights"

// Weights keeps the weights of the GCNII* models. Build it with NewWeights.
type Weights struct {
	// Store is rooted at "<prefix>/<model type>" (S3) or "<dir>/<model type>" (local): user and relative path are appended by it.
	Store userfiles.Store
	// Storage is trainingdb.StorageS3 or trainingdb.StorageLocal: what the rows made by this value say.
	Storage string
	// Root is the directory the Path of a row starts with: "<prefix>/<model type>" in S3, "<model type>" locally.
	Root string
	// Location says where the weights are, without secrets, for logs ("s3://bucket/prefix/gcnii" or the directory).
	Location string
	// ModelsDir is the directory of sargo with the working copy of the models, one subdirectory per user (see UserDir).
	ModelsDir string
}

// NewWeights makes the Weights of the GCNII* models: S3 when cfg has a bucket, else the local directory (cfg.LocalDir, or
// "model_weights" in the working directory). modelsDir is the directory sargo keeps its models in.
func NewWeights(ctx context.Context,
	cfg WeightsConfig,
	modelsDir string) (*Weights, error) {
	if modelsDir == "" {
		return nil, errors.New("modeltrain: the directory of the sargo models is required")
	}
	if cfg.S3.Bucket != "" {
		root := path.Join(prefixOrDefault(cfg.S3.Prefix), trainingdb.ModelTypeGCNII)
		s3cfg := cfg.S3
		s3cfg.Prefix = root
		store, err := userfiles.NewS3(ctx, s3cfg)
		if err != nil {
			return nil, err
		}
		return &Weights{
			Store:     store,
			Storage:   trainingdb.StorageS3,
			Root:      root,
			Location:  store.Location(),
			ModelsDir: modelsDir,
		}, nil
	}
	dir := cfg.LocalDir
	if dir == "" {
		dir = DefaultWeightsPrefix
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("modeltrain: weights directory: %w", err)
	}
	return &Weights{
		Store:     userfiles.FS{Dir: filepath.Join(abs, trainingdb.ModelTypeGCNII)},
		Storage:   trainingdb.StorageLocal,
		Root:      trainingdb.ModelTypeGCNII,
		Location:  filepath.Join(abs, trainingdb.ModelTypeGCNII),
		ModelsDir: modelsDir,
	}, nil
}

func prefixOrDefault(prefix string) string {
	if p := path.Clean("/" + prefix); p != "/" {
		return p[1:]
	}
	return DefaultWeightsPrefix
}

// UserDir is the directory of sargo's working copy of the model of a user: sargo names it by the hex of the user ID.
func (w *Weights) UserDir(userID string) string {
	return filepath.Join(w.ModelsDir, hex.EncodeToString([]byte(userID)))
}

// NewJobID is the ID of the model made by the training of a user that started at startedUnix (sargo's job time): unique, because of the
// random suffix, and sorted by start time. It is a valid relative path element.
func NewJobID(startedUnix int64,
	random []byte) string {
	return "gcnii_" + strconv.FormatInt(startedUnix, 10) + "_" + hex.EncodeToString(random)
}

// ModelPath is the Path of the row of a model: the directory of the weights in the store, from its root ("model_weights/gcnii/<user
// segment>/<job id>" in S3).
func (w *Weights) ModelPath(userID,
	jobID string) (string, error) {
	seg, err := userpath.Segment(userID)
	if err != nil {
		return "", err
	}
	return path.Join(w.Root, seg, jobID), nil
}

// Save copies the model that sargo trained for a user (its working copy) into the store, under jobID, and returns the files it
// wrote with their sizes and sha256. It fails if sargo has no model for the user.
func (w *Weights) Save(ctx context.Context,
	userID,
	jobID string) ([]trainingdb.TrainedModelFile, error) {
	dir := w.UserDir(userID)
	files := []string{WeightsFile, ClassesFile}
	out := make([]trainingdb.TrainedModelFile, 0, len(files))
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			if name == ClassesFile && errors.Is(err, os.ErrNotExist) {
				continue // sargo writes it after the weights; a model without it still works
			}
			return nil, fmt.Errorf("modeltrain: read the model of the user: %w", err)
		}
		if err := w.Store.Put(ctx,
			userID,
			jobID+"/"+name,
			data); err != nil {
			return nil, err
		}
		out = append(out, trainingdb.TrainedModelFile{
			Path:      name,
			SizeBytes: int64(len(data)),
			SHA256:    sha256Hex(data),
		})
	}
	return out, nil
}

// Restore writes the files of model m back into the working copy of sargo (its directory of the user), each one checked against the
// size and sha256 the row recorded; a file is renamed into place only when complete, and the weights go last, so sargo never loads a
// model with the classes of another one.
func (w *Weights) Restore(ctx context.Context,
	m trainingdb.TrainedModel) error {
	dir := w.UserDir(m.UserID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("modeltrain: %w", err)
	}
	ordered := weightsLast(m.Files)
	for _, f := range ordered {
		data, err := w.Store.Get(ctx,
			m.UserID,
			m.JobID+"/"+f.Path)
		if err != nil {
			return fmt.Errorf("modeltrain: get %s of model %d: %w", f.Path, m.ID, err)
		}
		if int64(len(data)) != f.SizeBytes || (f.SHA256 != "" && sha256Hex(data) != f.SHA256) {
			return fmt.Errorf("modeltrain: %s of model %d does not match the size and sha256 recorded", f.Path, m.ID)
		}
		if err := writeAtomic(filepath.Join(dir, f.Path),
			data); err != nil {
			return err
		}
	}
	return w.WriteMarker(m.UserID, m.ID)
}

// WriteMarker says that the files in sargo's directory of the user are the weights of model id.
func (w *Weights) WriteMarker(userID string,
	id int64) error {
	if err := os.MkdirAll(w.UserDir(userID), 0o755); err != nil {
		return fmt.Errorf("modeltrain: %w", err)
	}
	return writeAtomic(filepath.Join(w.UserDir(userID), MarkerFile),
		[]byte(strconv.FormatInt(id, 10)))
}

// Marker is the ID of the model whose weights are in sargo's directory of the user, 0 if it is not known (no marker, or no directory).
func (w *Weights) Marker(userID string) int64 {
	b, err := os.ReadFile(filepath.Join(w.UserDir(userID), MarkerFile))
	if err != nil {
		return 0
	}
	id, _ := strconv.ParseInt(string(b), 10, 64)
	return id
}

// weightsLast is files with WeightsFile moved to the end (the order is otherwise kept).
func weightsLast(files []trainingdb.TrainedModelFile) []trainingdb.TrainedModelFile {
	out := make([]trainingdb.TrainedModelFile, 0, len(files))
	var last []trainingdb.TrainedModelFile
	for _, f := range files {
		if f.Path == WeightsFile {
			last = append(last, f)
			continue
		}
		out = append(out, f)
	}
	return append(out, last...)
}

func writeAtomic(p string,
	data []byte) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("modeltrain: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("modeltrain: %w", err)
	}
	return nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ModelInfo is what the row of a model needs to know besides the files: what the training was asked and what sargo reported.
type ModelInfo struct {
	UserID, JobID, Dataset string
	// Graphs and ValidationGraphs are the sizes of the sets; Epochs is how many epochs were trained.
	Graphs, ValidationGraphs, Epochs int
	// Loss is the loss on the training graphs, nil if unknown.
	Loss *float64
	// Params are the other settings and the scores, a JSON object (see WeightsParams).
	Params json.RawMessage
}

// WeightsParams is the "params" JSON of the row of a GCNII* model.
type WeightsParams struct {
	// Run is the ID of the record of the training in the history (see Run), "" if there is none.
	Run string `json:"run,omitempty"`
	// Train are the settings the request gave (0 is sargo's default), Classes the outputs of the model.
	Train   TrainParams `json:"train"`
	Classes []string    `json:"classes"`
	// Accuracy and Loss are measured on the training graphs, ValAccuracy and ValLoss on the validation graphs.
	Accuracy    *float64 `json:"accuracy,omitempty"`
	Loss        *float64 `json:"loss,omitempty"`
	ValAccuracy *float64 `json:"val_accuracy,omitempty"`
	ValLoss     *float64 `json:"val_loss,omitempty"`
}

// NewModel is the row of a GCNII* model whose weights are the files in the store of w.
func (w *Weights) NewModel(info ModelInfo,
	files []trainingdb.TrainedModelFile) (trainingdb.TrainedModel, error) {
	dir, err := w.ModelPath(info.UserID, info.JobID)
	if err != nil {
		return trainingdb.TrainedModel{}, err
	}
	return trainingdb.TrainedModel{
		ModelType:     trainingdb.ModelTypeGCNII,
		UserID:        info.UserID,
		Storage:       w.Storage,
		Name:          fmt.Sprintf("gcnii-%s-%d-graphs-%s", datasetLabel(info.Dataset), info.Graphs, shortID(info.JobID)),
		JobID:         info.JobID,
		Dataset:       info.Dataset,
		NumExamples:   info.Graphs,
		NumTrainSteps: info.Epochs,
		FinalLoss:     info.Loss,
		Params:        info.Params,
		Path:          dir,
		Files:         files,
		SizeBytes:     totalSize(files),
	}, nil
}

func datasetLabel(dataset string) string {
	if dataset == "" {
		return "adhoc"
	}
	return dataset
}

// shortID is the last 8 characters of a job ID (its random part).
func shortID(jobID string) string {
	return jobID[max(0, len(jobID)-8):]
}

func totalSize(files []trainingdb.TrainedModelFile) int64 {
	var n int64
	for _, f := range files {
		n += f.SizeBytes
	}
	return n
}
