package agent_defs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"text/template"
	"unicode/utf8"
)

var (
	nameRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	roleRe     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`) // as agent.ValidSubagentName: it becomes the name of a sub-agent
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
	for i := range d.Spawn.Roles {
		r := &d.Spawn.Roles[i]
		r.Name, r.Agent = strings.TrimSpace(r.Name), strings.TrimSpace(r.Agent)
		r.Version, r.When, r.Does = strings.TrimSpace(r.Version), strings.TrimSpace(r.When), strings.TrimSpace(r.Does)
		r.Trigger = strings.ToLower(strings.TrimSpace(r.Trigger))
		if r.Trigger == "" {
			r.Trigger = TriggerOnDemand
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
	bad := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }

	if d.Schema != SchemaVersion {
		bad("schema: %d is not supported (this version reads schema %d)", d.Schema, SchemaVersion)
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
		bad("max_steps: %d must be between 1 and %d", d.MaxSteps, MaxStepsCeiling)
	}
	if d.MaxTokens < 1 || d.MaxTokens > maxTokensCeiling {
		bad("max_tokens: %d must be between 1 and %d", d.MaxTokens, maxTokensCeiling)
	}

	seen := map[string]bool{}
	for _, t := range d.Tools {
		switch {
		case t == "query" || t == "remember":
			bad("tools: %q is not listed here: the knowledge base tools come from [knowledge] access", t)
		case !contains(KnownTools, t):
			bad("tools: unknown tool %q (known: %s)", t, strings.Join(KnownTools, ", "))
		case seen[t]:
			bad("tools: %q is listed twice", t)
		}
		seen[t] = true
	}
	errs = append(errs, checkSearch(d)...)
	errs = append(errs, checkKnowledge(d)...)

	for key, v := range map[string]float64{"limits.max_cost_usd": d.Limits.MaxCostUSD, "limits.max_tree_cost_usd": d.Limits.MaxTreeCostUSD} {
		if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			bad("%s: %v must be 0 (no cap) or a positive number of USD", key, v)
		}
	}
	errs = append(errs, checkSpawn(d)...)
	return errs
}

func checkSearch(d *Def) []string {
	var errs []string
	bad := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	s := d.Search
	domains := func(key string, list []string) {
		seen := map[string]bool{}
		for _, h := range list {
			switch {
			case !domainRe.MatchString(h):
				bad("%s: %q is not a host name like example.com (no scheme, no path)", key, h)
			case seen[h]:
				bad("%s: %q is listed twice", key, h)
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
		bad("search.max_uses_per_call: %d must be between 1 and %d", s.MaxUsesPerCall, maxSearchUses)
	}
	if s.MaxCalls < 0 {
		bad("search.max_calls: %d must not be negative", s.MaxCalls)
	}
	if s.Fetch.MaxPageChars < minPageChars || s.Fetch.MaxPageChars > maxPageChars {
		bad("search.fetch.max_page_chars: %d must be between %d and %d", s.Fetch.MaxPageChars, minPageChars, maxPageChars)
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
	bad := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
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

func checkSpawn(d *Def) []string {
	var errs []string
	bad := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	sp := d.Spawn
	limits := []struct {
		key        string
		v, ceiling int
	}{{"spawn.max_depth", sp.MaxDepth, MaxSubagentDepth}, {"spawn.max_per_agent", sp.MaxPerAgent, MaxSubagentsPerAgent}, {"spawn.max_per_tree", sp.MaxPerTree, MaxSubagentsPerTree}}
	for _, l := range limits {
		if l.v < 0 || l.v > l.ceiling {
			bad("%s: %d must be between 0 (the ceiling) and %d, the ceiling", l.key, l.v, l.ceiling)
		}
	}
	if !sp.Enabled {
		if sp.Messaging || sp.MaxDepth != 0 || sp.MaxPerAgent != 0 || sp.MaxPerTree != 0 || len(sp.Roles) > 0 {
			bad("spawn: settings or roles are given but enabled is false")
		}
		return errs
	}
	names := map[string]bool{}
	onStart := 0
	for i, r := range sp.Roles {
		key := fmt.Sprintf("spawn.roles[%d]", i)
		if r.Name != "" {
			key += " (" + r.Name + ")"
		}
		switch {
		case !roleRe.MatchString(r.Name):
			bad("%s.name: %q must be 1 to 40 letters, digits, - or _", key, r.Name)
		case names[r.Name]:
			bad("%s.name: the role is defined twice", key)
		}
		names[r.Name] = true
		if !nameRe.MatchString(r.Agent) {
			bad("%s.agent: %q is not a valid agent name", key, r.Agent)
		}
		if r.Version != "" {
			if err := checkConstraint(r.Version); err != nil {
				bad("%s.version: %v", key, err)
			}
		}
		switch {
		case r.Does == "":
			bad("%s.does: required (what the sub-agent does, in a sentence)", key)
		case utf8.RuneCountInString(r.Does) > 300:
			bad("%s.does: too long (at most 300 characters)", key)
		}
		if utf8.RuneCountInString(r.When) > 600 {
			bad("%s.when: too long (at most 600 characters)", key)
		}
		if r.MaxInstances < 0 || r.MaxInstances > MaxSubagentsPerAgent {
			bad("%s.max_instances: %d must be between 0 and %d", key, r.MaxInstances, MaxSubagentsPerAgent)
		}
		if r.MaxSteps < 0 || r.MaxSteps > MaxStepsCeiling || r.MaxStepsLimit < 0 || r.MaxStepsLimit > MaxStepsCeiling {
			bad("%s: max_steps and max_steps_limit must be between 0 and %d", key, MaxStepsCeiling)
		}
		if r.MaxSteps > 0 && r.MaxStepsLimit > 0 && r.MaxSteps > r.MaxStepsLimit {
			bad("%s: max_steps (%d) is above max_steps_limit (%d)", key, r.MaxSteps, r.MaxStepsLimit)
		}
		switch r.Trigger {
		case TriggerOnDemand:
			if r.When == "" {
				bad("%s.when: required for an on_demand role (it tells the model when to start the sub-agent)", key)
			}
			if r.TaskTemplate != "" {
				bad("%s.task_template: only an on_start role has one (the model writes the task of an on_demand sub-agent)", key)
			}
		case TriggerOnStart:
			onStart++
			if r.MaxInstances > 1 {
				bad("%s.max_instances: an on_start role starts exactly one sub-agent", key)
			}
			if _, err := template.New("task").Option("missingkey=error").Parse(r.TaskTemplate); err != nil {
				bad("%s.task_template: %v", key, err)
			} else if _, err := r.Task("topic"); err != nil {
				bad("%s.task_template: %v", key, err)
			}
		default:
			bad("%s.trigger: %q must be on_demand or on_start", key, r.Trigger)
		}
	}
	if onStart > d.MaxPerAgent() {
		bad("spawn.roles: %d on_start roles are more than the %d sub-agents an agent may have", onStart, d.MaxPerAgent())
	}
	return errs
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// hashOf is the SHA-256 of the definition without its Source and Hash.
func hashOf(d *Def) string {
	c := *d
	c.Source, c.Hash = "", ""
	b, err := json.Marshal(&c)
	if err != nil {
		panic("agent_defs: " + err.Error()) // a Def is always marshalable
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
