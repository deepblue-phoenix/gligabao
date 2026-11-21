package usereval

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/evals/evalfile"
	"github.com/deepblue-phoenix/gligabao/internal/evals/userfiles"
)

// failingStore wraps a store and fails the calls named in fail.
type failingStore struct {
	userfiles.Store
	fail map[string]bool
}

func (f failingStore) Put(ctx context.Context,
	user,
	rel string,
	data []byte) error {
	if f.fail["put"] {
		return errors.New("s3 down")
	}
	return f.Store.Put(ctx, user, rel, data)
}

func (f failingStore) List(ctx context.Context,
	user,
	prefix string) ([]string, error) {
	if f.fail["list"] {
		return nil, errors.New("s3 down")
	}
	return f.Store.List(ctx, user, prefix)
}

type synced struct {
	calls []string
	err   error
}

func newEnv(t *testing.T) (Env, *synced) {
	t.Helper()
	s := &synced{}
	return Env{
		Store: userfiles.FS{Dir: t.TempDir()},
		Limits: evalfile.DefaultLimits([]string{"gpt-5"},
			nil,
			nil),
		Sync: func(_ context.Context,
			user string,
			ev evalfile.UserEval) error {
			s.calls = append(s.calls, user+"/"+ev.Name)
			return s.err
		},
	}, s
}

const evalBody = `{"name":"e","cases":[{"name":"c","remember":[{"text_file":"corpus/a.txt"}],"query":"q"}]}`
const evalBody2 = `{"name":"e","cases":[{"name":"c","query":"other"}]}`

func putCorpus(t *testing.T,
	env Env,
	user string) {
	t.Helper()
	if err := env.Store.Put(context.Background(), user, "corpus/a.txt", []byte("text")); err != nil {
		t.Fatal(err)
	}
}

func isInvalid(err error) *InvalidError {
	var ie *InvalidError
	if errors.As(err, &ie) {
		return ie
	}
	return nil
}

func TestSaveGetListDelete(t *testing.T) {
	ctx := context.Background()
	env, sync := newEnv(t)
	putCorpus(t, env, "alice")

	created, err := Save(ctx, env, "alice", "e", []byte(evalBody))
	if err != nil || !created {
		t.Fatalf("save = %v, %v", created, err)
	}
	created, err = Save(ctx, env, "alice", "e", []byte(evalBody2))
	if err != nil || created {
		t.Fatalf("replace = %v, %v", created, err)
	}
	if !slices.Equal(sync.calls, []string{"alice/e", "alice/e"}) {
		t.Fatalf("sync calls = %v", sync.calls)
	}
	got, err := Get(ctx, env, "alice", "e")
	if err != nil || string(got.JSON) != evalBody2 || len(got.Files) != 0 {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := Save(ctx, env, "alice", "e", []byte(evalBody)); err != nil {
		t.Fatal(err)
	}
	got, _ = Get(ctx, env, "alice", "e")
	if !slices.Equal(got.Files, []string{"corpus/a.txt"}) {
		t.Fatalf("files = %v", got.Files)
	}
	if names, err := List(ctx, env, "alice"); err != nil || !slices.Equal(names, []string{"e"}) {
		t.Fatalf("list = %v, %v", names, err)
	}

	// another user sees nothing
	if _, err := Get(ctx, env, "bob", "e"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob get: %v", err)
	}
	if err := Delete(ctx, env, "bob", "e", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob delete: %v", err)
	}
	if names, _ := List(ctx, env, "bob"); len(names) != 0 {
		t.Fatalf("bob list = %v", names)
	}

	// delete keeps the corpus unless asked
	if err := Delete(ctx, env, "alice", "e", false); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(ctx, env, "alice", "e"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
	if _, err := env.Store.Get(ctx, "alice", "corpus/a.txt"); err != nil {
		t.Fatalf("corpus gone: %v", err)
	}
	if err := Delete(ctx, env, "alice", "e", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if _, err := Save(ctx, env, "alice", "e", []byte(evalBody)); err != nil {
		t.Fatal(err)
	}
	if err := Delete(ctx, env, "alice", "e", true); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Store.Get(ctx, "alice", "corpus/a.txt"); !errors.Is(err, userfiles.ErrNotFound) {
		t.Fatalf("corpus kept: %v", err)
	}
}

func TestSaveRefusals(t *testing.T) {
	ctx := context.Background()
	env, sync := newEnv(t)
	for _, u := range []string{"", " ", "system"} {
		if _, err := Save(ctx, env, u, "e", []byte(evalBody2)); !errors.Is(err, ErrInvalidUser) {
			t.Errorf("user %q: %v", u, err)
		}
		if _, err := Get(ctx, env, u, "e"); !errors.Is(err, ErrInvalidUser) {
			t.Errorf("get user %q: %v", u, err)
		}
	}
	// invalid content: nothing written, nothing synced
	if _, err := Save(ctx, env, "a", "e", []byte(`{"name":"x","cases":[]}`)); isInvalid(err) == nil {
		t.Errorf("invalid eval accepted: %v", err)
	}
	// the corpus file is missing
	_, err := Save(ctx, env, "a", "e", []byte(evalBody))
	if ie := isInvalid(err); ie == nil || !strings.Contains(string(ie.Problems[0]), "corpus/a.txt") {
		t.Errorf("missing corpus: %v", err)
	}
	// a file of another user does not count
	putCorpus(t, env, "other")
	if _, err := Save(ctx, env, "a", "e", []byte(evalBody)); isInvalid(err) == nil {
		t.Errorf("another user's corpus used: %v", err)
	}
	// too large
	small := env
	small.Limits.MaxFileBytes = 10
	if _, err := Save(ctx, small, "a", "e", []byte(evalBody2)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too large: %v", err)
	}
	if len(sync.calls) != 0 {
		t.Errorf("synced: %v", sync.calls)
	}
	if names, _ := List(ctx, env, "a"); len(names) != 0 {
		t.Errorf("something was written: %v", names)
	}
}

func TestSaveSyncFailureUndoesTheWrite(t *testing.T) {
	ctx := context.Background()
	env, sync := newEnv(t)
	sync.err = errors.New("db down")

	// new eval: the JSON is removed again
	if _, err := Save(ctx, env, "a", "e", []byte(evalBody2)); !errors.Is(err, ErrSync) {
		t.Fatalf("err = %v", err)
	}
	if _, err := Get(ctx, env, "a", "e"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the new JSON stayed: %v", err)
	}

	// replaced eval: the old JSON is back
	sync.err = nil
	if _, err := Save(ctx, env, "a", "e", []byte(evalBody2)); err != nil {
		t.Fatal(err)
	}
	sync.err = errors.New("db down")
	next := `{"name":"e","cases":[{"name":"z","query":"changed"}]}`
	if _, err := Save(ctx, env, "a", "e", []byte(next)); !errors.Is(err, ErrSync) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := Get(ctx, env, "a", "e"); string(got.JSON) != evalBody2 {
		t.Fatalf("the old JSON was not kept: %s", got.JSON)
	}
}

func TestStorageDown(t *testing.T) {
	ctx := context.Background()
	env, sync := newEnv(t)
	env.Store = failingStore{Store: env.Store, fail: map[string]bool{"put": true}}
	if _, err := Save(ctx, env, "a", "e", []byte(evalBody2)); !errors.Is(err, ErrStorage) {
		t.Fatalf("put down: %v", err)
	}
	env.Store = failingStore{Store: env.Store.(failingStore).Store, fail: map[string]bool{"list": true}}
	if _, err := Save(ctx, env, "a", "e", []byte(evalBody)); !errors.Is(err, ErrStorage) {
		t.Fatalf("list down: %v", err)
	}
	if len(sync.calls) != 0 {
		t.Fatalf("synced: %v", sync.calls)
	}
}

func TestPutAndDeleteFile(t *testing.T) {
	ctx := context.Background()
	env, _ := newEnv(t)
	env.MaxFiles = 2
	env.MaxFileBytes = 8

	if err := PutFile(ctx, env, "a", "e", "corpus/a.txt", "text/plain", []byte("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such eval: %v", err)
	}
	if _, err := Save(ctx, env, "a", "e", []byte(evalBody2)); err != nil {
		t.Fatal(err)
	}
	if err := PutFile(ctx, env, "a", "e", "corpus/a.txt", "text/plain; charset=utf-8", []byte("12345678")); err != nil {
		t.Fatal(err)
	}
	if err := PutFile(ctx, env, "a", "e", "corpus/a.txt", "application/json", []byte("{}")); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if err := PutFile(ctx, env, "a", "e", "b.txt", "text/plain", []byte("x")); err != nil {
		t.Fatal(err)
	}
	// the third file is over the limit; the eval JSON does not count
	if err := PutFile(ctx, env, "a", "e", "c.txt", "text/plain", []byte("x")); isInvalid(err) == nil {
		t.Fatalf("too many files: %v", err)
	}
	if err := PutFile(ctx, env, "a", "e", "b.txt", "text/plain", []byte("123456789")); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("too large: %v", err)
	}
	for _, rel := range []string{"../x.txt", "/x.txt", "e.json", "a//b.txt", "x y.txt"} {
		if err := PutFile(ctx, env, "a", "e", rel, "text/plain", []byte("x")); isInvalid(err) == nil {
			t.Errorf("path %q accepted: %v", rel, err)
		}
	}
	for _, ct := range []string{"", "image/png", "text/html", "bad/"} {
		if err := PutFile(ctx, env, "a", "e", "b.txt", ct, []byte("x")); isInvalid(err) == nil {
			t.Errorf("content type %q accepted: %v", ct, err)
		}
	}
	// the eval JSON cannot be overwritten through a file
	if err := PutFile(ctx, env, "a", "e", "e.json", "application/json", []byte("{}")); isInvalid(err) == nil {
		t.Errorf("eval file overwritten: %v", err)
	}
	// another user has no such eval
	if err := PutFile(ctx, env, "b", "e", "x.txt", "text/plain", []byte("x")); !errors.Is(err, ErrNotFound) {
		t.Errorf("other user: %v", err)
	}
	if err := DeleteFile(ctx, env, "b", "e", "b.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("other user delete: %v", err)
	}

	if text, err := TextOf(ctx, env, "a", "b.txt"); err != nil || text != "x" {
		t.Fatalf("TextOf = %q, %v", text, err)
	}
	if _, err := TextOf(ctx, env, "b", "b.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("TextOf of another user: %v", err)
	}
	if err := DeleteFile(ctx, env, "a", "e", "b.txt"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteFile(ctx, env, "a", "e", "b.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if err := DeleteFile(ctx, env, "a", "e", "../b.txt"); isInvalid(err) == nil {
		t.Fatalf("bad path: %v", err)
	}
}

func TestPrepareRechecksAndSyncs(t *testing.T) {
	ctx := context.Background()
	env, sync := newEnv(t)
	putCorpus(t, env, "a")
	if _, err := Save(ctx, env, "a", "e", []byte(evalBody)); err != nil {
		t.Fatal(err)
	}
	sync.calls = nil
	ev, err := Prepare(ctx, env, "a", "e")
	if err != nil || ev.Name != "e" || len(ev.Cases) != 1 || !slices.Equal(sync.calls, []string{"a/e"}) {
		t.Fatalf("prepare = %+v, %v, %v", ev, err, sync.calls)
	}
	// the corpus file disappears: the run does not start
	if err := env.Store.Delete(ctx, "a", "corpus/a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(ctx, env, "a", "e"); isInvalid(err) == nil {
		t.Fatalf("missing corpus at run start: %v", err)
	}
	if _, err := Prepare(ctx, env, "b", "e"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user: %v", err)
	}
	putCorpus(t, env, "a")
	sync.err = errors.New("db down")
	if _, err := Prepare(ctx, env, "a", "e"); !errors.Is(err, ErrSync) {
		t.Fatalf("sync failure: %v", err)
	}
}
