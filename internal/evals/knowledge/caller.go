package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/atif"
	"github.com/deepblue-phoenix/gligabao/internal/atif/atifstore"
)

// callTimeout bounds one call of the eval endpoints: a chunked text is extracted chunk by chunk in one request, so it can take long.
const callTimeout = 30 * time.Minute

// Caller makes the calls of a case to the eval endpoints (POST /v1/gb/eval/remember and /v1/gb/eval/query) as the owner of the eval.
// It never opens a listener: the implementation is MuxCaller, over a private mux that is not mounted on the public one.
type Caller interface {
	// Do sends body as JSON and returns the status and the body of the answer.
	Do(ctx context.Context,
		owner,
		method,
		path string,
		body any) (status int, resp []byte, err error)
}

// MuxCaller is a Caller over a *http.ServeMux that it calls in process (httptest.NewRecorder): the handlers act as the owner, who is
// set in the gb-user-id header.
type MuxCaller struct {
	Mux *http.ServeMux
}

// Do implements Caller.
func (c MuxCaller) Do(ctx context.Context,
	owner,
	method,
	path string,
	body any) (int, []byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx,
		method,
		path,
		bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("gb-user-id", owner) // the handlers take the user from this header only
	rec := httptest.NewRecorder()
	c.Mux.ServeHTTP(rec, req)
	data, err := io.ReadAll(rec.Result().Body)
	return rec.Code, data, err
}

// post sends body as JSON to path as owner and decodes the reply into out.
func post(ctx context.Context,
	c Caller,
	owner,
	path string,
	body,
	out any) error {
	status, data, err := c.Do(ctx,
		owner,
		http.MethodPost,
		path,
		body)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		msg := strings.TrimSpace(string(data))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			msg = e.Error // the server's reason, without the JSON around it
		}
		return fmt.Errorf("status %d: %s",
			status,
			msg)
	}
	return json.Unmarshal(data, out)
}

// locStore wraps a trajectory store and remembers where each trajectory went, so the case run can reference it.
type locStore struct {
	inner atifstore.Store

	mu   sync.Mutex
	locs map[string]string // trajectory id -> location
}

func newLocStore(inner atifstore.Store) *locStore {
	return &locStore{
		inner: inner,
		locs:  map[string]string{},
	}
}

// Put implements atifstore.Store.
func (s *locStore) Put(ctx context.Context,
	user string,
	t *atif.Trajectory) (string, error) {
	loc, err := s.inner.Put(ctx,
		user,
		t)
	if err == nil {
		s.mu.Lock()
		s.locs[t.TrajectoryID] = loc
		s.mu.Unlock()
	}
	return loc, err
}

// Read implements atifstore.Store.
func (s *locStore) Read(ctx context.Context,
	location string) ([]byte, error) {
	return s.inner.Read(ctx, location)
}

// snapshot is a copy of the locations stored so far.
func (s *locStore) snapshot() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.locs))
	for k, v := range s.locs {
		out[k] = v
	}
	return out
}
