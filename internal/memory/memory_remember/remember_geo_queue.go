package memory_remember

// The queued side of the geo stage (see remember_geo.go): when the call has the geo manager (memory_core.WithGeoResolver with a
// memory_geo.Queue) or is deferred (memory_core.WithGeoDeferred), the stage does not ask the resolver. It records each place the LLM
// found in gb_geo_pending and, with a manager, queues it; the manager asks the resolver in its own goroutine and its Done function
// (geoJob) stores the coordinates and removes the pending record. The call only waits for that with memory_core.WithGeoSync.
//
// The pending records are what makes the work durable: a place whose resolving was lost (a restart, a full queue, a resolver that
// failed, a worker process that has no manager) is found by the sweeper (RunGeoSweeper) of the server, which queues it again.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_geo"

	log "github.com/sirupsen/logrus"
)

const (
	// maxGeoAttempts is how many times the resolver may fail for a pending place before the sweeper gives up on it (a new mention of the
	// entity starts the attempts again).
	maxGeoAttempts = 5
	// geoSweepInterval is how often the sweeper looks for pending places, and geoSweepBatch how many it queues at most each time.
	geoSweepInterval = 2 * time.Minute
	geoSweepBatch    = 500
)

// geoSlot keeps the outcome of one queued job for the call that waits for it (WithGeoSync).
type geoSlot struct {
	mu       sync.Mutex
	done     bool
	outcome  memory_geo.Outcome
	storeErr error
}

func (s *geoSlot) set(o memory_geo.Outcome,
	storeErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done, s.outcome, s.storeErr = true, o, storeErr
}

func (s *geoSlot) get() (memory_geo.Outcome, error, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outcome, s.storeErr, s.done
}

//---------------------------------------------------------------------------------------------

// geoResolverName is the name of the resolver of the call, "deferred" without one.
func geoResolverName(co memory_core.CallOptions) string {
	if co.GeoResolver == nil {
		return "deferred"
	}
	return co.GeoResolver.Name()
}

// queueGeo records the places as pending and, with a manager, queues them; with WithGeoSync it then waits for them. It returns one
// result per place: Pending while nothing is known, or what the manager found (WithGeoSync).
func (j *job) queueGeo(ctx context.Context,
	places []geoPlace,
	resolverName string,
	fields log.Fields) []GeoResult {
	results := make([]GeoResult,
		len(places))
	pend := make([]graph.NewGeoPending,
		0,
		len(places))
	for i, p := range places {
		results[i] = GeoResult{
			Entity:     p.Entity.Name,
			EntityType: p.Entity.Type,
			EntityID:   p.ID,
			Kind:       p.Kind,
			Query:      p.Query,
			Pending:    p.ID != 0,
		}
		if p.ID == 0 {
			results[i].Error = "store: entity not found"
			continue
		}
		pend = append(pend, graph.NewGeoPending{
			EntityID: p.ID,
			Kind:     p.Kind,
			Query:    p.Query,
			Now:      j.now,
		})
	}
	if err := j.storePending(ctx, pend); err != nil {
		log.WithError(err).WithFields(fields).Warn("remember: recording the pending places failed")
		for i := range results {
			if results[i].Pending {
				results[i].Pending, results[i].Error = false, "pending: "+err.Error()
			}
		}
		j.run.Output(StageResolveGeo,
			"queue_geo",
			"",
			map[string]any{"places": len(pend), "error": err.Error()})
		return results
	}

	slots := make([]*geoSlot,
		len(places))
	queued, duplicates := 0, 0
	for i, p := range places {
		if !results[i].Pending || j.co.GeoQueue == nil { // deferred: the sweeper of the server queues it
			continue
		}
		slot := &geoSlot{}
		err := j.co.GeoQueue.Enqueue(geoJob(j.d,
			p.ID,
			p.Kind,
			p.Query,
			resolverName,
			slot.set))
		switch {
		case err == nil:
			slots[i] = slot
			queued++
		case errors.Is(err, memory_geo.ErrDuplicate): // another call or the sweeper has it in the queue already
			duplicates++
		case errors.Is(err, memory_geo.ErrQueueFull): // stays pending: the sweeper queues it later
			log.WithFields(fields).WithField("entity", p.Entity.Name).Warn("remember: the geo queue is full, the place stays pending")
		default:
			results[i].Pending, results[i].Error = false, "queue: "+err.Error()
		}
	}
	mode := "async"
	if j.co.GeoQueue == nil {
		mode = "deferred"
	} else if j.co.GeoSync {
		mode = "sync"
	}
	j.tr.Tool("The places are recorded as pending and handed to the geo manager, which resolves them in its own goroutine; this call does not wait for the resolver (mode: "+mode+").",
		"queue_geo",
		map[string]any{"places": len(places), "mode": mode, "resolver": resolverName},
		map[string]any{"pending": len(pend), "queued": queued, "already_queued": duplicates})
	j.run.Output(StageResolveGeo,
		"queue_geo",
		"",
		map[string]any{"places": len(places), "mode": mode, "pending": len(pend), "queued": queued, "already_queued": duplicates})
	if j.co.GeoQueue == nil || !j.co.GeoSync {
		return results
	}

	//--------------------------------------
	// SYNC: wait for the manager to be done with everything queued before this point, then read what it did
	flushErr := j.co.GeoQueue.Flush(ctx)
	if flushErr != nil {
		log.WithError(flushErr).WithFields(fields).Warn("remember: waiting for the geo manager failed")
	}
	for i, p := range places {
		if !results[i].Pending {
			continue
		}
		o, storeErr, ok := slots[i].getOrZero()
		if ok {
			r, _ := j.recordGeoOutcome(p,
				resolverName,
				o,
				fields)
			if storeErr != nil && r.Resolved {
				r.Resolved, r.Error = false, "store: "+storeErr.Error()
			}
			if o.Err != nil && !errors.Is(o.Err, memory_geo.ErrNotFound) {
				r.Pending = true // the resolver failed: the sweeper asks again
			}
			results[i] = r
			continue
		}
		// not queued by this call (another call has it, or the queue was full), or the wait failed: what is stored is the answer
		if g, err := graph.GetEntityGeo(ctx,
			j.d,
			p.ID); err == nil {
			results[i].Pending, results[i].Resolved, results[i].Latitude, results[i].Longitude = false, true, g.Latitude, g.Longitude
			results[i].DisplayName, results[i].Country, results[i].CountryCode, results[i].Region = g.DisplayName, g.Country, g.CountryCode, g.Region
			results[i].Provider = g.Provider
		} else if flushErr != nil {
			results[i].Error = "flush: " + flushErr.Error()
		}
	}
	return results
}

func (s *geoSlot) getOrZero() (memory_geo.Outcome, error, bool) {
	if s == nil {
		return memory_geo.Outcome{}, nil, false
	}
	return s.get()
}

// recordGeoOutcome files the answer of the resolver for the place in the trajectory and the run log, and returns the result of the
// place and, if it was found, the metadata to store (nil if not found, or if the entity has no ID).
func (j *job) recordGeoOutcome(p geoPlace,
	resolverName string,
	o memory_geo.Outcome,
	fields log.Fields) (GeoResult, *graph.NewEntityGeo) {
	r := GeoResult{
		Entity:     p.Entity.Name,
		EntityType: p.Entity.Type,
		EntityID:   p.ID,
		Kind:       p.Kind,
		Query:      p.Query,
	}
	args := map[string]any{"entity": p.Entity.Name, "type": p.Entity.Type, "kind": p.Kind, "query": p.Query, "resolver": resolverName}
	timing := memory_core.ToolTiming{
		Provider: resolverName,
		Started:  o.Started,
		Latency:  o.Latency,
	}
	place, err := o.Place, o.Err
	switch {
	case errors.Is(err, memory_geo.ErrNotFound):
		log.WithFields(fields).WithField("entity", p.Entity.Name).Info("remember: the geo resolver does not know the place")
		j.tr.TimedTool("The geo resolver does not know the place.",
			"resolve_geo",
			args,
			map[string]any{"found": false},
			timing)
		j.run.Output(StageResolveGeo,
			"resolve_geo",
			p.Entity.Name,
			map[string]any{"request": args, "found": false})
		return r, nil
	case err != nil:
		r.Error = "resolve: " + err.Error()
		log.WithError(err).WithFields(fields).WithField("entity", p.Entity.Name).Warn("remember: the geo resolver failed")
		j.tr.TimedTool("The geo resolver failed.",
			"resolve_geo",
			args,
			map[string]any{"error": r.Error},
			timing)
		j.run.Output(StageResolveGeo,
			"resolve_geo",
			p.Entity.Name,
			map[string]any{"request": args, "error": r.Error})
		return r, nil
	}
	r.Resolved, r.Latitude, r.Longitude, r.DisplayName = true, place.Latitude, place.Longitude, place.DisplayName
	r.Country, r.CountryCode, r.Region, r.Provider = place.Country, place.CountryCode, place.Region, place.Provider
	if r.Provider == "" {
		r.Provider = resolverName
	}
	j.tr.TimedTool("The geo resolver found the place.",
		"resolve_geo",
		args,
		map[string]any{"found": true, "latitude": place.Latitude,
			"longitude": place.Longitude, "display_name": place.DisplayName, "country": place.Country},
		timing)
	j.run.Output(StageResolveGeo,
		"resolve_geo",
		p.Entity.Name,
		map[string]any{"request": args, "found": true, "place": map[string]any{"latitude": place.Latitude, "longitude": place.Longitude,
			"kind": place.Kind, "display_name": place.DisplayName, "country": place.Country, "country_code": place.CountryCode,
			"region": place.Region, "type": place.Type, "bbox": place.BBox, "importance": place.Importance, "provider": r.Provider, "ref": place.Ref}})
	if p.ID == 0 {
		return r, nil
	}
	up := newEntityGeo(p.ID,
		p.Kind,
		p.Query,
		place,
		r.Provider,
		j.now)
	return r, &up
}

// newEntityGeo is the geo metadata to store for the place the resolver found for the entity.
func newEntityGeo(entityID int64,
	kind,
	query string,
	place *memory_geo.Place,
	provider string,
	now time.Time) graph.NewEntityGeo {
	return graph.NewEntityGeo{
		EntityID:    entityID,
		Kind:        kind,
		Query:       query,
		Latitude:    place.Latitude,
		Longitude:   place.Longitude,
		DisplayName: place.DisplayName,
		Country:     place.Country,
		CountryCode: place.CountryCode,
		Region:      place.Region,
		PlaceType:   place.Type,
		BBox:        place.BBox,
		Importance:  place.Importance,
		Provider:    provider,
		ProviderRef: place.Ref,
		Now:         now,
	}
}

// storePending records the places as pending, in one transaction.
func (j *job) storePending(ctx context.Context,
	pend []graph.NewGeoPending) error {
	if len(pend) == 0 {
		return nil
	}
	tx, err := graph.Begin(ctx, j.d)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range pend {
		if err := tx.UpsertGeoPending(ctx, p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

//---------------------------------------------------------------------------------------------

// geoJob is the job for the geo manager that resolves the pending place of an entity: its Done stores what the resolver said
// (persistGeoOutcome) and then calls after, if set, with the answer and the error of the store.
func geoJob(d *sql.DB,
	entityID int64,
	kind,
	query,
	provider string,
	after func(memory_geo.Outcome, error)) memory_geo.Job {
	return memory_geo.Job{
		Key: entityID,
		Query: memory_geo.Query{
			Text: query,
			Kind: kind,
		},
		Done: func(ctx context.Context,
			o memory_geo.Outcome) {
			err := persistGeoOutcome(ctx,
				d,
				entityID,
				kind,
				query,
				provider,
				o)
			if err != nil {
				log.WithError(err).WithField("entity_id", entityID).Warn("geo: storing the resolved place failed")
			}
			if after != nil {
				after(o, err)
			}
		},
	}
}

// persistGeoOutcome stores the answer of the resolver, in one transaction:
//   - a place: its coordinates go to gb_entity_geo and the pending record is removed;
//   - ErrNotFound: the pending record is removed (the resolver does not know the place; a new mention of the entity asks again);
//   - a failure of the resolver: the attempt is counted on the pending record, which the sweeper picks up again.
//
// An entity that was deleted meanwhile has nothing to store against: that is not an error.
func persistGeoOutcome(ctx context.Context,
	d *sql.DB,
	entityID int64,
	kind,
	query,
	provider string,
	o memory_geo.Outcome) error {
	tx, err := graph.Begin(ctx, d)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now()
	switch {
	case errors.Is(o.Err, memory_geo.ErrNotFound):
		err = tx.DeleteGeoPending(ctx, entityID)
	case o.Err != nil:
		err = tx.FailGeoPending(ctx,
			entityID,
			o.Err.Error(),
			now)
	default:
		if o.Place.Provider != "" {
			provider = o.Place.Provider
		}
		if err = tx.UpsertEntityGeo(ctx, newEntityGeo(entityID,
			kind,
			query,
			o.Place,
			provider,
			now)); err == nil {
			err = tx.DeleteGeoPending(ctx, entityID)
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// RunGeoSweeper queues the places that are pending (the entities that are places but have no geo metadata: the LLM found them, and the
// resolving was lost or failed) with the geo manager q, right away and then every interval (0: every two minutes), until ctx is done.
// It blocks: run it in a goroutine. provider is the name of the resolver, for the metadata stored.
func RunGeoSweeper(ctx context.Context,
	d *sql.DB,
	q memory_geo.Queue,
	provider string,
	interval time.Duration) {
	if interval <= 0 {
		interval = geoSweepInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if n, err := sweepGeo(ctx,
			d,
			q,
			provider); err != nil {
			log.WithError(err).Warn("geo sweeper: looking for the pending places failed")
		} else if n > 0 {
			log.WithField("places", n).Info("geo sweeper: queued the pending places")
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// sweepGeo queues the pending places that are not queued yet and returns how many it queued; it stops when the queue is full.
func sweepGeo(ctx context.Context,
	d *sql.DB,
	q memory_geo.Queue,
	provider string) (int, error) {
	tx, err := graph.Begin(ctx, d)
	if err != nil {
		return 0, err
	}
	pending, err := tx.ListGeoPending(ctx,
		maxGeoAttempts,
		geoSweepBatch)
	tx.Rollback() // only read
	if err != nil {
		return 0, fmt.Errorf("list pending places: %w", err)
	}
	n := 0
	for _, p := range pending {
		err := q.Enqueue(geoJob(d,
			p.EntityID,
			p.Kind,
			p.Query,
			provider,
			nil))
		switch {
		case err == nil:
			n++
		case errors.Is(err, memory_geo.ErrDuplicate):
		default: // full, or stopped: the rest waits for the next sweep
			return n, nil
		}
	}
	return n, nil
}
