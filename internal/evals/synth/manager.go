package synth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/evals/evalfile"
	"github.com/deepblue-phoenix/gligabao/internal/evals/usereval"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	log "github.com/sirupsen/logrus"
)

// Timing of the generations.
const (
	// generationTimeout is the longest a generation may take.
	generationTimeout = 30 * time.Minute
	// DefaultStaleAfter is how long a generation may go without progress before it counts as lost (its server died).
	DefaultStaleAfter = 10 * time.Minute
	// detailGenerations is the number of generations the detail of an eval lists.
	detailGenerations = 20
)

// ErrExists means an eval of that name exists already (HTTP 409).
var ErrExists = errors.New("synth: an eval with this name already exists")

// Config is what a Manager works with.
type Config struct {
	DB    *sql.DB
	Store evalsdb.Store
	// LLM writes the cases (nil: Create and Regenerate answer core.ErrNoLLM).
	LLM memory_core.LLM
	// Env is the environment of package usereval (the files of the users and their limits), read at every call.
	Env func() usereval.Env
	// Models are the LLM models a definition may name.
	Models func() []string
	// Ctx is the lifetime of the background generations (nil: context.Background()).
	Ctx context.Context
	// StaleAfter is DefaultStaleAfter if 0.
	StaleAfter time.Duration
}

// Manager creates and changes synthetic evals and runs their generations in the background.
type Manager struct {
	cfg Config
	wg  sync.WaitGroup
}

// New makes a Manager.
func New(cfg Config) *Manager {
	if cfg.Ctx == nil {
		cfg.Ctx = context.Background()
	}
	if cfg.StaleAfter == 0 {
		cfg.StaleAfter = DefaultStaleAfter
	}
	return &Manager{
		cfg: cfg,
	}
}

// Wait blocks until the generations in the background have ended.
func (m *Manager) Wait() { m.wg.Wait() }

// CanGenerate says whether the server has an LLM.
func (m *Manager) CanGenerate() bool { return m.cfg.LLM != nil }

// Models are the models a definition may name.
func (m *Manager) Models() []string { return m.cfg.Models() }

// Sweep ends the generations that went without progress for StaleAfter (errored, "server restarted or crashed"). It returns how many.
func (m *Manager) Sweep(ctx context.Context) (int64, error) {
	now := time.Now()
	return m.cfg.Store.FailStaleSynthGenerations(ctx,
		m.cfg.DB,
		now.Add(-m.cfg.StaleAfter),
		"server restarted or crashed while the generation was in progress",
		now)
}

func checkOwner(owner string) error {
	if owner == "" || owner == core.SystemUser {
		return usereval.ErrInvalidUser
	}
	return nil
}

func invalid(problems ...evalfile.Problem) error {
	return &usereval.InvalidError{
		Problems: problems,
	}
}

// Create makes the synthetic eval name of the owner: its eval def (without cases yet) and its definition, version 1, from Defaults with
// the patch applied, and starts the generation of its cases. Errors: usereval.ErrInvalidUser, *usereval.InvalidError (the name or the
// settings), core.ErrNoLLM, ErrExists.
func (m *Manager) Create(ctx context.Context,
	owner,
	name string,
	p Patch) (*core.SynthGeneration, error) {
	if err := checkOwner(owner); err != nil {
		return nil, err
	}
	if !evalfile.ValidName(name) || slices.Contains(evalfile.ReservedNames, name) {
		return nil, invalid(evalfile.Problem(fmt.Sprintf(
			"the eval name %q is not valid (1-64 characters: letters, digits, . _ -, starting with a letter or digit, no \"..\"; reserved: %v)",
			name,
			evalfile.ReservedNames)))
	}
	settings := Apply(Defaults(), p)
	env := m.cfg.Env()
	if problems := Validate(settings,
		m.cfg.Models(),
		env.Limits.MaxCases); len(problems) > 0 {
		return nil, invalid(problems...)
	}
	if m.cfg.LLM == nil {
		return nil, core.ErrNoLLM
	}
	if _, err := m.cfg.Store.GetEvalDefByName(ctx,
		m.cfg.DB,
		owner,
		name); err == nil {
		return nil, ErrExists
	} else if !errors.Is(err, evalsdb.ErrNotFound) {
		return nil, err
	}
	if _, err := usereval.Get(ctx,
		env,
		owner,
		name); err == nil {
		return nil, ErrExists
	} else if !errors.Is(err, usereval.ErrNotFound) {
		return nil, err
	}
	description := ""
	if p.Description != nil {
		description = *p.Description
	}
	var def core.SynthDef
	var gen core.SynthGeneration
	if err := m.tx(ctx, func(q evalsdb.Execer) error {
		ev := &core.EvalDef{
			UserID:      owner,
			Name:        name,
			Description: description,
			Type:        core.TypeKnowledge,
		}
		if err := m.cfg.Store.CreateEvalDef(ctx,
			q,
			ev); err != nil {
			return err
		}
		def = core.SynthDef{
			EvalDefID:     ev.ID,
			SynthSettings: settings,
		}
		if err := m.cfg.Store.CreateSynthDef(ctx,
			q,
			owner,
			&def); err != nil {
			return err
		}
		gen = newGeneration(def)
		return m.cfg.Store.CreateSynthGeneration(ctx,
			q,
			owner,
			&gen)
	}); err != nil {
		return nil, err
	}
	m.start(owner,
		name,
		description,
		def,
		gen,
		false)
	return &gen, nil
}

// newGeneration is the record of a generation with the settings of def.
func newGeneration(def core.SynthDef) core.SynthGeneration {
	return core.SynthGeneration{
		EvalDefID: def.EvalDefID,
		Version:   def.Version,
		Model:     def.Model,
		Mode:      def.Mode,
		Requested: def.NumExamples,
		Status:    core.SynthPending,
	}
}

// tx runs f in a transaction of the database.
func (m *Manager) tx(ctx context.Context,
	f func(q evalsdb.Execer) error) error {
	t, err := m.cfg.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := f(t); err != nil {
		t.Rollback()
		return err
	}
	return t.Commit()
}

// lookup returns the eval def and the definition of the synthetic eval name of the owner (evalsdb.ErrNotFound if there is none, also for
// a human eval).
func (m *Manager) lookup(ctx context.Context,
	owner,
	name string) (*core.EvalDef, *core.SynthDef, error) {
	if err := checkOwner(owner); err != nil {
		return nil, nil, err
	}
	ev, err := m.cfg.Store.GetEvalDefByName(ctx,
		m.cfg.DB,
		owner,
		name)
	if err != nil {
		return nil, nil, err
	}
	def, err := m.cfg.Store.GetSynthDef(ctx,
		m.cfg.DB,
		owner,
		ev.ID)
	if err != nil {
		return nil, nil, err
	}
	return ev, def, nil
}

// Update changes the definition of the synthetic eval name of the owner by the patch. A change of the settings makes a new version
// (the same settings do not); a Description is changed in the eval too. With regenerate it then starts a generation from the new version
// (keepEdited: see Regenerate). It returns the definition and the generation (nil without regenerate). Errors: those of Create,
// evalsdb.ErrNotFound, core.ErrSynthConflict.
func (m *Manager) Update(ctx context.Context,
	owner,
	name string,
	p Patch,
	regenerate,
	keepEdited bool) (*core.SynthDef, *core.SynthGeneration, error) {
	ev, cur, err := m.lookup(ctx,
		owner,
		name)
	if err != nil {
		return nil, nil, err
	}
	settings := Apply(cur.SynthSettings, p)
	if problems := Validate(settings,
		m.cfg.Models(),
		m.cfg.Env().Limits.MaxCases); len(problems) > 0 {
		return nil, nil, invalid(problems...)
	}
	if regenerate && m.cfg.LLM == nil {
		return nil, nil, core.ErrNoLLM
	}
	def, err := m.cfg.Store.UpdateSynthDef(ctx,
		m.cfg.DB,
		owner,
		ev.ID,
		settings)
	if err != nil {
		return nil, nil, err
	}
	description := ev.Description
	if p.Description != nil && *p.Description != ev.Description {
		description = *p.Description
		if err := m.setDescription(ctx,
			owner,
			ev,
			description); err != nil {
			return nil, nil, err
		}
	}
	if !regenerate {
		return def, nil, nil
	}
	gen, err := m.startGeneration(ctx,
		owner,
		name,
		description,
		*def,
		keepEdited)
	if err != nil {
		return nil, nil, err
	}
	return def, gen, nil
}

// setDescription changes the description of the eval def, and of the stored file if it has one.
func (m *Manager) setDescription(ctx context.Context,
	owner string,
	ev *core.EvalDef,
	description string) error {
	ev.Description = description
	if err := m.cfg.Store.UpdateEvalDef(ctx,
		m.cfg.DB,
		ev); err != nil {
		return err
	}
	stored, err := usereval.Get(ctx,
		m.cfg.Env(),
		owner,
		ev.Name)
	if errors.Is(err, usereval.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	parsed, err := evalfile.ParseUser(stored.JSON)
	if err != nil {
		return err
	}
	data, err := evalJSON(ev.Name,
		description,
		parsed.Cases)
	if err != nil {
		return err
	}
	_, err = usereval.Save(ctx,
		m.cfg.Env(),
		owner,
		ev.Name,
		data)
	return err
}

// Regenerate starts a generation of the synthetic eval name of the owner from its current definition. With keepEdited the cases the
// user edited or added stay as they are and only the others are generated again (so that the eval has NumExamples cases); without it
// every case is replaced. Errors: evalsdb.ErrNotFound, core.ErrNoLLM, core.ErrSynthConflict (one is going).
func (m *Manager) Regenerate(ctx context.Context,
	owner,
	name string,
	keepEdited bool) (*core.SynthGeneration, error) {
	ev, def, err := m.lookup(ctx,
		owner,
		name)
	if err != nil {
		return nil, err
	}
	if m.cfg.LLM == nil {
		return nil, core.ErrNoLLM
	}
	return m.startGeneration(ctx,
		owner,
		name,
		ev.Description,
		*def,
		keepEdited)
}

func (m *Manager) startGeneration(ctx context.Context,
	owner,
	name,
	description string,
	def core.SynthDef,
	keepEdited bool) (*core.SynthGeneration, error) {
	gen := newGeneration(def)
	if err := m.cfg.Store.CreateSynthGeneration(ctx,
		m.cfg.DB,
		owner,
		&gen); err != nil {
		return nil, err
	}
	m.start(owner,
		name,
		description,
		def,
		gen,
		keepEdited)
	return &gen, nil
}

// start runs the generation in the background.
func (m *Manager) start(owner,
	name,
	description string,
	def core.SynthDef,
	gen core.SynthGeneration,
	keepEdited bool) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ctx, cancel := context.WithTimeout(m.cfg.Ctx, generationTimeout)
		defer cancel()
		m.run(ctx,
			owner,
			name,
			description,
			def,
			gen,
			keepEdited)
	}()
}

// evalJSON is the file of an eval.
func evalJSON(name,
	description string,
	cases []core.KnowledgeCase) ([]byte, error) {
	return json.MarshalIndent(struct {
		Name        string               `json:"name"`
		Description string               `json:"description,omitempty"`
		Cases       []core.KnowledgeCase `json:"cases"`
	}{
		Name:        name,
		Description: description,
		Cases:       cases,
	},
		"",
		"  ")
}

// existing returns the cases the stored file of the eval has (none if it has no file yet).
func (m *Manager) existing(ctx context.Context,
	owner,
	name string) ([]core.KnowledgeCase, error) {
	stored, err := usereval.Get(ctx,
		m.cfg.Env(),
		owner,
		name)
	if errors.Is(err, usereval.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ev, err := evalfile.ParseUser(stored.JSON)
	if err != nil {
		return nil, err
	}
	return ev.Cases, nil
}

// run is the generation: it reads the stored cases, keeps the ones to keep, generates the others, writes the eval and records what
// came of it. It never returns an error: the outcome is in the record of the generation.
func (m *Manager) run(ctx context.Context,
	owner,
	name,
	description string,
	def core.SynthDef,
	gen core.SynthGeneration,
	keepEdited bool) {
	start := time.Now().UTC()
	gen.Status, gen.StartedAt = core.SynthRunning, &start
	record := func() {
		if err := m.cfg.Store.UpdateSynthGeneration(context.WithoutCancel(ctx),
			m.cfg.DB,
			owner,
			&gen); err != nil {
			log.WithError(err).Warnf("synth: record generation %d", gen.ID)
		}
	}
	record()
	err := m.generate(ctx,
		owner,
		name,
		description,
		def,
		&gen,
		keepEdited,
		record)
	end := time.Now().UTC()
	gen.FinishedAt = &end
	gen.Status = core.SynthCompleted
	if err != nil {
		gen.Status, gen.Error = core.SynthErrored, err.Error()
		log.WithError(err).Warnf("synth: generation %d of eval %q failed", gen.ID, name)
	}
	record()
}

// generate does the work of run; gen is updated as it goes (record stores it).
func (m *Manager) generate(ctx context.Context,
	owner,
	name,
	description string,
	def core.SynthDef,
	gen *core.SynthGeneration,
	keepEdited bool,
	record func()) error {
	have, err := m.existing(ctx,
		owner,
		name)
	if err != nil {
		return err
	}
	rows, err := m.cfg.Store.ListSynthCases(ctx,
		m.cfg.DB,
		owner,
		def.EvalDefID)
	if err != nil {
		return err
	}
	kept, count := Plan(have,
		rows,
		def.NumExamples,
		keepEdited)
	gen.Requested, gen.Kept = count, len(kept)
	record()
	if count == 0 {
		return nil
	}
	res, genErr := Generate(ctx,
		m.cfg.LLM,
		Request{
			Settings:    def.SynthSettings,
			EvalName:    name,
			Description: description,
			Count:       count,
			Keep:        kept,
			Limits:      m.cfg.Env().Limits,
		},
		func(r Result) {
			applyResult(gen, r)
			record()
		})
	applyResult(gen, res)
	if len(res.Cases) == 0 {
		return errOr(genErr, errors.New("the LLM wrote no valid case"))
	}
	cases := Merge(kept, res.Cases)
	data, err := evalJSON(name,
		description,
		cases)
	if err != nil {
		return err
	}
	if _, err := usereval.Save(ctx,
		m.cfg.Env(),
		owner,
		name,
		data); err != nil {
		return err
	}
	if err := m.cfg.Store.SetSynthCases(ctx,
		m.cfg.DB,
		owner,
		def.EvalDefID,
		Provenance(rows,
			kept,
			res.Cases,
			gen.ID,
			def.Version,
			time.Now().UTC())); err != nil {
		return err
	}
	return genErr
}

func errOr(err,
	other error) error {
	if err != nil {
		return err
	}
	return other
}

// applyResult copies the state of a Generate into the record of the generation.
func applyResult(g *core.SynthGeneration,
	r Result) {
	g.Generated = len(r.Cases)
	g.Calls = r.Calls
	g.InputTokens = r.InputTokens
	g.OutputTokens = r.OutputTokens
	g.CostUSD = r.CostUSD
}

// Plan decides what a generation does with the cases the eval has (have, in order) given where the generated ones come from (rows): the
// cases to keep, and how many new ones to generate so that the eval has want. With keepEdited the kept ones are those the user edited
// (the hash of the content is not the generated one) or added (no row); without it none is kept.
func Plan(have []core.KnowledgeCase,
	rows []core.SynthCase,
	want int,
	keepEdited bool) ([]core.KnowledgeCase, int) {
	if !keepEdited {
		return nil, want
	}
	kept := []core.KnowledgeCase{}
	for _, c := range have {
		if Classify(c, rows) != CaseGenerated {
			kept = append(kept, c)
		}
	}
	return kept, max(0, want-len(kept))
}

// Merge is the cases of the new file: the kept and the new ones, ordered by name.
func Merge(kept,
	generated []core.KnowledgeCase) []core.KnowledgeCase {
	out := append(append([]core.KnowledgeCase{}, kept...), generated...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Provenance is the rows of gb_synth_cases after a generation: the rows of the kept cases stay, the new cases get one of this generation.
func Provenance(old []core.SynthCase,
	kept,
	generated []core.KnowledgeCase,
	gen core.SynthGenerationID,
	version int,
	at time.Time) []core.SynthCase {
	byName := map[string]core.SynthCase{}
	for _, r := range old {
		byName[r.CaseName] = r
	}
	out := []core.SynthCase{}
	for _, c := range kept {
		if r, ok := byName[c.Name]; ok {
			out = append(out, r)
		}
	}
	for _, c := range generated {
		out = append(out, core.SynthCase{
			CaseName:     c.Name,
			GenerationID: gen,
			Version:      version,
			ContentHash:  core.CaseHash(c),
			CreatedAt:    at,
		})
	}
	return out
}

// CaseOrigin says where a case of a synthetic eval comes from.
type CaseOrigin string

// The origins of a case.
const (
	// CaseGenerated: as the generator wrote it.
	CaseGenerated CaseOrigin = "generated"
	// CaseEdited: generated, then changed by the user.
	CaseEdited CaseOrigin = "edited"
	// CaseUser: added by the user.
	CaseUser CaseOrigin = "user"
)

// Classify says where case c comes from, given the rows of gb_synth_cases.
func Classify(c core.KnowledgeCase,
	rows []core.SynthCase) CaseOrigin {
	for _, r := range rows {
		if r.CaseName == c.Name {
			if r.ContentHash == core.CaseHash(c) {
				return CaseGenerated
			}
			return CaseEdited
		}
	}
	return CaseUser
}

// CaseView is a case of a synthetic eval with where it comes from.
type CaseView struct {
	Name   string     `json:"name"`
	Origin CaseOrigin `json:"origin"`
	// Generation and Version are those that wrote it (0 for a case of the user).
	Generation core.SynthGenerationID `json:"generation,omitempty"`
	Version    int                    `json:"version,omitempty"`
	core.KnowledgeInput
	core.KnowledgeExpected
}

// Detail is a synthetic eval as the API shows it.
type Detail struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Origin      core.Origin `json:"origin"`
	core.SynthDef
	Versions    []core.SynthVersion    `json:"versions"`
	Generations []core.SynthGeneration `json:"generations"`
	Cases       []CaseView             `json:"cases"`
}

// Views returns the cases with where they come from.
func Views(cases []core.KnowledgeCase,
	rows []core.SynthCase) []CaseView {
	byName := map[string]core.SynthCase{}
	for _, r := range rows {
		byName[r.CaseName] = r
	}
	out := make([]CaseView, 0, len(cases))
	for _, c := range cases {
		v := CaseView{
			Name:              c.Name,
			Origin:            Classify(c, rows),
			KnowledgeInput:    c.KnowledgeInput,
			KnowledgeExpected: c.KnowledgeExpected,
		}
		if r, ok := byName[c.Name]; ok {
			v.Generation, v.Version = r.GenerationID, r.Version
		}
		out = append(out, v)
	}
	return out
}

// Get returns the synthetic eval name of the owner with its versions, its latest generations and its cases. It ends a generation that
// looks lost first (Sweep). evalsdb.ErrNotFound if there is no such synthetic eval.
func (m *Manager) Get(ctx context.Context,
	owner,
	name string) (*Detail, error) {
	ev, def, err := m.lookup(ctx,
		owner,
		name)
	if err != nil {
		return nil, err
	}
	versions, err := m.cfg.Store.ListSynthVersions(ctx,
		m.cfg.DB,
		owner,
		ev.ID)
	if err != nil {
		return nil, err
	}
	gens, err := m.cfg.Store.ListSynthGenerations(ctx,
		m.cfg.DB,
		owner,
		ev.ID,
		detailGenerations)
	if err != nil {
		return nil, err
	}
	// Only a generation that looks lost is swept (a read does not write otherwise: it would contend with the generation's own writes).
	if len(gens) > 0 && !gens[0].Status.Finished() && time.Since(gens[0].UpdatedAt) > m.cfg.StaleAfter {
		if _, err := m.Sweep(ctx); err != nil {
			log.WithError(err).Warn("synth: sweep")
		} else if gens, err = m.cfg.Store.ListSynthGenerations(ctx,
			m.cfg.DB,
			owner,
			ev.ID,
			detailGenerations); err != nil {
			return nil, err
		}
	}
	rows, err := m.cfg.Store.ListSynthCases(ctx,
		m.cfg.DB,
		owner,
		ev.ID)
	if err != nil {
		return nil, err
	}
	cases, err := m.existing(ctx,
		owner,
		name)
	if err != nil {
		return nil, err
	}
	return &Detail{
		Name:        name,
		Description: ev.Description,
		Origin:      core.OriginSynthetic,
		SynthDef:    *def,
		Versions:    versions,
		Generations: gens,
		Cases:       Views(cases, rows),
	}, nil
}

// EditCase replaces the case caseName of the synthetic eval name of the owner by the case in body (JSON of the shape of a case of an
// eval file; its name is kept). The file is validated and saved as a user eval is, so a case that is not valid is a *usereval.InvalidError
// and nothing changes. The case counts as edited from then on: a regeneration keeps it. evalsdb.ErrNotFound if the eval or the case
// does not exist.
func (m *Manager) EditCase(ctx context.Context,
	owner,
	name,
	caseName string,
	body []byte) (*CaseView, error) {
	ev, _, err := m.lookup(ctx,
		owner,
		name)
	if err != nil {
		return nil, err
	}
	cases, err := m.existing(ctx,
		owner,
		name)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(cases, func(c core.KnowledgeCase) bool { return c.Name == caseName })
	if i < 0 {
		return nil, fmt.Errorf("case %q: %w",
			caseName,
			evalsdb.ErrNotFound)
	}
	edited, err := ParseCase(body)
	if err != nil {
		return nil, invalid(evalfile.Problem(err.Error()))
	}
	edited.Name = caseName
	next := slices.Clone(cases)
	next[i] = edited
	data, err := evalJSON(name,
		ev.Description,
		next)
	if err != nil {
		return nil, err
	}
	if _, err := usereval.Save(ctx,
		m.cfg.Env(),
		owner,
		name,
		data); err != nil {
		return nil, err
	}
	rows, err := m.cfg.Store.ListSynthCases(ctx,
		m.cfg.DB,
		owner,
		ev.ID)
	if err != nil {
		return nil, err
	}
	v := Views([]core.KnowledgeCase{edited}, rows)[0]
	return &v, nil
}
