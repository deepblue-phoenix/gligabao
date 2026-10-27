package admin

// Timeseries in the admin UI (see docs/timeseries.md): every series stored in a database, across the datasets of all
// users, with what each is anchored to, spelled out (the names of the entities, the facts, the artifacts) so the UI
// does not have to look them up.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	log "github.com/sirupsen/logrus"
)

// anchorTextMax is how many characters of an anchoring artifact's text are shown.
const anchorTextMax = 120

// tsAnchor is one anchor of a series, with a label for what it is anchored to.
type tsAnchor struct {
	Type        string     `json:"type"` // "artifact", "entity" or "edge"
	ID          int64      `json:"id"`
	Label       string     `json:"label"`
	Role        string     `json:"role,omitempty"`
	WindowStart *time.Time `json:"window_start,omitempty"`
	WindowEnd   *time.Time `json:"window_end,omitempty"`
}

// tsEntity is an entity a series belongs to. Via says how: "anchor" (anchored to it), "edge" (an endpoint of an
// anchored fact) or "artifact" (mentioned by an anchored artifact).
type tsEntity struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Via  string `json:"via"`
}

type tsSeries struct {
	ID         int64           `json:"id"`
	User       string          `json:"user_id"`
	Dataset    string          `json:"dataset"`
	Key        string          `json:"key"`
	Field      string          `json:"field"`
	Name       string          `json:"name"`
	Unit       string          `json:"unit,omitempty"`
	Frequency  string          `json:"frequency,omitempty"`
	Source     string          `json:"source"`
	Provider   string          `json:"provider,omitempty"`
	Definition json.RawMessage `json:"definition,omitempty"`
	PointCount int             `json:"point_count"`
	FirstTS    *time.Time      `json:"first_ts,omitempty"`
	LastTS     *time.Time      `json:"last_ts,omitempty"`
	Stats      json.RawMessage `json:"stats,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Entities   []tsEntity      `json:"entities"`
	Anchors    []tsAnchor      `json:"anchors"`
	// Preview is the series downsampled to at most previewMax points, [unix milliseconds, value] each, for a small plot.
	Preview [][2]float64 `json:"preview"`
}

// previewMax is the most points of a series sent for its preview plot.
const previewMax = 120

// preview downsamples points (oldest first) to at most previewMax, by keeping the min and the max of each bucket in
// time order, so spikes survive. The first and the last point are always kept.
func preview(points []graph.SeriesPoint) [][2]float64 {
	pt := func(p graph.SeriesPoint) [2]float64 { return [2]float64{float64(p.TS.UnixMilli()), p.Value} }
	out := make([][2]float64, 0, previewMax)
	if len(points) <= previewMax {
		for _, p := range points {
			out = append(out, pt(p))
		}
		return out
	}
	buckets := previewMax/2 - 1
	out = append(out, pt(points[0]))
	body := points[1 : len(points)-1]
	for b := 0; b < buckets; b++ {
		chunk := body[b*len(body)/buckets : (b+1)*len(body)/buckets]
		if len(chunk) == 0 {
			continue
		}
		lo, hi := 0, 0
		for i, p := range chunk {
			if p.Value < chunk[lo].Value {
				lo = i
			}
			if p.Value > chunk[hi].Value {
				hi = i
			}
		}
		if lo > hi {
			lo, hi = hi, lo
		}
		out = append(out, pt(chunk[lo]))
		if hi != lo {
			out = append(out, pt(chunk[hi]))
		}
	}
	return append(out, pt(points[len(points)-1]))
}

// TimeseriesOf serves GET /admin/api/timeseries?db=<name>[&include_deleted=true] for one of dbs (the first without
// db): the series of all the datasets of all users, newest update first, with their stats and anchors, no points.
func TimeseriesOf(dbs []Named) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("db")
		if name == "" && len(dbs) > 0 {
			name = dbs[0].Name
		}
		for _, n := range dbs {
			if n.Name != name {
				continue
			}
			list, err := allSeries(r.Context(), n.DB, r.URL.Query().Get("include_deleted") == "true")
			if err != nil {
				log.WithError(err).Error("admin: list timeseries failed")
				httputil.Error(w, http.StatusInternalServerError, "failed to list timeseries")
				return
			}
			httputil.WriteJSON(w, http.StatusOK, map[string]any{"series": list})
			return
		}
		httputil.Error(w, http.StatusNotFound, "unknown database")
	}
}

func allSeries(ctx context.Context, d *sql.DB, includeDeleted bool) ([]tsSeries, error) {
	datasets, err := relational.ListAllDatasets(ctx, d, includeDeleted)
	if err != nil {
		return nil, err
	}
	out := []tsSeries{}
	for _, ds := range datasets {
		list, err := graph.ListSeries(ctx, d, ds.ID)
		if err != nil {
			return nil, err
		}
		if len(list) == 0 {
			continue
		}
		anchors := make([][]graph.SeriesAnchor, len(list))
		need := map[string]bool{}
		for i, s := range list {
			if anchors[i], err = graph.ListSeriesAnchors(ctx, d, s.ID); err != nil {
				return nil, err
			}
			for _, a := range anchors[i] {
				need[a.Type] = true
			}
		}
		lk, err := loadLookup(ctx, d, ds.ID, need)
		if err != nil {
			return nil, err
		}
		for i, s := range list {
			v := tsSeries{ID: s.ID, User: ds.UserID, Dataset: ds.Name, Key: s.Key, Field: s.Field, Name: s.Name, Unit: s.Unit,
				Frequency: s.Frequency, Source: s.Source, Provider: s.Provider, PointCount: s.PointCount, FirstTS: s.FirstTS,
				LastTS: s.LastTS, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
			if s.Stats != "" && json.Valid([]byte(s.Stats)) {
				v.Stats = json.RawMessage(s.Stats)
			}
			if s.Definition != "" && json.Valid([]byte(s.Definition)) {
				v.Definition = json.RawMessage(s.Definition)
			}
			v.Anchors, v.Entities = lk.describe(anchors[i])
			points, err := graph.SeriesPoints(ctx, d, s.ID, nil, nil, 0)
			if err != nil {
				return nil, err
			}
			v.Preview = preview(points)
			out = append(out, v)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// lookup holds what the anchors of one dataset point to (only the kinds that are needed are loaded).
type lookup struct {
	entities  map[int64]graph.EvidenceEntity
	edges     map[int64]graph.EvidenceEdge
	artifacts map[int64]graph.Artifact
	// artifactEntities are the entities each artifact mentions.
	artifactEntities map[int64][]int64
}

func loadLookup(ctx context.Context, d *sql.DB, dsID int64, need map[string]bool) (*lookup, error) {
	lk := &lookup{entities: map[int64]graph.EvidenceEntity{}, edges: map[int64]graph.EvidenceEdge{}, artifacts: map[int64]graph.Artifact{},
		artifactEntities: map[int64][]int64{}}
	if need[graph.AnchorEntity] || need[graph.AnchorEdge] || need[graph.AnchorArtifact] {
		es, err := graph.ListEntities(ctx, d, dsID)
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			lk.entities[e.ID] = e
		}
	}
	if need[graph.AnchorEdge] {
		es, err := graph.ListGraphEdges(ctx, d, dsID)
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			lk.edges[e.ID] = e
		}
	}
	if need[graph.AnchorArtifact] {
		as, err := graph.ListArtifacts(ctx, d, dsID)
		if err != nil {
			return nil, err
		}
		for _, a := range as {
			lk.artifacts[a.ID] = a
		}
		ea, err := graph.ListEntityArtifacts(ctx, d, dsID)
		if err != nil {
			return nil, err
		}
		for _, l := range ea {
			lk.artifactEntities[l.ArtifactID] = append(lk.artifactEntities[l.ArtifactID], l.EntityID)
		}
	}
	return lk, nil
}

func shortText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// describe labels the anchors and collects the entities they lead to (each once, the most direct way first).
func (lk *lookup) describe(anchors []graph.SeriesAnchor) ([]tsAnchor, []tsEntity) {
	as := make([]tsAnchor, 0, len(anchors))
	es := []tsEntity{}
	seen := map[int64]bool{}
	addEntity := func(id int64, via string) {
		e, ok := lk.entities[id]
		if !ok || seen[id] {
			return
		}
		seen[id] = true
		es = append(es, tsEntity{ID: id, Name: e.Name, Type: e.Type, Via: via})
	}
	// Direct anchors first, then edge endpoints, then the entities of artifacts.
	for _, via := range []string{graph.AnchorEntity, graph.AnchorEdge, graph.AnchorArtifact} {
		for _, a := range anchors {
			if a.Type != via {
				continue
			}
			switch via {
			case graph.AnchorEntity:
				addEntity(a.TargetID, "anchor")
			case graph.AnchorEdge:
				if e, ok := lk.edges[a.TargetID]; ok {
					addEntity(e.SourceID, "edge")
					addEntity(e.TargetID, "edge")
				}
			case graph.AnchorArtifact:
				for _, id := range lk.artifactEntities[a.TargetID] {
					addEntity(id, "artifact")
				}
			}
		}
	}
	for _, a := range anchors {
		label := ""
		switch a.Type {
		case graph.AnchorEntity:
			if e, ok := lk.entities[a.TargetID]; ok {
				label = e.Name + " (" + e.Type + ")"
			}
		case graph.AnchorEdge:
			if e, ok := lk.edges[a.TargetID]; ok {
				label = e.Source + " —" + e.Relation + "→ " + e.Target
			}
		case graph.AnchorArtifact:
			if art, ok := lk.artifacts[a.TargetID]; ok {
				label = art.Kind + ": " + shortText(art.Text, anchorTextMax)
			}
		}
		as = append(as, tsAnchor{Type: a.Type, ID: a.TargetID, Label: label, Role: a.Role, WindowStart: a.WindowStart, WindowEnd: a.WindowEnd})
	}
	return as, es
}
