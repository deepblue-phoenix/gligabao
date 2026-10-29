package memory_remember

// Series observations for Remember (see docs/timeseries.md): the last stage of a call made with
// memory_core.WithSeriesObservations(true), for the series the call stored (the supplied ones and the fetched ones).
//
// For each series the points of the call's window (the period the request carried, or the period that was fetched) are
// read back and summarized in Go (memory_timeseries.ObservationText): a few plain sentences that only say what the stats
// say, such as "Apple close fell 5.57% in this period, from 235.1 USD on 2025-01-29 to 222 USD on 2025-01-31". That text
// is then processed like any other text: the graph is extracted from it, reconciled with the stored facts and stored, so
// the series yields bi-temporal edges, deduplication and embeddings with no logic of its own.
//
// The text is stored as an ARTIFACT OF ITS OWN, of kind "series_observation" (graph.ArtifactSeriesObservation). Its
// series_id says which series it describes, its query which series and period, and its parent is the artifact that anchors
// the series: the artifact of the call (the parent artifact, for a chunked text) for a supplied series, the artifact the
// entity came from for a fetched one. So the provenance chain of a fact runs: the fact -> the observation artifact -> the
// series -> the artifact that anchored it.
//
// The observation is an enrichment of what is already stored: a failure in it (reading the points, the extraction, the
// storage) is logged, recorded in the trajectory and in the result, and never fails Remember or removes anything.

import (
	"context"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_timeseries"

	log "github.com/sirupsen/logrus"
)

// observationPart says that the text stored by store is the observation of a series, and not text the caller sent: it is
// stored as an artifact of kind "series_observation" that records the series and hangs off the artifact that anchors it.
type observationPart struct {
	// ParentID is the artifact that anchors the series.
	ParentID int64
	// SeriesID is the series that is described.
	SeriesID int64
	// Query says which series and period the observation is of.
	Query string
	// Label is only used to label the trajectory steps.
	Label string
}

// observable is a series the call stored, that an observation may be made of.
type observable struct {
	SeriesID int64
	Observed memory_timeseries.Observed
	// ParentID is the artifact that anchors the series.
	ParentID int64
	// From and To are the window of the observation: the period of the points the call wrote.
	From, To time.Time
}

// SeriesObservationResult is the observation made of one series of a Remember call.
type SeriesObservationResult struct {
	SeriesID int64  `json:"series_id"`
	Key      string `json:"key"`
	Field    string `json:"field"`
	// ParentArtifactID is the artifact that anchors the series: the parent of the observation artifact.
	ParentArtifactID int64 `json:"parent_artifact_id"`
	// Query says which series and period the observation is of (also saved on the artifact).
	Query string `json:"query"`
	// From and To are the window of the observation (RFC 3339).
	From string `json:"from"`
	To   string `json:"to"`
	// Text is the generated text; the graph was extracted from it.
	Text string `json:"text,omitempty"`
	// ArtifactID is the series_observation artifact; 0 if nothing was stored (see Error).
	ArtifactID int64 `json:"artifact_id,omitempty"`
	// Entities, Edges and InvalidatedEdges are what the text yielded, as for the artifact of a text (Result).
	Entities         []Entity `json:"entities,omitempty"`
	Edges            []Edge   `json:"edges,omitempty"`
	InvalidatedEdges []int64  `json:"invalidated_edges,omitempty"`
	// Error says why nothing was stored; "" if all went well.
	Error string `json:"error,omitempty"`
}

const (
	// maxSeriesObservations is the most observations one Remember call makes, however many series it stores: each one is
	// an extraction call (and, when there are stored facts to compare, a resolution call).
	maxSeriesObservations = 10
	// maxObservationPoints bounds the points read back for an observation (the most a series can carry in a request).
	maxObservationPoints = memory_core.MaxSeriesPoints
)

// label is appended to the messages of the trajectory steps of an observation; "" for any other text (or nil).
func (o *observationPart) label() string {
	if o == nil {
		return ""
	}
	return fmt.Sprintf(" (observation of series %q)", o.Label)
}

// noteObservable records series the call stored as candidates for an observation, if the call makes them. results are the
// stored series, subjects[i] names the entity that results[i] is about (and its type), "" for none, and the points of
// the window are [from[i], to[i]].
func (j *job) noteObservable(parentID int64, results []memory_timeseries.Result, subjects [][2]string, windows [][2]time.Time) {
	if !j.co.SeriesObservations || j.web != nil || j.obs != nil || parentID == 0 {
		return
	}
	for i, r := range results {
		o := observable{SeriesID: r.ID, ParentID: parentID, From: windows[i][0], To: windows[i][1],
			Observed: memory_timeseries.Observed{Key: r.Key, Field: r.Field, Name: r.Name, Unit: r.Unit, Frequency: r.Frequency,
				Provider: r.Provider, Subject: subjects[i][0], SubjectType: subjects[i][1]}}
		j.observables = append(j.observables, o)
	}
}

// noteSuppliedSeries records the series the caller supplied (j.co.Series, in the order of results) that were stored and
// anchored to the artifact artifactID. A series is about the entity its first entity selector names, and its window is the
// period of the points of the request.
func (j *job) noteSuppliedSeries(artifactID int64, results []memory_timeseries.Result) {
	if !j.co.SeriesObservations || len(results) != len(j.co.Series) {
		return
	}
	subjects := make([][2]string, len(results))
	windows := make([][2]time.Time, len(results))
	for i, s := range j.co.Series {
		for _, a := range s.Anchors {
			if a.Entity != nil {
				subjects[i] = [2]string{a.Entity.Name, a.Entity.Type}
				break
			}
		}
		windows[i] = [2]time.Time{s.Points[0].TS, s.Points[len(s.Points)-1].TS}
	}
	j.noteObservable(artifactID, results, subjects, windows)
}

// observeSeries is the observe_series stage of Remember (see observeSeriesStage): it makes an observation artifact of each
// series the call stored (see the top of this file). It returns one result per series observed. It never fails the call. The
// call makes observations and stored series.
func (j *job) observeSeries(ctx context.Context) (out []SeriesObservationResult) {
	obs := j.observables
	if len(obs) > maxSeriesObservations {
		obs = obs[:maxSeriesObservations]
	}
	results := make([]SeriesObservationResult, 0, len(obs))
	for _, o := range obs {
		results = append(results, j.observe(ctx, o))
	}
	return results
}

// observe makes the observation of one series.
func (j *job) observe(ctx context.Context, o observable) SeriesObservationResult {
	query := memory_timeseries.ObservationQuery(o.Observed, o.From, o.To)
	out := SeriesObservationResult{SeriesID: o.SeriesID, Key: o.Observed.Key, Field: o.Observed.Field, ParentArtifactID: o.ParentID,
		Query: query, From: relational.FormatTime(o.From), To: relational.FormatTime(o.To)}
	fields := log.Fields{"stage": "observe_series", "user_id": j.userID, "dataset": j.dataset, "series_id": o.SeriesID,
		"key": o.Observed.Key, "parent_artifact_id": o.ParentID}
	fail := func(stage string, err error) SeriesObservationResult {
		out.Error = stage + ": " + err.Error()
		log.WithError(err).WithFields(fields).Warn("remember: series observation failed for " + stage)
		return out
	}
	args := map[string]any{"series_id": o.SeriesID, "key": o.Observed.Key, "field": o.Observed.Field, "parent_artifact_id": o.ParentID,
		"from": out.From, "to": out.To}

	//--------------------------------------
	// THE TEXT: from the stats of the points of the window, in Go
	from, to := o.From, o.To
	points, err := graph.SeriesPoints(ctx, j.d, o.SeriesID, &from, &to, maxObservationPoints)
	if err != nil {
		return fail("read points", err)
	}
	text := memory_timeseries.ObservationText(o.Observed, points)
	if text == "" {
		j.tr.Tool("The series has no points in the window, so there is nothing to observe.", "observe_series", args, map[string]any{"points": 0})
		j.run.Output(StageObserveSeries, "observe_series", o.Observed.Key, map[string]any{"request": args, "points": 0})
		return out
	}
	out.Text = text
	observed := map[string]any{"points": len(points), "text": text}
	j.tr.Tool("Generated the observation text from the stats of the series' points in the window (no LLM).", "observe_series", args, observed)
	j.run.Output(StageObserveSeries, "observe_series", o.Observed.Key, map[string]any{"request": args, "result": observed})

	//--------------------------------------
	// THE OBSERVATION IS A TEXT LIKE ANY OTHER: a job of its own that stores a "series_observation" artifact. It is as of
	// the last point of the window, so its facts are valid from then.
	last := points[len(points)-1].TS
	oj := *j
	oj.run = j.run.Within(StageObserveSeries) // its inferences are the observe_series stage's
	oj.obs = &observationPart{ParentID: o.ParentID, SeriesID: o.SeriesID, Query: query, Label: o.Observed.Key}
	oj.kind, oj.ref = graph.ArtifactSeriesObservation, last

	ex, err := Extract(ctx, oj.co.ModelLLM(oj.llm, memory_core.StageExtractGraph), text, oj.ref)
	oj.outputExtraction(oj.stepLabel(nil), ex)
	if err != nil {
		return fail("extract", err)
	}
	oj.tr.Tool("The LLM extracted these entities and edges (new facts) from the series observation, before any stored fact is considered."+oj.obs.label(),
		"extract_graph", map[string]any{"text_chars": len(text)},
		map[string]any{"entities": ex.Entities, "edges": candidateFacts(ex.Edges, nil)})
	res, err := oj.reconcile(ctx, text, ex)
	if err != nil {
		return fail("store", err)
	}
	out.ArtifactID, out.Entities, out.Edges, out.InvalidatedEdges = res.ArtifactID, res.Entities, res.Edges, res.InvalidatedEdges
	fields["artifact_id"], fields["entities"], fields["edges"] = res.ArtifactID, len(res.Entities), len(res.Edges)
	log.WithFields(fields).Info("remember: series observation stored")
	return out
}
