package memory_ontology

// Ontologies describe the concepts of a domain: classes, the relations between them and their properties, written in the YAML/JSON DSL
// of package ontospec. This package stores them (gb_ontologies, with a version history in gb_ontology_versions), keeps the compiled
// registries in memory and swaps them when an ontology is saved, and resolves the ontologies a remember call names into one registry
// (Resolve). Ontologies are owned by a user and named; names are unique per user. They are separate from datasets.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_ontology/ontospec"
)

// cached is the compiled form of one stored ontology, at one version.
type cached struct {
	ref  ontospec.Ref
	spec ontospec.Spec
	reg  *ontospec.Registry
}

// registryStore holds, for each (user, ontology), a pointer to its compiled registry. A save swaps the pointer atomically, so the calls
// that are running keep the registry they started with and the next ones get the new one.
type registryStore struct {
	mu   sync.Mutex
	byID map[string]*atomic.Pointer[cached]
}

var (
	ErrInvalidName = errors.New("invalid ontology name")
	// ErrInvalid is wrapped by the errors of an ontology that is not valid (a malformed document, or problems found by Validate; the
	// error is then an *ontospec.InvalidError with the list), and of ontologies that cannot be merged.
	ErrInvalid  = ontospec.ErrInvalid
	ErrExists   = errors.New("ontology already exists")
	ErrNotFound = errors.New("ontology not found")
)

// MaxBytes is the maximum size of an ontology document.
const MaxBytes = 8 << 20

// Meta is a stored ontology (see relational.Ontology).
type Meta = relational.Ontology

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Registries is the in-memory store of the compiled registries of the server.
var Registries = &registryStore{byID: map[string]*atomic.Pointer[cached]{}}

// ResolveName trims name and checks it: required, 1-64 characters of letters, digits, '.', '_' or '-',
// starting with a letter or digit.
func ResolveName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%w: name is required", ErrInvalidName)
	}
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("%w: name must be 1-64 characters of letters, digits, '.', '_' or '-', starting with a letter or digit", ErrInvalidName)
	}
	return name, nil
}

//---------------------------------------------------------------------------------------------

// prepared is a parsed and validated document, ready to be stored.
type prepared struct {
	meta Meta // everything but the ID, the owner, the name, the version and the timestamps
	spec ontospec.Spec
}

// prepare validates the inputs, parses the document (format "yaml", "json", or "" to detect it) and checks the spec.
func prepare(userID,
	name,
	description,
	format string,
	content []byte) (userIDOut, nameOut string, p *prepared, err error) {
	if userID, err = memory_core.ResolveUserID(userID); err != nil {
		return
	}
	if name, err = ResolveName(name); err != nil {
		return
	}
	if len(content) > MaxBytes {
		err = fmt.Errorf("%w: larger than %d bytes",
			ErrInvalid,
			MaxBytes)
		return
	}
	if format = strings.ToLower(strings.TrimSpace(format)); format == "" {
		format = ontospec.DetectFormat(content)
	}
	if format == "yml" {
		format = ontospec.FormatYAML
	}
	spec, parseErr := ontospec.Parse(format, content)
	if parseErr != nil {
		err = parseErr
		return
	}
	if _, compileErr := ontospec.Compile(ontospec.Part{
		Ref:  ontospec.Ref{Name: name},
		Spec: spec,
	}); compileErr != nil {
		err = compileErr
		return
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return
	}
	if description = strings.TrimSpace(description); description == "" {
		description = spec.Description
	}
	sum := sha256.Sum256(content)
	p = &prepared{
		meta: Meta{
			Description: description,
			Format:      format,
			SizeBytes:   len(content),
			SHA256:      hex.EncodeToString(sum[:]),
			Classes:     len(spec.Classes),
			Relations:   len(spec.Relations),
			Properties:  countProperties(spec),
			Spec:        specJSON,
			Content:     string(content),
		},
		spec: spec,
	}
	return userID, name, p, nil
}

func countProperties(s ontospec.Spec) int {
	n := 0
	for _, c := range s.Classes {
		n += len(c.Properties)
	}
	for _, r := range s.Relations {
		n += len(r.Properties)
	}
	return n
}

// Create parses the ontology in content (YAML or JSON; format "" detects it), checks it and stores it for userID under name, as its
// version 1. If description is empty, the ontology's own description is used. It returns ErrExists if the user already has an ontology
// with that name, and an error wrapping ErrInvalid (an *ontospec.InvalidError, for a spec with problems) if it is not valid. Nothing
// is kept when it fails.
func Create(ctx context.Context,
	d *sql.DB,
	userID,
	name,
	description,
	format string,
	content []byte) (*Meta, error) {
	userID, name, p, err := prepare(userID,
		name,
		description,
		format,
		content)
	if err != nil {
		return nil, err
	}
	p.meta.UserID, p.meta.Name = userID, name
	meta, err := relational.CreateOntology(ctx,
		d,
		p.meta)
	if errors.Is(err, relational.ErrExists) {
		return nil, ErrExists
	}
	if err != nil {
		return nil, err
	}
	if err := Registries.put(userID,
		meta,
		p.spec); err != nil {
		return nil, err
	}
	archive(ctx,
		userID,
		name,
		actionCreate,
		meta.Format,
		content)
	return withoutSpec(meta), nil
}

// Update replaces the ontology stored for userID under name with the one in content: the new document is the new version (the version
// is incremented, the old documents are kept in the history), and the compiled registry is swapped for the new one. It returns ErrNotFound
// if there is no such ontology (an update never creates one) and an error wrapping ErrInvalid if the document is not valid, in which
// case the stored ontology is left as it was.
func Update(ctx context.Context,
	d *sql.DB,
	userID,
	name,
	description,
	format string,
	content []byte) (*Meta, error) {
	userID, name, p, err := prepare(userID,
		name,
		description,
		format,
		content)
	if err != nil {
		return nil, err
	}
	old, err := relational.GetOntology(ctx,
		d,
		userID,
		name)
	if errors.Is(err, relational.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if p.meta.Description == "" {
		p.meta.Description = old.Description
	}
	p.meta.ID = old.ID
	meta, err := relational.UpdateOntology(ctx,
		d,
		p.meta)
	if errors.Is(err, relational.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := Registries.put(userID,
		meta,
		p.spec); err != nil {
		return nil, err
	}
	archive(ctx,
		userID,
		name,
		actionUpdate,
		meta.Format,
		content)
	return withoutSpec(meta), nil
}

// Get returns userID's ontology called name, spec included, or ErrNotFound.
func Get(ctx context.Context,
	d *sql.DB,
	userID,
	name string) (*Meta, error) {
	userID, err := memory_core.ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	if name, err = ResolveName(name); err != nil {
		return nil, err
	}
	meta, err := relational.GetOntology(ctx,
		d,
		userID,
		name)
	if errors.Is(err, relational.ErrNotFound) {
		return nil, ErrNotFound
	}
	return meta, err
}

// List returns userID's ontologies, oldest first, without their specs.
func List(ctx context.Context,
	d *sql.DB,
	userID string) ([]Meta, error) {
	userID, err := memory_core.ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	metas, err := relational.ListOntologies(ctx,
		d,
		userID)
	if err != nil {
		return nil, err
	}
	out := make([]Meta, len(metas))
	for i := range metas {
		out[i] = *withoutSpec(&metas[i])
	}
	return out, nil
}

func withoutSpec(m *Meta) *Meta {
	c := *m
	c.Spec, c.Content = nil, ""
	return &c
}

// Events returns the grounding events of userID's ontology called name (see graph.OntologyEvent), newest first.
func Events(ctx context.Context,
	d *sql.DB,
	userID,
	name string,
	f graph.OntologyEventFilter) ([]graph.OntologyEvent, error) {
	meta, err := Get(ctx,
		d,
		userID,
		name)
	if err != nil {
		return nil, err
	}
	return graph.ListOntologyEvents(ctx,
		d,
		meta.ID,
		f)
}

//---------------------------------------------------------------------------------------------
// The registries

// Resolve returns the registry of the ontologies a remember call grounds against: the ontologies called names of userID, merged
// (all the user's ontologies if names is empty). It returns ErrNotFound (naming it) if one does not exist or the user has none, and an
// error wrapping ErrInvalid, naming both ontologies, if two of them define the same class or relation differently.
func Resolve(ctx context.Context,
	d *sql.DB,
	userID string,
	names []string) (*ontospec.Registry, error) {
	userID, err := memory_core.ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	metas, err := selected(ctx,
		d,
		userID,
		names)
	if err != nil {
		return nil, err
	}
	entries := make([]*cached, len(metas))
	for i := range metas {
		if entries[i], err = Registries.get(userID, &metas[i]); err != nil {
			return nil, err
		}
	}
	if len(entries) == 1 {
		return entries[0].reg, nil
	}
	return ontospec.Compile(mapParts(entries)...)
}

func mapParts(entries []*cached) []ontospec.Part {
	out := make([]ontospec.Part, len(entries))
	for i, e := range entries {
		out[i] = ontospec.Part{
			Ref:  e.ref,
			Spec: e.spec,
		}
	}
	return out
}

// selected loads the ontologies called names (duplicates counted once), in the order given; all of the user's if names is empty.
func selected(ctx context.Context,
	d *sql.DB,
	userID string,
	names []string) ([]Meta, error) {
	if len(names) == 0 {
		all, err := relational.ListOntologies(ctx,
			d,
			userID)
		if err != nil {
			return nil, err
		}
		if len(all) == 0 {
			return nil, fmt.Errorf("%w: the user has no ontologies", ErrNotFound)
		}
		sort.SliceStable(all, func(i, j int) bool { return all[i].Name < all[j].Name })
		return all, nil
	}
	var out []Meta
	seen := map[string]bool{}
	for _, n := range names {
		n, err := ResolveName(n)
		if err != nil {
			return nil, err
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		meta, err := relational.GetOntology(ctx,
			d,
			userID,
			n)
		if errors.Is(err, relational.ErrNotFound) {
			return nil, fmt.Errorf("%w: %q", ErrNotFound, n)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, *meta)
	}
	return out, nil
}

func storeKey(userID,
	name string) string {
	return userID + "\x00" + name
}

// slot returns the pointer of (user, name), creating it empty.
func (s *registryStore) slot(userID,
	name string) *atomic.Pointer[cached] {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := storeKey(userID, name)
	p, ok := s.byID[k]
	if !ok {
		p = &atomic.Pointer[cached]{}
		s.byID[k] = p
	}
	return p
}

// Forget drops the cached registry of (user, ontology): the next use compiles it from the database again.
func (s *registryStore) Forget(userID,
	name string) {
	s.slot(userID, name).Store(nil)
}

// compileMeta compiles a stored ontology (its spec as JSON) into a cache entry.
func compileMeta(m *Meta) (*cached, error) {
	var spec ontospec.Spec
	if err := json.Unmarshal(m.Spec, &spec); err != nil {
		return nil, fmt.Errorf("ontology %q: stored spec: %w", m.Name, err)
	}
	return compileSpec(m, spec)
}

func compileSpec(m *Meta,
	spec ontospec.Spec) (*cached, error) {
	ref := ontospec.Ref{
		ID:      m.ID,
		Name:    m.Name,
		Version: m.Version,
	}
	reg, err := ontospec.Compile(ontospec.Part{
		Ref:  ref,
		Spec: spec,
	})
	if err != nil {
		return nil, err
	}
	return &cached{
		ref:  ref,
		spec: spec,
		reg:  reg,
	}, nil
}

// put compiles a saved ontology and swaps it in: the calls that already hold the previous registry are not affected.
func (s *registryStore) put(userID string,
	m *Meta,
	spec ontospec.Spec) error {
	c, err := compileSpec(m, spec)
	if err != nil {
		return err
	}
	swapIfNewer(s.slot(userID, m.Name), c)
	return nil
}

// swapIfNewer stores c unless the slot already holds a later version of the ontology (a slow reader must not put an old one back).
func swapIfNewer(slot *atomic.Pointer[cached],
	c *cached) {
	for {
		cur := slot.Load()
		if cur != nil && cur.ref.ID == c.ref.ID && cur.ref.Version > c.ref.Version {
			return
		}
		if slot.CompareAndSwap(cur, c) {
			return
		}
	}
}

// get returns the compiled form of the stored ontology m: the one in memory if it is of the same version, else it is compiled from m
// (the first use after a start, or after a save by another server) and swapped in.
func (s *registryStore) get(userID string,
	m *Meta) (*cached, error) {
	slot := s.slot(userID, m.Name)
	if c := slot.Load(); c != nil && c.ref.ID == m.ID && c.ref.Version == m.Version {
		return c, nil
	}
	c, err := compileMeta(m)
	if err != nil {
		return nil, err
	}
	swapIfNewer(slot, c)
	return c, nil
}
