package agent_defs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/deepblue-phoenix/gligabao/internal/agent/code_sandbox/hostfn"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	nameRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	skillRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	domainRe   = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	languageRe = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
	countryRe  = regexp.MustCompile(`^[A-Za-z]{2}$`)
	datasetRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`) // as memory_core.ResolveDatasetName
)

// ValidName reports whether s is a valid name of an agent definition.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// normalize fills the defaults a file leaves out. It is idempotent, so a definition that went through a Bundle comes out the same.
func normalize(d *Def) {
	if d.Schema == 0 {
		d.Schema = SchemaVersion
	}
	d.Name = strings.TrimSpace(d.Name)
	d.Version = strings.TrimSpace(d.Version)
	d.Description = strings.TrimSpace(d.Description)
	d.SystemPrompt = strings.TrimSpace(d.SystemPrompt)
	d.Model = strings.TrimSpace(d.Model)
	if d.MaxSteps == 0 {
		d.MaxSteps = DefaultMaxSteps
	}
	if d.MaxTokens == 0 {
		d.MaxTokens = DefaultMaxTokens
	}
	d.Loop = strings.ToLower(strings.TrimSpace(d.Loop))
	if d.Loop == LoopSimple { // "" is the simple loop: keep one form, so that the hash of an agent does not depend on how it says so
		d.Loop = ""
	}
	if d.Tools == nil {
		d.Tools = append([]string(nil), KnownTools...)
	}
	s := &d.Search
	s.AllowedDomains, s.BlockedDomains, s.PreferredDomains = lowerAll(s.AllowedDomains), lowerAll(s.BlockedDomains), lowerAll(s.PreferredDomains)
	s.Fetch.AllowedDomains, s.Fetch.BlockedDomains = lowerAll(s.Fetch.AllowedDomains), lowerAll(s.Fetch.BlockedDomains)
	s.Recency, s.Language = strings.ToLower(strings.TrimSpace(s.Recency)), strings.TrimSpace(s.Language)
	s.Instructions = strings.TrimSpace(s.Instructions)
	if s.Recency == "" {
		s.Recency = RecencyAny
	}
	if s.MaxUsesPerCall == 0 {
		s.MaxUsesPerCall = DefaultSearchUses
	}
	if s.Fetch.MaxPageChars == 0 {
		s.Fetch.MaxPageChars = DefaultMaxPageChars
	}
	if l := s.Location; l != nil {
		l.Country = strings.ToUpper(strings.TrimSpace(l.Country))
		l.Region, l.City, l.Timezone = strings.TrimSpace(l.Region), strings.TrimSpace(l.City), strings.TrimSpace(l.Timezone)
		if *l == (Location{}) {
			s.Location = nil
		}
	}
	k := &d.Knowledge
	k.Access = strings.ToLower(strings.TrimSpace(k.Access))
	if k.Access == "" {
		k.Access = AccessReadWrite
	}
	k.Dataset, k.Instructions = strings.TrimSpace(k.Dataset), strings.TrimSpace(k.Instructions)
	if sc := d.Schedule; sc != nil {
		sc.Cron, sc.Timezone, sc.Topic, sc.User = strings.TrimSpace(sc.Cron), strings.TrimSpace(sc.Timezone), strings.TrimSpace(sc.Topic), strings.TrimSpace(sc.User)
		if sc.Timezone == "" {
			sc.Timezone = DefaultScheduleTimezone
		}
		if sc.User == "" {
			sc.User = DefaultScheduleUser
		}
	}
}

func lowerAll(list []string) []string {
	if len(list) == 0 {
		return nil
	}
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = strings.ToLower(strings.TrimSpace(s))
	}
	return out
}

// check collects the problems of a definition on its own (see Registry for those between definitions); each is "key: what is wrong".
func check(d *Def) []string {
	var errs []string
	bad := func(format string,
		args ...any) {
		errs = append(errs, fmt.Sprintf(format, args...))
	}

	if d.Schema != SchemaVersion {
		bad("schema: %d is not supported (this version reads schema %d)",
			d.Schema,
			SchemaVersion)
	}
	switch {
	case d.Name == "":
		bad("name: required")
	case !nameRe.MatchString(d.Name):
		bad("name: %q must be 1 to 40 lowercase letters, digits, - or _, starting with a letter or digit", d.Name)
	}
	if _, err := parseVersion(d.Version); err != nil {
		bad("version: %v (a semantic version is required)", err)
	}
	switch {
	case d.Description == "":
		bad("description: required")
	case utf8.RuneCountInString(d.Description) > maxTextChars:
		bad("description: too long (at most %d characters)", maxTextChars)
	}
	switch {
	case d.SystemPrompt == "":
		bad("system_prompt: empty")
	case len(d.SystemPrompt) > maxPromptBytes:
		bad("system_prompt: too long (at most %d bytes)", maxPromptBytes)
	}
	if len(d.Model) > 100 || strings.ContainsAny(d.Model, " \t\r\n") {
		bad("model: %q is not a valid model name", d.Model)
	}
	if d.MaxSteps < 1 || d.MaxSteps > MaxStepsCeiling {
		bad("max_steps: %d must be between 1 and %d",
			d.MaxSteps,
			MaxStepsCeiling)
	}
	if d.MaxTokens < 1 || d.MaxTokens > maxTokensCeiling {
		bad("max_tokens: %d must be between 1 and %d",
			d.MaxTokens,
			maxTokensCeiling)
	}
	for _, p := range d.checkLoop() {
		bad("%s", p)
	}

	seen := map[string]bool{}
	for _, t := range d.Tools {
		switch {
		case t == "query" || t == "remember":
			bad("tools: %q is not listed here: the knowledge base tools come from [knowledge] access", t)
		case !contains(KnownTools, t):
			bad("tools: unknown tool %q (known: %s)",
				t,
				strings.Join(KnownTools, ", "))
		case seen[t]:
			bad("tools: %q is listed twice", t)
		}
		seen[t] = true
	}
	errs = append(errs, checkSearch(d)...)
	errs = append(errs, checkKnowledge(d)...)

	for key, v := range map[string]float64{"limits.max_cost_usd": d.Limits.MaxCostUSD, "limits.max_tree_cost_usd": d.Limits.MaxTreeCostUSD} {
		if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			bad("%s: %v must be 0 (no cap) or a positive number of USD",
				key,
				v)
		}
	}
	errs = append(errs, checkSchedule(d)...)
	return errs
}

func checkSearch(d *Def) []string {
	var errs []string
	bad := func(format string,
		args ...any) {
		errs = append(errs, fmt.Sprintf(format, args...))
	}
	s := d.Search
	domains := func(key string,
		list []string) {
		seen := map[string]bool{}
		for _, h := range list {
			switch {
			case !domainRe.MatchString(h):
				bad("%s: %q is not a host name like example.com (no scheme, no path)",
					key,
					h)
			case seen[h]:
				bad("%s: %q is listed twice",
					key,
					h)
			}
			seen[h] = true
		}
	}
	domains("search.allowed_domains", s.AllowedDomains)
	domains("search.blocked_domains", s.BlockedDomains)
	domains("search.preferred_domains", s.PreferredDomains)
	domains("search.fetch.allowed_domains", s.Fetch.AllowedDomains)
	domains("search.fetch.blocked_domains", s.Fetch.BlockedDomains)
	if len(s.AllowedDomains) > 0 && len(s.BlockedDomains) > 0 {
		bad("search: allowed_domains and blocked_domains cannot be used together for web_search (the search tool takes one of them)")
	}
	if len(s.Fetch.AllowedDomains) > 0 && len(s.Fetch.BlockedDomains) > 0 {
		bad("search.fetch: allowed_domains and blocked_domains cannot be used together")
	}
	switch s.Recency {
	case RecencyAny, RecencyDay, RecencyWeek, RecencyMonth, RecencyYear:
	default:
		bad("search.recency: %q must be any, day, week, month or year", s.Recency)
	}
	if s.Language != "" && !languageRe.MatchString(s.Language) {
		bad("search.language: %q is not a language code like en or pt-BR", s.Language)
	}
	if utf8.RuneCountInString(s.Instructions) > maxTextChars {
		bad("search.instructions: too long (at most %d characters)", maxTextChars)
	}
	if l := s.Location; l != nil {
		if l.Country != "" && !countryRe.MatchString(l.Country) {
			bad("search.location.country: %q is not an ISO 3166-1 alpha-2 code like DE", l.Country)
		}
		for key, v := range map[string]string{"region": l.Region, "city": l.City, "timezone": l.Timezone} {
			if utf8.RuneCountInString(v) > 100 {
				bad("search.location.%s: too long (at most 100 characters)", key)
			}
		}
	}
	if s.MaxUsesPerCall < 1 || s.MaxUsesPerCall > maxSearchUses {
		bad("search.max_uses_per_call: %d must be between 1 and %d",
			s.MaxUsesPerCall,
			maxSearchUses)
	}
	if s.MaxCalls < 0 {
		bad("search.max_calls: %d must not be negative", s.MaxCalls)
	}
	if s.Fetch.MaxPageChars < minPageChars || s.Fetch.MaxPageChars > maxPageChars {
		bad("search.fetch.max_page_chars: %d must be between %d and %d",
			s.Fetch.MaxPageChars,
			minPageChars,
			maxPageChars)
	}
	if s.Fetch.MaxCalls < 0 {
		bad("search.fetch.max_calls: %d must not be negative", s.Fetch.MaxCalls)
	}
	if !d.HasTool(ToolWebSearch) && (len(s.AllowedDomains) > 0 || len(s.BlockedDomains) > 0 || s.Location != nil || s.MaxCalls > 0) {
		bad("search: the domain, location and call limits of web_search are set but web_search is not in tools")
	}
	if !d.HasTool(ToolFetchURL) && (len(s.Fetch.AllowedDomains) > 0 || len(s.Fetch.BlockedDomains) > 0 || s.Fetch.MaxCalls > 0) {
		bad("search.fetch: limits of fetch_url are set but fetch_url is not in tools")
	}
	return errs
}

func checkKnowledge(d *Def) []string {
	var errs []string
	bad := func(format string,
		args ...any) {
		errs = append(errs, fmt.Sprintf(format, args...))
	}
	k := d.Knowledge
	switch k.Access {
	case AccessReadWrite, AccessReadOnly, AccessNone:
	default:
		bad("knowledge.access: %q must be read_write, read_only or none", k.Access)
	}
	if k.Dataset != "" && !datasetRe.MatchString(k.Dataset) {
		bad("knowledge.dataset: %q must be 1-64 characters of letters, digits, '.', '_' or '-', starting with a letter or digit", k.Dataset)
	}
	if k.DatasetLocked && k.Dataset == "" {
		bad("knowledge.dataset_locked: needs a dataset to lock to")
	}
	if utf8.RuneCountInString(k.Instructions) > maxTextChars {
		bad("knowledge.instructions: too long (at most %d characters)", maxTextChars)
	}
	if k.Access == AccessNone && (k.Dataset != "" || k.DatasetLocked || k.QueryFirst || k.Instructions != "") {
		bad("knowledge: settings are given but access is none")
	}
	return errs
}

func checkSchedule(d *Def) []string {
	sc := d.Schedule
	if sc == nil {
		return nil
	}
	var errs []string
	bad := func(format string,
		args ...any) {
		errs = append(errs, fmt.Sprintf(format, args...))
	}
	loc, locErr := time.LoadLocation(sc.Timezone)
	if locErr != nil {
		bad("schedule.timezone: %q is not an IANA time zone like Europe/Berlin", sc.Timezone)
	}
	if sc.Cron == "" {
		bad("schedule.cron: required")
	} else if c, err := ParseCron(sc.Cron); err != nil {
		bad("schedule.cron: %v", err)
	} else if locErr == nil {
		from := time.Date(2030,
			1,
			1,
			0,
			0,
			0,
			0,
			loc)
		if c.Next(from).IsZero() {
			bad("schedule.cron: %q never matches a date", sc.Cron)
		} else if gap := c.MinGap(from); gap > 0 && gap < MinScheduleInterval {
			bad("schedule.cron: %q runs the agent every %v, at least %v is needed between two runs",
				sc.Cron,
				gap,
				MinScheduleInterval)
		}
	}
	switch {
	case sc.Topic == "":
		bad("schedule.topic: required (what every scheduled run researches)")
	case utf8.RuneCountInString(sc.Topic) > 2000:
		bad("schedule.topic: too long (at most 2000 characters)")
	case locErr == nil:
		if _, err := sc.TopicAt(time.Now()); err != nil {
			bad("schedule.topic: %v", err)
		}
	}
	if len(sc.User) > 128 || strings.ContainsFunc(sc.User, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		bad("schedule.user: %q must be at most 128 bytes, without control characters", sc.User)
	}
	return errs
}

func contains(list []string,
	s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// hashOf is the SHA-256 of the definition without its Source, Hash and Schedule.
func hashOf(d *Def) string {
	c := *d
	c.Source, c.Hash, c.Schedule = "", "", nil
	b, err := json.Marshal(&c)
	if err != nil {
		panic("agent_defs: " + err.Error()) // a Def is always marshalable
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// checkLoop returns what is wrong with the loop and its knobs.
func (d *Def) checkLoop() []string {
	var out []string
	switch d.Loop {
	case "":
		if d.Investigate != nil {
			out = append(out, `investigate: the knobs only take effect with loop = "investigative"; set the loop or remove the table`)
		}
		if d.Code != nil {
			out = append(out, `code: run_code is a tool of the investigative loop; set loop = "investigative" or remove the table`)
		}
		return out
	case LoopInvestigative:
	default:
		return []string{fmt.Sprintf(`loop: %q must be "simple" or "investigative"`, d.Loop)}
	}
	if v := d.Investigate; v != nil {
		out = append(out, v.Check()...)
	}
	if c := d.Code; c != nil {
		out = append(out, c.Check()...)
	}
	return out
}

// Check returns what is wrong with the [code] table (each number is 0, the default, or in its range).
func (c Code) Check() []string {
	var out []string
	if !c.Enabled {
		out = append(out, "code: the table needs enabled = true (remove it to have no code tool)")
	}
	in := func(name string,
		value,
		lo,
		hi int) {
		if value != 0 && (value < lo || value > hi) {
			out = append(out, fmt.Sprintf("code.%s: %d must be between %d and %d (or 0 for the default)",
				name,
				value,
				lo,
				hi))
		}
	}
	in("timeout_s",
		c.TimeoutS,
		1,
		60)
	in("max_memory_mb",
		c.MaxMemoryMB,
		8,
		1024)
	in("max_recursion",
		c.MaxRecursion,
		10,
		5000)
	in("max_output_chars",
		c.MaxOutputChars,
		200,
		20000)
	in("max_host_calls",
		c.MaxHostCalls,
		1,
		1000)
	in("max_result_chars",
		c.MaxResultChars,
		100,
		1000000)
	if c.MaxCalls != nil && (*c.MaxCalls < 0 || *c.MaxCalls > 1000) {
		out = append(out, fmt.Sprintf("code.max_calls: %d must be between 0 (no limit) and 1000", *c.MaxCalls))
	}
	if _, err := hostfn.Standard().Only(c.Functions); err != nil {
		out = append(out, "code.functions: "+err.Error())
	}
	return out
}

// Check returns what is wrong with the knobs (each of them is 0, the default, or in its range).
func (v Investigate) Check() []string {
	var out []string
	in := func(name string,
		value,
		lo,
		hi int) {
		if value != 0 && (value < lo || value > hi) {
			out = append(out, fmt.Sprintf("investigate.%s: %d must be between %d and %d (or 0 for the default)",
				name,
				value,
				lo,
				hi))
		}
	}
	in("max_depth",
		v.MaxDepth,
		1,
		6)
	in("max_attempts",
		v.MaxAttempts,
		1,
		10)
	in("max_questions",
		v.MaxQuestions,
		2,
		200)
	in("max_iterations",
		v.MaxIterations,
		2,
		300)
	in("max_total_tokens",
		v.MaxTotalTokens,
		1000,
		1<<30)
	in("keep_turns",
		v.KeepTurns,
		1,
		5)
	in("extraction_threshold",
		v.ExtractionThreshold,
		200,
		100000)
	in("notes_max_tokens",
		v.NotesMaxTokens,
		500,
		50000)
	in("min_sources",
		v.MinSources,
		1,
		100)
	in("min_domains",
		v.MinDomains,
		1,
		100)
	in("min_independent_sources",
		v.MinIndependentSources,
		1,
		10)
	in("min_challenges",
		v.MinChallenges,
		1,
		10)
	in("max_challenges_per_target",
		v.MaxChallengesPerTarget,
		1,
		10)
	in("max_reopens",
		v.MaxReopens,
		1,
		5)
	in("max_parallel",
		v.MaxParallel,
		1,
		MaxParallelCeiling)
	in("worker_max_steps",
		v.WorkerMaxSteps,
		1,
		MaxStepsCeiling)
	in("worker_context_tokens",
		v.WorkerContextTokens,
		100,
		maxWorkerContextTokens)
	in("max_workers",
		v.MaxWorkers,
		1,
		MaxWorkersCeiling)
	if v.MinPriorityToDelegate < 0 || v.MinPriorityToDelegate > 1 || math.IsNaN(v.MinPriorityToDelegate) {
		out = append(out, "investigate.min_priority_to_delegate: must be between 0 and 1 (0 for no minimum)")
	}
	if v.SynthesisReserve < 0 || v.SynthesisReserve > 0.5 || math.IsNaN(v.SynthesisReserve) {
		out = append(out, "investigate.synthesis_reserve: must be between 0 and 0.5 (0 for the default 0.15)")
	}
	if v.GatePriority < 0 || v.GatePriority > 1 || math.IsNaN(v.GatePriority) {
		out = append(out, "investigate.gate_priority: must be between 0 and 1 (0 for the default 0.5)")
	}
	return out
}

// Merge returns v with the knobs that o sets (not 0) in place of its own: the run options over the definition.
func (v Investigate) Merge(o Investigate) Investigate {
	pick := func(a,
		b int) int {
		if b != 0 {
			return b
		}
		return a
	}
	pickF := func(a,
		b float64) float64 {
		if b != 0 {
			return b
		}
		return a
	}
	return Investigate{
		MaxDepth:               pick(v.MaxDepth, o.MaxDepth),
		MaxAttempts:            pick(v.MaxAttempts, o.MaxAttempts),
		MaxQuestions:           pick(v.MaxQuestions, o.MaxQuestions),
		MaxIterations:          pick(v.MaxIterations, o.MaxIterations),
		MaxTotalTokens:         pick(v.MaxTotalTokens, o.MaxTotalTokens),
		KeepTurns:              pick(v.KeepTurns, o.KeepTurns),
		ExtractionThreshold:    pick(v.ExtractionThreshold, o.ExtractionThreshold),
		NotesMaxTokens:         pick(v.NotesMaxTokens, o.NotesMaxTokens),
		MinSources:             pick(v.MinSources, o.MinSources),
		MinDomains:             pick(v.MinDomains, o.MinDomains),
		MinIndependentSources:  pick(v.MinIndependentSources, o.MinIndependentSources),
		MinChallenges:          pick(v.MinChallenges, o.MinChallenges),
		MaxChallengesPerTarget: pick(v.MaxChallengesPerTarget, o.MaxChallengesPerTarget),
		MaxReopens:             pick(v.MaxReopens, o.MaxReopens),
		MaxParallel:            pick(v.MaxParallel, o.MaxParallel),
		WorkerMaxSteps:         pick(v.WorkerMaxSteps, o.WorkerMaxSteps),
		WorkerContextTokens:    pick(v.WorkerContextTokens, o.WorkerContextTokens),
		MaxWorkers:             pick(v.MaxWorkers, o.MaxWorkers),
		MinPriorityToDelegate:  pickF(v.MinPriorityToDelegate, o.MinPriorityToDelegate),
		SynthesisReserve:       pickF(v.SynthesisReserve, o.SynthesisReserve),
		GatePriority:           pickF(v.GatePriority, o.GatePriority),
	}
}
