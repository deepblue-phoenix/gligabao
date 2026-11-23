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
	Classes []string
	// MinGraphsPerClass drops the classes that have fewer graphs than this (0 or 1: keep all).
	MinGraphsPerClass int
	// ValidationFraction is the share of the graphs of each class that goes to the validation set, in [0, 1). 0 means no validation set.
	// A class never loses its last training graph, so a class with one graph has no validation graph.
	ValidationFraction float64
	// Seed makes the split reproducible: the same dataset, options and seed give the same sets.
	Seed uint64
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

// ErrInvalidOptions means Options is malformed.
var ErrInvalidOptions = errors.New("invalid training options")

// Validate checks the options.
func (o Options) Validate() error {
	if math.IsNaN(o.ValidationFraction) || o.ValidationFraction < 0 || o.ValidationFraction >= 1 {
		return fmt.Errorf("%w: validation_fraction must be in [0, 1)",
			ErrInvalidOptions)
	}
	if o.MinGraphsPerClass < 0 {
		return fmt.Errorf("%w: min_graphs_per_class must not be negative",
			ErrInvalidOptions)
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
	items := convertAll(td)
	if len(items) == 0 {
		return Sets{}, ErrNoGraphs
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

// Item is a graph ready for sargo and the artifact it is of.
type Item struct {
	ArtifactID int64
	Graph      sargo.GCNIIGraph
}

// DefaultHook is the classifying training hook used when the program that embeds gligabao has not set one: it trains on every graph that
// has an LLM class (see PickClass) and drops the classes with fewer graphs than opts.MinGraphsPerClass (and, with opts.Classes, the
// classes not in it). The artifacts without a kept graph are left out too.
func DefaultHook(opts Options) memory_core.ClassifyingTrainingHook {
	allowed, _ := memory_core.ResolveGraphClasses(opts.Classes)
	return func(_ context.Context,
		ds memory_core.KnowledgeDataset) (memory_core.TrainingDataset, error) {
		picked := pickAll(ds.Graphs, allowed)
		counts := countBy(picked)
		kept := filterGraphs(ds.Graphs, func(g memory_core.ArtifactGraph) bool {
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

// PickClass returns the class of a graph out of the classes an LLM gave it: the one given most often (a graph that was remembered
// more than once has a record per run), the earliest on a tie. With allowed not empty only those classes count. "" if there is none.
func PickClass(classes []graph.ArtifactClass,
	allowed []string) string {
	ok := func(c string) bool {
		return len(allowed) == 0 || contains(allowed, c)
	}
	counts := map[string]int{}
	order := []string{}
	for _, c := range classes {
		name := memory_core.NormalizeGraphClass(c.Class)
		if name == "" || !ok(name) {
			continue
		}
		if counts[name] == 0 {
			order = append(order, name)
		}
		counts[name]++
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
// nodes of its source and target. Facts whose endpoint is not an entity of the graph, self loops and repeated facts are left out. ok is
// false for a graph without entities (sargo needs at least one node).
func ToGCNIIGraph(g memory_core.ArtifactGraph,
	class string) (out sargo.GCNIIGraph, ok bool) {
	if len(g.Entities) == 0 {
		return sargo.GCNIIGraph{}, false
	}
	index := make(map[int64]int, len(g.Entities))
	nodes := make([]sargo.GCNIINode, 0, len(g.Entities))
	for _, e := range g.Entities {
		if _, dup := index[e.ID]; dup {
			continue
		}
		index[e.ID] = len(nodes)
		nodes = append(nodes, sargo.GCNIINode{
			Name: e.Name,
			Type: e.Type,
		})
	}
	seen := map[[2]int]bool{}
	edges := [][2]int{}
	for _, e := range g.Edges {
		a, okA := index[e.SourceID]
		b, okB := index[e.TargetID]
		if !okA || !okB || a == b {
			continue
		}
		key := [2]int{min(a, b), max(a, b)}
		if seen[key] {
			continue
		}
		seen[key] = true
		edges = append(edges, key)
	}
	return sargo.GCNIIGraph{
		N:     len(nodes),
		Edges: edges,
		Nodes: nodes,
		Class: class,
	}, true
}

//---------------------------------------------------------------------------------------------

func convertAll(td memory_core.TrainingDataset) []Item {
	items := []Item{}
	for _, g := range td.Graphs {
		class := strings.TrimSpace(td.Classes[g.ArtifactID])
		if converted, ok := ToGCNIIGraph(g, class); ok && class != "" {
			items = append(items, Item{
				ArtifactID: g.ArtifactID,
				Graph:      converted,
			})
		}
	}
	return items
}

func pickAll(graphs []memory_core.ArtifactGraph,
	allowed []string) map[int64]string {
	out := map[int64]string{}
	for _, g := range graphs {
		if class := PickClass(g.Classes, allowed); class != "" {
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
