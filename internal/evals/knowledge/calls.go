package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/query"
	"github.com/deepblue-phoenix/gligabao/internal/api/remember"
	"github.com/deepblue-phoenix/gligabao/internal/atif"
	"github.com/deepblue-phoenix/gligabao/internal/atif/atifstore"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_query"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

// callTimeout bounds one call of the pipeline: a chunked text is extracted chunk by chunk in one call, so it can take long.
const callTimeout = 30 * time.Minute

// callRemember remembers req as owner, with the same function the POST /v1/gb/remember handler runs (remember.Remember), so the
// request is validated and the options are assembled as for a call of the API. opts are the options of the calls (see api.RememberOptions).
func callRemember(ctx context.Context,
	mgr *memory_remember.Manager,
	d *db.DB,
	owner string,
	req remember.Request,
	opts []memory_core.Option) (*memory_remember.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	return remember.Remember(ctx,
		mgr,
		d.Relational,
		owner,
		req,
		opts)
}

// callQuery asks req as owner, with the same function the POST /v1/gb/query handler runs (query.Run). opts are the options of the
// calls (see api.QueryOptions).
func callQuery(ctx context.Context,
	d *db.DB,
	llm memory_core.LLM,
	owner string,
	req query.Request,
	opts []memory_core.Option) (*memory_query.QueryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	return query.Run(ctx,
		d.Relational,
		llm,
		owner,
		req,
		opts)
}

// rememberRequest is the remember request of one text of a case: the dataset it goes into, the models and the reasoning levels of the
// case, and geoSync when the checks read the coordinates right after (the geo manager is waited for). A case that has timeseries
// gives them as JSON, in the form of the API.
func rememberRequest(m core.KnowledgeArtifact,
	dataset string,
	models memory_core.Models,
	reasoning memory_core.Reasoning,
	geoSync bool) (remember.Request, error) {
	var series *remember.TimeseriesRequest
	if len(m.Timeseries) > 0 {
		series = &remember.TimeseriesRequest{}
		if err := json.Unmarshal(m.Timeseries, series); err != nil {
			return remember.Request{}, fmt.Errorf("timeseries: %w", err)
		}
	}
	return remember.Request{
		Dataset:       dataset,
		Text:          m.Text,
		Kind:          m.Kind,
		ReferenceTime: m.ReferenceTime,
		Models:        models,
		Reasoning:     reasoning,
		WebSearch:     m.WebSearch,
		GeoSync:       geoSync,
		Timeseries:    series,
	}, nil
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
