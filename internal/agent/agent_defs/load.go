package agent_defs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	log "github.com/sirupsen/logrus"
)

// DirName is the name of the directory of the definitions next to the binary; SkillsDir is the directory of the skill files in it.
const (
	DirName   = "agent_defs"
	SkillsDir = "skills"
)

// DefaultDir is the directory of the definitions next to the running binary (DirName in the working directory if the binary's location
// cannot be found).
func DefaultDir() string {
	exe, err := os.Executable()
	if err != nil {
		return DirName
	}
	return filepath.Join(filepath.Dir(exe), DirName)
}

// fileDef is what a definition file holds (TOML or JSON): the fields of Def, with the prompt and the skills as paths and names.
type fileDef struct {
	Schema           int          `toml:"schema" json:"schema"`
	Name             string       `toml:"name" json:"name"`
	Version          string       `toml:"version" json:"version"`
	Description      string       `toml:"description" json:"description"`
	SystemPrompt     string       `toml:"system_prompt" json:"system_prompt"`
	SystemPromptFile string       `toml:"system_prompt_file" json:"system_prompt_file"`
	Model            string       `toml:"model" json:"model"`
	MaxSteps         int          `toml:"max_steps" json:"max_steps"`
	MaxTokens        int          `toml:"max_tokens" json:"max_tokens"`
	Tools            []string     `toml:"tools" json:"tools"`
	Skills           []string     `toml:"skills" json:"skills"`
	RequiredSkills   []string     `toml:"required_skills" json:"required_skills"`
	Search           Search       `toml:"search" json:"search"`
	Knowledge        Knowledge    `toml:"knowledge" json:"knowledge"`
	Limits           Limits       `toml:"limits" json:"limits"`
	Spawn            Spawn        `toml:"spawn" json:"spawn"`
	Loop             string       `toml:"loop" json:"loop"`
	Investigate      *Investigate `toml:"investigate" json:"investigate"`
	Code             *Code        `toml:"code" json:"code"`
	Schedule         *Schedule    `toml:"schedule" json:"schedule"`
}

// Load reads the definitions of dir: every *.toml and *.json file in it is one agent (the subdirectories hold what the files refer to:
// the prompts and, in SkillsDir, the skills). A directory that does not exist is no error: only the built-in default is there. A file
// that is not valid is: Load reports all the problems of all the files at once, because an agent that silently does not load is worse
// than a server that does not start.
func Load(dir string) (*Registry, error) {
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		log.WithField("dir", dir).Info("agent definitions: no directory; only the built-in default agent is available")
		return newRegistry("", nil)
	case err != nil:
		return nil, fmt.Errorf("agent definitions: %w", err)
	case !info.IsDir():
		return nil, fmt.Errorf("agent definitions: %s is not a directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("agent definitions: %w", err)
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("agent definitions: %w", err)
	}
	var defs []*Def
	var errs []error
	byName := map[string]string{}
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || (ext != ".toml" && ext != ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		d, problems := loadFile(path, root)
		if len(problems) > 0 {
			for _, p := range problems {
				errs = append(errs, fmt.Errorf("%s: %s", path, p))
			}
			continue
		}
		if other, dup := byName[d.Name]; dup {
			errs = append(errs, fmt.Errorf("%s: the name %q is also the name of %s", path, d.Name, other))
			continue
		}
		byName[d.Name] = path
		defs = append(defs, d)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("agent definitions: %w", err)
	}
	if other, ok := byName[DefaultName]; ok {
		log.WithField("file", other).Warn("agent definitions: this file replaces the built-in default agent")
	}
	reg, err := newRegistry(dir, defs)
	if err != nil {
		return nil, fmt.Errorf("agent definitions: %w", err)
	}
	for _, d := range reg.List() {
		log.WithFields(log.Fields{"agent": d.Name, "version": d.Version, "hash": d.Short(), "source": d.Source, "skills": len(d.Skills)}).Info("agent definition loaded")
	}
	return reg, nil
}

// loadFile reads and checks one file. root is the real path of the definitions directory: the prompt and skill files must stay in it.
func loadFile(path, root string) (*Def, []string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, []string{err.Error()}
	}
	var f fileDef
	if strings.EqualFold(filepath.Ext(path), ".json") {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			return nil, []string{"invalid JSON: " + err.Error()}
		}
		if _, err := dec.Token(); !errors.Is(err, io.EOF) {
			return nil, []string{"invalid JSON: more than one value"}
		}
	} else {
		md, err := toml.Decode(string(raw), &f)
		if err != nil {
			return nil, []string{"invalid TOML: " + err.Error()}
		}
		if un := md.Undecoded(); len(un) > 0 {
			keys := make([]string, len(un))
			for i, k := range un {
				keys[i] = k.String()
			}
			return nil, []string{"unknown key(s): " + strings.Join(keys, ", ")}
		}
	}

	var problems []string
	d := &Def{
		Schema: f.Schema, Name: f.Name, Version: f.Version, Description: f.Description, SystemPrompt: f.SystemPrompt, Model: f.Model,
		MaxSteps: f.MaxSteps, MaxTokens: f.MaxTokens, Tools: f.Tools, Search: f.Search, Knowledge: f.Knowledge, Limits: f.Limits, Spawn: f.Spawn, Loop: f.Loop, Investigate: f.Investigate, Code: f.Code, Schedule: f.Schedule,
		Source: path,
	}
	switch {
	case f.SystemPrompt != "" && f.SystemPromptFile != "":
		problems = append(problems, "system_prompt and system_prompt_file: give one of them, not both")
	case f.SystemPromptFile != "":
		text, err := readInside(root, filepath.Dir(path), f.SystemPromptFile, maxPromptBytes)
		if err != nil {
			problems = append(problems, "system_prompt_file: "+err.Error())
		}
		d.SystemPrompt = text
	case f.SystemPrompt == "":
		problems = append(problems, "system_prompt or system_prompt_file: one is required")
	}
	skills, skillProblems := loadSkills(root, f.Skills, f.RequiredSkills)
	d.Skills = skills
	problems = append(problems, skillProblems...)

	normalize(d)
	for _, p := range check(d) {
		if strings.HasPrefix(p, "system_prompt: empty") && len(problems) > 0 {
			continue // already said, with the reason
		}
		problems = append(problems, p)
	}
	if len(problems) > 0 {
		return nil, problems
	}
	d.Hash = hashOf(d)
	return d, nil
}

// loadSkills reads the skill files by name from the skills directory: a missing optional skill is a warning, a missing required one an
// error. The required skills come first in the prompt, then the optional ones, each in the order of the file.
func loadSkills(root string, optional, required []string) ([]Skill, []string) {
	var skills []Skill
	var problems []string
	seen := map[string]bool{}
	read := func(name string, must bool) {
		key := "skills"
		if must {
			key = "required_skills"
		}
		switch {
		case !skillRe.MatchString(name):
			problems = append(problems, fmt.Sprintf("%s: %q must be the name of a file of the skills directory without .md (lowercase letters, digits, - and _)", key, name))
			return
		case seen[name]:
			problems = append(problems, fmt.Sprintf("%s: %q is listed twice", key, name))
			return
		}
		seen[name] = true
		text, err := readInside(root, filepath.Join(root, SkillsDir), name+".md", maxSkillBytes)
		switch {
		case errors.Is(err, os.ErrNotExist) && !must:
			log.WithField("skill", name).Warn("agent definitions: skill file not found, the agent starts without it")
		case err != nil:
			problems = append(problems, fmt.Sprintf("%s: %s: %v", key, name, err))
		case text == "":
			problems = append(problems, fmt.Sprintf("%s: %s: the file is empty", key, name))
		default:
			skills = append(skills, Skill{Name: name, Content: text})
		}
	}
	for _, n := range required {
		read(n, true)
	}
	for _, n := range optional {
		read(n, false)
	}
	return skills, problems
}

// readInside reads the file rel (relative to base) as text, refusing a path that leaves root (after links are resolved) and a file over
// max bytes. A missing file is an error that wraps os.ErrNotExist.
func readInside(root, base, rel string, max int) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("%q must be a path relative to the file, inside the definitions directory", rel)
	}
	if c := filepath.Clean(rel); c == ".." || strings.HasPrefix(c, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q is outside the definitions directory", rel)
	}
	path := filepath.Join(base, rel)
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%q: %w", rel, os.ErrNotExist)
		}
		return "", err
	}
	if within, err := filepath.Rel(root, real); err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q is outside the definitions directory", rel)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a file", rel)
	}
	if info.Size() > int64(max) {
		return "", fmt.Errorf("%q is too big (%d bytes, at most %d)", rel, info.Size(), max)
	}
	b, err := os.ReadFile(real)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
