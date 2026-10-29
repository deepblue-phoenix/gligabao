package training

// Deploying a trained model: handing its weights to a deployer, a hook supplied by the program that embeds gligabao
// (ServeOptions.TrainedModelDeployers). gligabao fetches the model's files from the sidecar's storage (the Modal models
// volume) to a directory on the local disk and calls the deployer with it; what the deployer does with the files (upload them
// to a third party, copy them, convert them, ...) is its own business. Every deployment is recorded (gb_trained_model_deployments).

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/sys/sidecar"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	"github.com/deepblue-phoenix/gligabao/monitoring"
	log "github.com/sirupsen/logrus"
)

// ErrInvalidDeployer means a registered deployer is malformed.
var ErrInvalidDeployer = errors.New("invalid trained model deployer")

// The limits of a deployer.
const (
	MaxDeployerNameLen = 64
	MaxDeployers       = 20
	// FetchModelTimeout bounds fetching all the files of a model from the sidecar.
	FetchModelTimeout = 30 * time.Minute
	// MaxDeployOptionsBytes is the largest JSON a request may give a deployer as its options.
	MaxDeployOptionsBytes = 1 << 16
	// MaxDeployUpdates is how many progress messages of a deployment are kept (the newest ones), MaxDeployUpdateLen the longest
	// message (longer ones are cut).
	MaxDeployUpdates   = 200
	MaxDeployUpdateLen = 500
)

var deployerNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// DeployTrainedModelRequest is what a deployer is asked to deploy.
type DeployTrainedModelRequest struct {
	// DeployID is the ID of this deployment (gb_trained_model_deployments.id): unique, so a deployer can use it to name what it
	// creates.
	DeployID int64
	// Model is the trained model: its base model, size, dataset, training settings and files (with their sha256).
	Model trainingdb.TrainedModel
	// Dir is the absolute path of a local directory with the model's files (Model.Files, at the paths they list), already
	// verified against their sizes and sha256. gligabao never deletes it: the same directory is reused by the next deploy of
	// the model. The deployer must not modify or delete the files in it.
	Dir string
	// Options are the free-form options of the request that asked for the deployment (may be nil).
	Options map[string]any
	// Update reports what the deployer is doing, as a short plain-text message ("uploading the archive to S3"): the admin UI
	// shows the messages while the deployment runs, the same for every deployer. They are kept in memory only (not stored with the
	// deploy), so they are for display, not for the record: put what must survive in the result's Details. Never nil, safe for
	// concurrent use, cheap. Messages are cut at MaxDeployUpdateLen characters and only the last MaxDeployUpdates are kept.
	// Calls after Deploy has returned are ignored.
	Update func(message string)
}

// DeployTrainedModelResult is what a deployer reports when it is done.
type DeployTrainedModelResult struct {
	// Ref identifies what the deployment made, in the deployer's terms (e.g. the name of the model it created elsewhere).
	Ref string
	// Details is anything else worth recording; it must be JSON-encodable (may be nil).
	Details map[string]any
}

// DeployTrainedModelFunc deploys one trained model. It runs synchronously: the request that asked for the deployment waits for
// it. It must honour ctx and may be called concurrently. A returned error (or a panic) marks the deployment failed.
type DeployTrainedModelFunc func(ctx context.Context, req DeployTrainedModelRequest) (DeployTrainedModelResult, error)

// TrainedModelDeployer is a named way of deploying trained models.
type TrainedModelDeployer struct {
	// Name identifies the deployer: letters, digits, '_', '-' and '.', starting with a letter or digit, unique. It is what
	// a deployment request names, and is stored with the deployment.
	Name string
	// Description says in plain words what the deployer does and where the model goes (optional). The admin UI shows it.
	Description string
	// Deploy does the deploying.
	Deploy DeployTrainedModelFunc
}

// ResolveDeployers validates the deployers registered at startup (ErrInvalidDeployer, with what is wrong, if one is
// malformed) and returns them sorted by name.
func ResolveDeployers(list []TrainedModelDeployer) ([]TrainedModelDeployer, error) {
	if len(list) > MaxDeployers {
		return nil, fmt.Errorf("%w: at most %d deployers may be registered, got %d", ErrInvalidDeployer, MaxDeployers, len(list))
	}
	seen := map[string]bool{}
	out := make([]TrainedModelDeployer, 0, len(list))
	for i, e := range list {
		switch {
		case e.Name == "" || len(e.Name) > MaxDeployerNameLen || !deployerNameRE.MatchString(e.Name):
			return nil, fmt.Errorf("%w: deployer %d: name %q must be 1-%d letters, digits, '_', '-' or '.', starting with a letter or digit", ErrInvalidDeployer, i, e.Name, MaxDeployerNameLen)
		case seen[e.Name]:
			return nil, fmt.Errorf("%w: duplicate deployer name %q", ErrInvalidDeployer, e.Name)
		case e.Deploy == nil:
			return nil, fmt.Errorf("%w: deployer %q has no Deploy function", ErrInvalidDeployer, e.Name)
		}
		seen[e.Name] = true
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ModelFileFetcher reads the files of a trained model from where the sidecar keeps them; *sidecar.Client is one.
type ModelFileFetcher interface {
	DownloadModelFile(ctx context.Context, modelPath, file string, w io.Writer) (int64, error)
}

// ModelDir is the directory under root the files of trained model tm are fetched to: one directory per job, named after it.
func ModelDir(root string, tm *trainingdb.TrainedModel) (string, error) {
	if !deployerNameRE.MatchString(tm.JobID) || strings.ContainsAny(tm.JobID, `/\`) {
		return "", fmt.Errorf("job id %q is not usable as a directory name", tm.JobID)
	}
	abs, err := filepath.Abs(filepath.Join(root, tm.JobID))
	if err != nil {
		return "", err
	}
	return abs, nil
}

// fetchLocks serialize the fetches into one directory, so two deployments of a model do not download it twice at the same time.
var fetchLocks sync.Map // dir -> *sync.Mutex

// FetchModelFiles makes sure dir holds the files of trained model tm, at the paths tm.Files lists, each with the recorded size and
// sha256 (a file that is already there and matches is kept, the others are downloaded through f and verified). A file is
// written under a temporary name and renamed when it is complete, so dir never holds a half-written file under its real name.
func FetchModelFiles(ctx context.Context, f ModelFileFetcher, tm *trainingdb.TrainedModel, dir string) error {
	if len(tm.Files) == 0 {
		return errors.New("the trained model has no recorded files")
	}
	mu, _ := fetchLocks.LoadOrStore(dir, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	for _, file := range tm.Files {
		if !filepath.IsLocal(filepath.FromSlash(file.Path)) || strings.Contains(file.Path, `\`) {
			return fmt.Errorf("file path %q is not a relative path inside the model's directory", file.Path)
		}
		target := filepath.Join(dir, filepath.FromSlash(file.Path))
		if fileMatches(target, file) {
			continue
		}
		if err := fetchFile(ctx, f, tm.Path, file, target); err != nil {
			return fmt.Errorf("fetch %s: %w", file.Path, err)
		}
	}
	return nil
}

// fileMatches reports whether path is a file of the size and sha256 of want (the sha256 is not checked when none is recorded).
func fileMatches(path string, want trainingdb.TrainedModelFile) bool {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() != want.SizeBytes {
		return false
	}
	if want.SHA256 == "" {
		return true
	}
	fh, err := os.Open(path)
	if err != nil {
		return false
	}
	defer fh.Close()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want.SHA256)
}

func fetchFile(ctx context.Context, f ModelFileFetcher, modelPath string, file trainingdb.TrainedModelFile, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".fetch-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	h := sha256.New()
	n, err := f.DownloadModelFile(ctx, modelPath, file.Path, io.MultiWriter(tmp, h))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if n != file.SizeBytes {
		return fmt.Errorf("got %d bytes, the model records %d", n, file.SizeBytes)
	}
	if sum := hex.EncodeToString(h.Sum(nil)); file.SHA256 != "" && !strings.EqualFold(sum, file.SHA256) {
		return fmt.Errorf("sha256 is %s, the model records %s", sum, file.SHA256)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

// deployUpdates holds the progress messages of one deployment while it runs. They are kept in memory only (liveDeployments), for the admin UI.
type deployUpdates struct {
	mu     sync.Mutex
	list   []trainingdb.TrainedModelDeploymentUpdate
	closed bool
}

// liveDeployments are the running deployments' progress messages: deploy ID -> *deployUpdates.
var liveDeployments sync.Map

// add appends a message. It is the Update function of DeployTrainedModelRequest.
func (u *deployUpdates) add(message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	if r := []rune(message); len(r) > MaxDeployUpdateLen {
		message = string(r[:MaxDeployUpdateLen-1]) + "…"
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return
	}
	u.list = append(u.list, trainingdb.TrainedModelDeploymentUpdate{Time: time.Now().UTC(), Message: message})
	if len(u.list) > MaxDeployUpdates {
		u.list = u.list[len(u.list)-MaxDeployUpdates:]
	}
}

// snapshot returns a copy of the messages so far (never nil).
func (u *deployUpdates) snapshot() []trainingdb.TrainedModelDeploymentUpdate {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]trainingdb.TrainedModelDeploymentUpdate{}, u.list...)
}

// close stops recording: later calls of add are ignored.
func (u *deployUpdates) close() {
	u.mu.Lock()
	u.closed = true
	u.mu.Unlock()
}

// withLiveUpdates sets e.Updates to the messages of e if it is running (empty otherwise).
func withLiveUpdates(e *trainingdb.TrainedModelDeployment) {
	e.Updates = []trainingdb.TrainedModelDeploymentUpdate{}
	if v, ok := liveDeployments.Load(e.ID); ok {
		e.Updates = v.(*deployUpdates).snapshot()
	}
}

// DeployRequest is what the caller of DeployModel chooses.
type DeployRequest struct {
	// Deployer is the name of the deployer to use.
	Deployer string
	// Options are passed to the deployer as they are.
	Options map[string]any
}

// DeployModel deploys a trained model tm through the deployer named by in: it fetches the model's files from the sidecar (f) into
// its directory under dir (see ModelDir), calls the deployer with it and waits for it, and records the deployment
// (gb_trained_model_deployments) with its outcome, a failure included. It returns the stored record, whose status is
// "completed". Errors are *Error: KindInvalid for an unknown deployer or bad options, KindUpstream when fetching the files or
// the deployer fails (the deployment is recorded as failed), KindInternal. A system event training.model_deploy.completed or
// .failed is emitted at the end.
func DeployModel(ctx context.Context, db *sql.DB, f ModelFileFetcher, deployers []TrainedModelDeployer, dir string, tm *trainingdb.TrainedModel, in DeployRequest) (*trainingdb.TrainedModelDeployment, error) {
	exp, err := findDeployer(deployers, in.Deployer)
	if err != nil {
		return nil, err
	}
	optionsJSON := json.RawMessage("{}")
	if in.Options != nil {
		b, err := json.Marshal(in.Options)
		if err != nil || len(b) > MaxDeployOptionsBytes {
			return nil, invalidErr(fmt.Sprintf("options must be a JSON object of at most %d bytes", MaxDeployOptionsBytes))
		}
		optionsJSON = b
	}
	modelDir, err := ModelDir(dir, tm)
	if err != nil {
		return nil, invalidErr("the model cannot be deployed: " + err.Error())
	}

	dbCtx := context.WithoutCancel(ctx) // the writes must outlive the request
	l := log.WithFields(log.Fields{"model_id": tm.ID, "deployer": exp.Name})
	id, err := trainingdb.RecordTrainedModelDeployment(dbCtx, db, trainingdb.TrainedModelDeployment{TrainedModelID: tm.ID, Deployer: exp.Name, Options: optionsJSON})
	if err != nil {
		l.WithError(err).Error("training: recording the deployment failed")
		return nil, internalErr("failed to record the deployment", err)
	}
	l = l.WithField("deploy_id", id)
	updates := &deployUpdates{}
	liveDeployments.Store(id, updates)
	defer func() {
		updates.close()
		liveDeployments.Delete(id)
	}()
	started := time.Now()
	fail := func(msg string, causeErr error) error {
		updates.add("Failed: " + msg)
		l.WithError(causeErr).Error("training: model deploy failed")
		ms := time.Since(started).Milliseconds()
		if err := trainingdb.FinishTrainedModelDeployment(dbCtx, db, id, trainingdb.TrainedModelDeploymentFailed, "", "", msg, ms); err != nil {
			l.WithError(err).Error("training: updating the deployment failed")
		}
		monitoring.EmitSystemEvent(dbCtx, "training.model_deploy.failed", monitoring.Event{
			"model_id": tm.ID, "deploy_id": id, "deployer": exp.Name, "duration_ms": ms, "error": msg})
		return &Error{KindUpstream, fmt.Sprintf("deploy %d failed: %s", id, msg), causeErr}
	}

	updates.add(fmt.Sprintf("Fetching the model's %d files from the sidecar", len(tm.Files)))
	fctx, cancel := context.WithTimeout(ctx, FetchModelTimeout)
	err = FetchModelFiles(fctx, f, tm, modelDir)
	cancel()
	if err != nil {
		return nil, fail("fetching the model's files failed: "+err.Error(), err)
	}
	if err := trainingdb.SetTrainedModelDeployDir(dbCtx, db, id, modelDir); err != nil {
		l.WithError(err).Error("training: updating the deployment failed")
	}

	updates.add(fmt.Sprintf("Files ready, running the %s deployer", exp.Name))
	res, err := callDeployer(ctx, exp, DeployTrainedModelRequest{DeployID: id, Model: *tm, Dir: modelDir, Options: in.Options, Update: updates.add})
	if err != nil {
		return nil, fail(err.Error(), err)
	}
	details := "{}"
	if res.Details != nil {
		b, err := json.Marshal(res.Details)
		if err != nil {
			return nil, fail("the deployer's details are not JSON-encodable: "+err.Error(), err)
		}
		details = string(b)
	}
	ms := time.Since(started).Milliseconds()
	updates.add("Completed")
	if err := trainingdb.FinishTrainedModelDeployment(dbCtx, db, id, trainingdb.TrainedModelDeploymentCompleted, res.Ref, details, "", ms); err != nil {
		l.WithError(err).Error("training: updating the deployment failed")
		return nil, internalErr("the deployment completed but failed to record it", err)
	}
	monitoring.EmitSystemEvent(dbCtx, "training.model_deploy.completed", monitoring.Event{
		"model_id": tm.ID, "deploy_id": id, "deployer": exp.Name, "duration_ms": ms})
	l.WithFields(log.Fields{"ref": res.Ref, "duration_ms": ms}).Info("training: trained model deployed")
	out, err := trainingdb.GetTrainedModelDeployment(dbCtx, db, id)
	if err != nil {
		return nil, internalErr("failed to read the deployment", err)
	}
	out.Updates = updates.snapshot()
	return out, nil
}

func findDeployer(deployers []TrainedModelDeployer, name string) (TrainedModelDeployer, error) {
	if len(deployers) == 0 {
		return TrainedModelDeployer{}, invalidErr("no trained model deployer is registered")
	}
	names := make([]string, 0, len(deployers))
	for _, e := range deployers {
		if e.Name == name {
			return e, nil
		}
		names = append(names, e.Name)
	}
	if name == "" {
		return TrainedModelDeployer{}, invalidErr("deployer is required: one of " + strings.Join(names, ", "))
	}
	return TrainedModelDeployer{}, invalidErr(fmt.Sprintf("unknown deployer %q: one of %s", name, strings.Join(names, ", ")))
}

// callDeployer runs the deployer; a panic is returned as an error.
func callDeployer(ctx context.Context, exp TrainedModelDeployer, req DeployTrainedModelRequest) (res DeployTrainedModelResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("deployer %q panicked: %v", exp.Name, r)
		}
	}()
	res, err = exp.Deploy(ctx, req)
	if err != nil {
		err = fmt.Errorf("deployer %q failed: %w", exp.Name, err)
	}
	return res, err
}

// ListDeployments reads the deployments of trained model modelID, newest first, at most limit: a KindInternal Error if it cannot.
func ListDeployments(ctx context.Context, db *sql.DB, modelID int64, limit int) ([]trainingdb.TrainedModelDeployment, error) {
	list, err := trainingdb.ListTrainedModelDeployments(ctx, db, modelID, limit)
	if err != nil {
		log.WithError(err).Error("training: list trained model deployments failed")
		return nil, internalErr("failed to list the deployments", err)
	}
	for i := range list {
		withLiveUpdates(&list[i])
	}
	return list, nil
}

var _ ModelFileFetcher = (*sidecar.Client)(nil)
