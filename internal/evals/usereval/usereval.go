// Package usereval is the logic of the user evals as library functions (the HTTP routes of step 5 only call them): saving an eval
// (validate, write the JSON to the user's files, sync the def into the database), reading and deleting it, uploading and deleting
// its corpus files, and preparing it for a run. The JSON in userfiles is the source of truth; the database copy is made by Env.Sync.
//
// Every function acts as an owner, the user id, which must not be empty or core.SystemUser (reserved for the training evals).
package usereval

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"slices"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/evalfile"
	"github.com/deepblue-phoenix/gligabao/internal/evals/userfiles"
)

// DefaultMaxFiles is the number of corpus files a user may keep.
const DefaultMaxFiles = 50

// SyncFunc puts the def of the eval in the database for the owner (runner.SyncEval, with the spec of knowledge.Spec).
type SyncFunc func(ctx context.Context,
	userID string,
	ev evalfile.UserEval) error

// Env is what the functions work with.
type Env struct {
	Store  userfiles.Store
	Limits evalfile.Limits
	// MaxFiles is the number of corpus files of a user (DefaultMaxFiles if 0): the files of the user that are not an eval file.
	MaxFiles int
	// MaxFileBytes is the size of a corpus file (Limits.MaxTextBytes if 0).
	MaxFileBytes int
	// Sync is called once the JSON is stored; nil: no database to sync.
	Sync SyncFunc
}

// Stored is an eval as it is stored.
type Stored struct {
	Name string
	// JSON is the stored file, as it was written.
	JSON []byte
	// Files are the corpus files its cases name, sorted.
	Files []string
}

// InvalidError is the error of an eval (or file) that is not valid; the HTTP answer is 400 {"error":"invalid eval","problems":[...]}.
type InvalidError struct {
	Problems []evalfile.Problem
}

// Error implements error.
func (e *InvalidError) Error() string {
	ps := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		ps[i] = string(p)
	}
	return "invalid eval: " + strings.Join(ps, "; ")
}

var (
	// ErrInvalidUser is the error of an empty user id or of core.SystemUser (HTTP 400).
	ErrInvalidUser = errors.New("usereval: invalid user")
	// ErrNotFound is the error of an unknown eval or file (HTTP 404).
	ErrNotFound = errors.New("usereval: not found")
	// ErrTooLarge is the error of a body over the limit (HTTP 413).
	ErrTooLarge = errors.New("usereval: too large")
	// ErrStorage is the error of an unavailable file store (HTTP 503).
	ErrStorage = errors.New("usereval: eval storage unavailable")
	// ErrSync is the error of the database sync (HTTP 500).
	ErrSync = errors.New("usereval: could not save the eval")
)

// allowedContentTypes are the media types of a corpus file.
var allowedContentTypes = []string{"text/plain", "application/json"}

// fileName is the file of an eval.
func fileName(name string) string { return name + ".json" }

// checkUser refuses the users that cannot own evals.
func checkUser(user string) error {
	if strings.TrimSpace(user) == "" || user == core.SystemUser {
		return ErrInvalidUser
	}
	return nil
}

// storageErr turns an error of the store into the error of this package: a bad path is invalid input, a missing file is ErrNotFound,
// anything else is ErrStorage.
func storageErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, userfiles.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, userfiles.ErrBadPath):
		return &InvalidError{Problems: []evalfile.Problem{evalfile.Problem(err.Error())}}
	default:
		return fmt.Errorf("%w: %v", ErrStorage, err)
	}
}

// isEvalFile says whether a relative path of the user's files is an eval file (not a corpus file).
func isEvalFile(rel string) bool {
	return !strings.Contains(rel, "/") && strings.HasSuffix(rel, ".json")
}

// missingFiles returns the problems for the files of paths that the user does not have.
func missingFiles(ctx context.Context,
	env Env,
	user string,
	paths []string) ([]evalfile.Problem, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	have, err := env.Store.List(ctx, user, "")
	if err != nil {
		return nil, storageErr(err)
	}
	var out []evalfile.Problem
	for _, p := range paths {
		if !slices.Contains(have, p) {
			out = append(out, evalfile.Problem(fmt.Sprintf("text_file %q does not exist (upload it with PUT files/%s)", p, p)))
		}
	}
	return out, nil
}

// validate parses and checks the JSON of the eval name, including that its text files exist.
func validate(ctx context.Context,
	env Env,
	user,
	name string,
	data []byte) (evalfile.UserEval, error) {
	if problems := evalfile.ValidateUser(data, name, env.Limits); len(problems) > 0 {
		return evalfile.UserEval{}, &InvalidError{Problems: problems}
	}
	ev, err := evalfile.ParseUser(data)
	if err != nil {
		return evalfile.UserEval{}, &InvalidError{Problems: []evalfile.Problem{evalfile.Problem(err.Error())}}
	}
	missing, err := missingFiles(ctx,
		env,
		user,
		evalfile.TextFiles(ev))
	if err != nil {
		return evalfile.UserEval{}, err
	}
	if len(missing) > 0 {
		return evalfile.UserEval{}, &InvalidError{Problems: missing}
	}
	return ev, nil
}

// Save stores the eval name of the user: it validates data (ValidateUser, and that its text files exist), writes the JSON, then syncs
// the def (Env.Sync). If the sync fails the write is undone (the new JSON is deleted, or the old one put back) and ErrSync is returned.
// created tells a new eval from a replaced one. Errors: ErrInvalidUser, ErrTooLarge, *InvalidError, ErrStorage, ErrSync.
func Save(ctx context.Context,
	env Env,
	user,
	name string,
	data []byte) (created bool, err error) {
	if err := checkUser(user); err != nil {
		return false, err
	}
	if env.Limits.MaxFileBytes > 0 && len(data) > env.Limits.MaxFileBytes {
		return false, ErrTooLarge
	}
	ev, err := validate(ctx,
		env,
		user,
		name,
		data)
	if err != nil {
		return false, err
	}
	old, getErr := env.Store.Get(ctx, user, fileName(name))
	if getErr != nil && !errors.Is(getErr, userfiles.ErrNotFound) {
		return false, storageErr(getErr)
	}
	created = getErr != nil
	if err := env.Store.Put(ctx, user, fileName(name), data); err != nil {
		return false, storageErr(err)
	}
	if env.Sync == nil {
		return created, nil
	}
	if syncErr := env.Sync(ctx, user, ev); syncErr != nil {
		// undo with a context that outlives a cancelled request
		undoCtx := context.WithoutCancel(ctx)
		if created {
			_ = env.Store.Delete(undoCtx, user, fileName(name))
		} else {
			_ = env.Store.Put(undoCtx, user, fileName(name), old)
		}
		return false, fmt.Errorf("%w: %v", ErrSync, syncErr)
	}
	return created, nil
}

// Get returns the stored eval name of the user and the corpus files it names. ErrNotFound if there is none.
func Get(ctx context.Context,
	env Env,
	user,
	name string) (Stored, error) {
	if err := checkUser(user); err != nil {
		return Stored{}, err
	}
	if !evalfile.ValidName(name) {
		return Stored{}, ErrNotFound
	}
	data, err := env.Store.Get(ctx, user, fileName(name))
	if err != nil {
		return Stored{}, storageErr(err)
	}
	files := []string{}
	if ev, parseErr := evalfile.ParseUser(data); parseErr == nil {
		files = evalfile.TextFiles(ev)
	}
	return Stored{
		Name:  name,
		JSON:  data,
		Files: files,
	}, nil
}

// List returns the names of the evals of the user, sorted.
func List(ctx context.Context,
	env Env,
	user string) ([]string, error) {
	if err := checkUser(user); err != nil {
		return nil, err
	}
	all, err := env.Store.List(ctx, user, "")
	if err != nil {
		return nil, storageErr(err)
	}
	names := []string{}
	for _, rel := range all {
		if isEvalFile(rel) {
			names = append(names, strings.TrimSuffix(rel, ".json"))
		}
	}
	return names, nil
}

// Delete removes the JSON of the eval name of the user, and with withFiles also the corpus files its cases name. The database side
// (runs, datasets, def) is the caller's, done before. ErrNotFound if there is no such eval; the files are removed first, so a failure
// half-way can be repeated.
func Delete(ctx context.Context,
	env Env,
	user,
	name string,
	withFiles bool) error {
	stored, err := Get(ctx,
		env,
		user,
		name)
	if err != nil {
		return err
	}
	if withFiles {
		for _, f := range stored.Files {
			if err := env.Store.Delete(ctx, user, f); err != nil && !errors.Is(err, userfiles.ErrNotFound) {
				return storageErr(err)
			}
		}
	}
	return storageErr(env.Store.Delete(ctx, user, fileName(name)))
}

// checkFilePath validates the relative path of a corpus file: a valid text_file path.
func checkFilePath(rel string) error {
	if !evalfile.ValidTextFile(rel) {
		return &InvalidError{Problems: []evalfile.Problem{evalfile.Problem(fmt.Sprintf(
			"%q is not a valid file path (letters, digits, . _ -, segments separated by /, no \"..\", not an eval file)", rel))}}
	}
	return nil
}

// PutFile stores a corpus file for the eval name (which must exist: ErrNotFound): rel is the path relative to the directory of the
// user, the same as a text_file. contentType must be text/plain or application/json. Errors: ErrTooLarge (over the file size limit),
// *InvalidError (bad path or content type, or the user already has MaxFiles files), ErrNotFound, ErrStorage.
func PutFile(ctx context.Context,
	env Env,
	user,
	name,
	rel,
	contentType string,
	data []byte) error {
	if _, err := Get(ctx,
		env,
		user,
		name); err != nil {
		return err
	}
	if err := checkFilePath(rel); err != nil {
		return err
	}
	if mt, _, err := mime.ParseMediaType(contentType); err != nil || !slices.Contains(allowedContentTypes, mt) {
		return &InvalidError{Problems: []evalfile.Problem{evalfile.Problem(fmt.Sprintf(
			"content type %q is not allowed (use %s)", contentType, strings.Join(allowedContentTypes, " or ")))}}
	}
	maxBytes := env.MaxFileBytes
	if maxBytes == 0 {
		maxBytes = env.Limits.MaxTextBytes
	}
	if maxBytes > 0 && len(data) > maxBytes {
		return ErrTooLarge
	}
	have, err := env.Store.List(ctx, user, "")
	if err != nil {
		return storageErr(err)
	}
	corpus := slices.DeleteFunc(slices.Clone(have), isEvalFile)
	maxFiles := env.MaxFiles
	if maxFiles == 0 {
		maxFiles = DefaultMaxFiles
	}
	if !slices.Contains(corpus, rel) && len(corpus) >= maxFiles {
		return &InvalidError{Problems: []evalfile.Problem{evalfile.Problem(fmt.Sprintf("too many files: the limit is %d", maxFiles))}}
	}
	return storageErr(env.Store.Put(ctx, user, rel, data))
}

// DeleteFile removes a corpus file (the eval name must exist: ErrNotFound; so must the file). An eval that still names it will fail
// its next save or run: that is reported then.
func DeleteFile(ctx context.Context,
	env Env,
	user,
	name,
	rel string) error {
	if _, err := Get(ctx,
		env,
		user,
		name); err != nil {
		return err
	}
	if err := checkFilePath(rel); err != nil {
		return err
	}
	return storageErr(env.Store.Delete(ctx, user, rel))
}

// Prepare is what a run does at its start: it re-reads the JSON of the eval from the store, validates it again (its text files must
// still exist) and syncs the def, so the database copy never has to be trusted. It returns the eval as parsed. Errors: ErrNotFound,
// *InvalidError, ErrStorage, ErrSync.
func Prepare(ctx context.Context,
	env Env,
	user,
	name string) (evalfile.UserEval, error) {
	stored, err := Get(ctx,
		env,
		user,
		name)
	if err != nil {
		return evalfile.UserEval{}, err
	}
	ev, err := validate(ctx,
		env,
		user,
		name,
		stored.JSON)
	if err != nil {
		return evalfile.UserEval{}, err
	}
	if env.Sync != nil {
		if err := env.Sync(ctx, user, ev); err != nil {
			return evalfile.UserEval{}, fmt.Errorf("%w: %v", ErrSync, err)
		}
	}
	return ev, nil
}

// TextOf reads the text of a corpus file of the user (what a run feeds to remember). ErrNotFound if it is gone.
func TextOf(ctx context.Context,
	env Env,
	user,
	rel string) (string, error) {
	if err := checkUser(user); err != nil {
		return "", err
	}
	if err := checkFilePath(rel); err != nil {
		return "", err
	}
	data, err := env.Store.Get(ctx, user, rel)
	return string(data), storageErr(err)
}
