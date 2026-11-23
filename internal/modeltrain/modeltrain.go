// Package modeltrain turns the graphs of a dataset, with the classes an LLM gave them (the classify_graph stage of remember, stored in
// gb_artifact_classes and read with SQL by memory.LoadKnowledgeDataset), into the training set and the validation set of a GCNII*
// model of sargo.
//
// The steps, all pure functions but the first:
//
//  1. Load: the knowledge dataset (artifacts, graphs, LLM classes) is read from the database; the classifying training hook (or
//     DefaultHook if there is none) makes the TrainingDataset of it: the graphs to train on, each with one class.
//  2. Split: the graphs are partitioned into a training and a validation set, per class (stratified) and deterministic for a seed.
//  3. Convert: each graph becomes a sargo.GCNIIGraph (entities are the nodes, facts the undirected edges, the class its label).
//
// Build does the three and returns a sargo.GCNIITrainRequest-ready pair of sets.
package modeltrain

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sargo"
)

// Options says how the graphs of a dataset become a training and a validation set. The zero value is valid: every classified graph, 20%
// of each class for validation, seed 0.
type Options struct {
	// Classes, if not empty, are the only classes to train on (normalized with memory_core.NormalizeGraphClass); a graph is given
	// the first of its LLM classes that is in it, graphs with none of them are left out.
	Classes []string `json:"classes"`
	// ClassPick is how the class of a graph is picked out of the classes the LLM gave it (see PickClass): PickMajority (the default,
	// "") or PickLatest.
	ClassPick string `json:"class_pick"`
	// TopClasses, if > 0, keeps the N most given classes (of the graphs that can be trained on); the graphs of any other class are
	// trained as OtherClass if OtherClass is true, else left out. 0 keeps every class.
	TopClasses int  `json:"top_classes"`
	OtherClass bool `json:"other_class"`
	// MinEdges is the fewest edges a graph needs to be trained on (the model learns from structure, so a graph without an edge adds
	// noise). 0 means 1.
	MinEdges int `json:"min_edges"`
	// MinGraphsPerClass drops the classes that have fewer graphs than this, after TopClasses (0 or 1: keep all). Dropping leaves
	// fewer than 2 classes sometimes: see ErrTooFewClasses.
	MinGraphsPerClass int `json:"min_graphs_per_class"`
	// ValidationFraction is the share of the graphs of each class that goes to the validation set, in [0, 1). 0 means no validation set.
	// A class never loses its last training graph, so a class with one graph has no validation graph.
	ValidationFraction float64 `json:"validation_fraction"`
	// Seed makes the split reproducible: the same dataset, options and seed give the same sets.
	Seed uint64 `json:"split_seed"`
}

// Sets is the result of Build.
type Sets struct {
	Train      []sargo.GCNIIGraph
	Validation []sargo.GCNIIGraph
	// Classes are the classes of the graphs of both sets, sorted.
	Classes []string
	// ArtifactsTrain and ArtifactsValidation are the artifact IDs of each graph of the sets, in the same order as Train and Validation.
	ArtifactsTrain, ArtifactsValidation []int64
}

// ErrNoGraphs means that no graph of the dataset can be trained on (none has a class, an entity, or the class survived the filters).
var ErrNoGraphs = errors.New("no classified graphs to train on")

// ErrTooFewClasses means the graphs left have fewer than two classes: a classifier of one class learns nothing.
var ErrTooFewClasses = errors.New("training needs graphs of at least 2 classes")

// The values of Options.ClassPick.
const (
	// PickMajority picks the class given most often to the graph (a graph remembered more than once has a record per run), the
	// earliest on a tie.
	PickMajority = "majority"
	// PickLatest picks the first class of the most recent model run (the highest InferenceID; the best class the last call chose).
	// Records without a run (InferenceID 0) are the oldest.
	PickLatest = "latest"
)

// OtherClass is the class of a graph whose own class is not among Options.TopClasses.
const OtherClass = "other"

// MaxTopClasses is the largest Options.TopClasses.
const MaxTopClasses = 1000

// ErrInvalidOptions means Options is malformed.
var ErrInvalidOptions = errors.New("invalid training options")

// Validate checks the options.
func (o Options) Validate() error {
	if math.IsNaN(o.ValidationFraction) || o.ValidationFraction < 0 || o.ValidationFraction >= 1 {
		return fmt.Errorf("%w: validation_fraction must be in [0, 1)",
			ErrInvalidOptions)
	}
	if o.MinGraphsPerClass < 0 || o.MinEdges < 0 {
		return fmt.Errorf("%w: min_graphs_per_class and min_edges must not be negative",
			ErrInvalidOptions)
	}
	if o.TopClasses < 0 || o.TopClasses > MaxTopClasses {
		return fmt.Errorf("%w: top_classes must be in [0, %d]",
			ErrInvalidOptions,
			MaxTopClasses)
	}
	if o.ClassPick != "" && o.ClassPick != PickMajority && o.ClassPick != PickLatest {
		return fmt.Errorf("%w: class_pick must be %q or %q",
			ErrInvalidOptions,
			PickMajority,
			PickLatest)
	}
	_, err := memory_core.ResolveGraphClasses(o.Classes)
	if err != nil {
		return fmt.Errorf("%w: %w",
			ErrInvalidOptions,
			err)
	}
	return nil
}

// Build reads the classes the LLM gave to the graphs of userID's dataset name (empty: the default one), picks one class per graph with
// hook (nil: DefaultHook(opts)) and returns the training and validation sets. It returns ErrNoGraphs if nothing is left to train on,
// and the errors of memory.MakeTrainingDataset (memory_core.ErrDatasetNotFound, a failing hook, ...).
func Build(ctx context.Context,
	d *sql.DB,
	hook memory_core.ClassifyingTrainingHook,
	userID,
	name string,
	opts Options) (Sets, error) {
	if err := opts.Validate(); err != nil {
		return Sets{}, err
	}
	if hook == nil {
		hook = DefaultHook(opts)
	}
	td, err := memory.MakeTrainingDataset(ctx,
		d,
		hook,
		userID,
		name)
	if err != nil {
		return Sets{}, err
	}
	return SetsOf(td, opts)
}

// SetsOf splits a training dataset (whatever hook made it) and converts the graphs. It returns ErrNoGraphs if it has none that can be
// converted.
func SetsOf(td memory_core.TrainingDataset,
	opts Options) (Sets, error) {
	if err := opts.Validate(); err != nil {
		return Sets{}, err
	}
	items := convertAll(td, opts.minEdges())
	if len(items) == 0 {
		return Sets{}, ErrNoGraphs
	}
	if classes := classesOf(items); len(classes) < 2 {
		return Sets{}, fmt.Errorf("%w: %d graph(s) in %d class(es) %v (only graphs with at least %d edge(s) and a class are used; classes come from remember with classify_graph)",
			ErrTooFewClasses,
			len(items),
			len(classes),
			classes,
			opts.minEdges())
	}
	train, val := Split(items,
		opts.ValidationFraction,
		opts.Seed)
	return Sets{
		Train:               graphsOf(train),
		Validation:          graphsOf(val),
		Classes:             classesOf(items),
		ArtifactsTrain:      idsOf(train),
		ArtifactsValidation: idsOf(val),
	}, nil
}

func (o Options) minEdges() int {
	return max(o.MinEdges, 1)
}

// Item is a graph ready for sargo and the artifact it is of.
type Item struct {
	ArtifactID int64
	Graph      sargo.GCNIIGraph
}

// DefaultHook is the classifying training hook used when the program that embeds gligabao has not set one. Of the graphs that can be
// trained on (opts.MinEdges) and have an LLM class (PickClass with opts.ClassPick and opts.Classes) it keeps the ones of the top
// opts.TopClasses classes (the others become OtherClass, if opts.OtherClass, else they are left out), then drops the classes with fewer
// graphs than opts.MinGraphsPerClass. The artifacts without a kept graph are left out too.
func DefaultHook(opts Options) memory_core.ClassifyingTrainingHook {
	allowed, _ := memory_core.ResolveGraphClasses(opts.Classes)
	return func(_ context.Context,
		ds memory_core.KnowledgeDataset) (memory_core.TrainingDataset, error) {
		eligible := filterGraphs(ds.Graphs, func(g memory_core.ArtifactGraph) bool {
			return trainable(g, opts.minEdges())
		})
		picked := withTop(pickAll(eligible,
			allowed,
			opts.ClassPick),
			opts)
		counts := countBy(picked)
		kept := filterGraphs(eligible, func(g memory_core.ArtifactGraph) bool {
			class, ok := picked[g.ArtifactID]
			return ok && counts[class] >= opts.MinGraphsPerClass
		})
		keptIDs := idSet(kept)
		sub := ds
		sub.Graphs = kept
		sub.Artifacts = filterArtifacts(ds.Artifacts, keptIDs)
		return memory_core.TrainingDataset{
			KnowledgeDataset: sub,
			Classes:          restrict(picked, keptIDs),
		}, nil
	}
}

// TopOf returns the n classes given to most graphs (the biggest first, then by name); n <= 0 gives all of them.
func TopOf(classes map[int64]string,
	n int) []string {
	counts := countBy(classes)
	names := make([]string, 0, len(counts))
	for c := range counts {
		names = append(names, c)
	}
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})
	if n > 0 && n < len(names) {
		names = names[:n]
	}
	return names
}

// withTop applies opts.TopClasses and opts.OtherClass to the class of each graph.
func withTop(classes map[int64]string,
	opts Options) map[int64]string {
	if opts.TopClasses <= 0 {
		return classes
	}
	top := map[string]bool{}
	for _, c := range TopOf(classes, opts.TopClasses) {
		top[c] = true
	}
	out := make(map[int64]string, len(classes))
	for id, c := range classes {
		switch {
		case top[c]:
			out[id] = c
		case opts.OtherClass:
			out[id] = OtherClass
		}
	}
	return out
}

// trainable is true for a graph the model can learn from: it converts (ToGCNIIGraph) and has at least minEdges edges.
func trainable(g memory_core.ArtifactGraph,
	minEdges int) bool {
	converted, ok := ToGCNIIGraph(g, "")
	return ok && len(converted.Edges) >= minEdges
}

// PickClass returns the class of a graph out of the classes an LLM gave it. With pick PickMajority (or "") it is the class given most
// often, the earliest on a tie; with PickLatest the first class (lowest record ID) of the most recent run (the highest InferenceID).
// With allowed not empty only those classes count. "" if there is none.
func PickClass(classes []graph.ArtifactClass,
	allowed []string,
	pick string) string {
	usable := []graph.ArtifactClass{}
	for _, c := range classes {
		c.Class = memory_core.NormalizeGraphClass(c.Class)
		if c.Class != "" && (len(allowed) == 0 || contains(allowed, c.Class)) {
			usable = append(usable, c)
		}
	}
	if pick == PickLatest {
		return latestClass(usable)
	}
	return majorityClass(usable)
}

func latestClass(classes []graph.ArtifactClass) string {
	best := -1
	for i, c := range classes {
		if best < 0 || c.InferenceID > classes[best].InferenceID { // ties keep the first (lowest ID)
			best = i
		}
	}
	if best < 0 {
		return ""
	}
	return classes[best].Class
}

func majorityClass(classes []graph.ArtifactClass) string {
	counts := map[string]int{}
	order := []string{}
	for _, c := range classes {
		if counts[c.Class] == 0 {
			order = append(order, c.Class)
		}
		counts[c.Class]++
	}
	best := ""
	for _, name := range order {
		if best == "" || counts[name] > counts[best] {
			best = name
		}
	}
	return best
}

// Split partitions items into a training and a validation set. Each class is split on its own: its items are put in a fixed pseudo-random
// order (a hash of the seed and the artifact ID, so it does not depend on the order of items) and the first round(fraction * n) go to
// validation, but at least one stays in training. The result keeps the order of items within each set.
func Split(items []Item,
	fraction float64,
	seed uint64) (train, validation []Item) {
	byClass := map[string][]Item{}
	for _, it := range items {
		byClass[it.Graph.Class] = append(byClass[it.Graph.Class], it)
	}
	inValidation := map[int64]bool{}
	for _, group := range byClass {
		sorted := append([]Item(nil), group...)
		sort.Slice(sorted, func(i, j int) bool {
			hi, hj := hashOf(seed, sorted[i].ArtifactID), hashOf(seed, sorted[j].ArtifactID)
			if hi != hj {
				return hi < hj
			}
			return sorted[i].ArtifactID < sorted[j].ArtifactID
		})
		n := int(math.Round(fraction * float64(len(sorted))))
		n = min(n, len(sorted)-1)
		for _, it := range sorted[:max(n, 0)] {
			inValidation[it.ArtifactID] = true
		}
	}
	for _, it := range items {
		if inValidation[it.ArtifactID] {
			validation = append(validation, it)
		} else {
			train = append(train, it)
		}
	}
	return train, validation
}

// ToGCNIIGraph converts the graph of an artifact: each entity is a node (its name and type), each fact an undirected edge between the
// nodes of its source and target. A fact whose endpoint is not an entity of the graph (one the artifact did not mention) adds that
// endpoint as a node, named after it and without a type. Self loops and repeated facts are left out. ok is false for a graph without
// nodes (sargo needs at least one).
func ToGCNIIGraph(g memory_core.ArtifactGraph,
	class string) (out sargo.GCNIIGraph, ok bool) {
	index := map[int64]int{}
	nodes := []sargo.GCNIINode{}
	node := func(id int64,
		name,
		typ string) int {
		if i, known := index[id]; known {
			return i
		}
		index[id] = len(nodes)
		nodes = append(nodes, sargo.GCNIINode{
			Name: name,
			Type: typ,
		})
		return index[id]
	}
	for _, e := range g.Entities {
		node(e.ID, e.Name, e.Type)
	}
	seen := map[[2]int]bool{}
	edges := [][2]int{}
	for _, e := range g.Edges {
		a := node(e.SourceID, e.Source, "")
		b := node(e.TargetID, e.Target, "")
		key := [2]int{min(a, b), max(a, b)}
		if a == b || seen[key] {
			continue
		}
		seen[key] = true
		edges = append(edges, key)
	}
	if len(nodes) == 0 {
		return sargo.GCNIIGraph{}, false
	}
	return sargo.GCNIIGraph{
		N:     len(nodes),
		Edges: edges,
		Nodes: nodes,
		Class: class,
	}, true
}

//---------------------------------------------------------------------------------------------

func convertAll(td memory_core.TrainingDataset,
	minEdges int) []Item {
	items := []Item{}
	for _, g := range td.Graphs {
		class := strings.TrimSpace(td.Classes[g.ArtifactID])
		if converted, ok := ToGCNIIGraph(g, class); ok && class != "" && len(converted.Edges) >= minEdges {
			items = append(items, Item{
				ArtifactID: g.ArtifactID,
				Graph:      converted,
			})
		}
	}
	return items
}

func pickAll(graphs []memory_core.ArtifactGraph,
	allowed []string,
	pick string) map[int64]string {
	out := map[int64]string{}
	for _, g := range graphs {
		if class := PickClass(g.Classes,
			allowed,
			pick); class != "" {
			out[g.ArtifactID] = class
		}
	}
	return out
}

func countBy(classes map[int64]string) map[string]int {
	out := map[string]int{}
	for _, c := range classes {
		out[c]++
	}
	return out
}

func filterGraphs(graphs []memory_core.ArtifactGraph,
	keep func(memory_core.ArtifactGraph) bool) []memory_core.ArtifactGraph {
	out := []memory_core.ArtifactGraph{}
	for _, g := range graphs {
		if keep(g) {
			out = append(out, g)
		}
	}
	return out
}

func filterArtifacts(artifacts []graph.Artifact,
	ids map[int64]bool) []graph.Artifact {
	out := []graph.Artifact{}
	for _, a := range artifacts {
		if ids[a.ID] {
			out = append(out, a)
		}
	}
	return out
}

func idSet(graphs []memory_core.ArtifactGraph) map[int64]bool {
	out := make(map[int64]bool, len(graphs))
	for _, g := range graphs {
		out[g.ArtifactID] = true
	}
	return out
}

func restrict(classes map[int64]string,
	ids map[int64]bool) map[int64]string {
	out := make(map[int64]string, len(ids))
	for id, c := range classes {
		if ids[id] {
			out[id] = c
		}
	}
	return out
}

func graphsOf(items []Item) []sargo.GCNIIGraph {
	out := make([]sargo.GCNIIGraph, 0, len(items))
	for _, it := range items {
		out = append(out, it.Graph)
	}
	return out
}

func idsOf(items []Item) []int64 {
	out := make([]int64, 0, len(items))
	for _, it := range items {
		out = append(out, it.ArtifactID)
	}
	return out
}

func classesOf(items []Item) []string {
	set := map[string]bool{}
	for _, it := range items {
		set[it.Graph.Class] = true
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func contains(list []string,
	s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func hashOf(seed uint64,
	id int64) uint64 {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%d/%d", seed, id)
	return h.Sum64()
}

func classCounts(graphs []sargo.GCNIIGraph) map[string]int {
	out := map[string]int{}
	for _, g := range graphs {
		out[g.Class]++
	}
	return out
}
