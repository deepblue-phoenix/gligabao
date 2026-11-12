package prompts

import (
	"strings"
	"testing"
)

func TestPromptsAreLoaded(t *testing.T) {
	for name, p := range map[string]string{"Research": Research, "Knowledge": Knowledge, "Worker": Worker, "WebSearch": WebSearch} {
		if strings.TrimSpace(p) == "" || p != strings.TrimSpace(p) {
			t.Errorf("%s: empty or not trimmed: %q",
				name,
				p)
		}
	}
}

// The worker prompt says how the worker ends and what it must not do.
func TestWorkerPrompt(t *testing.T) {
	for _, want := range []string{"submit_findings", "refut", "URL"} {
		if !strings.Contains(Worker, want) {
			t.Errorf("the worker prompt lacks %q", want)
		}
	}
}

func TestJoin(t *testing.T) {
	if got := Join("a\n",
		"",
		" b"); got != "a\n\nb" {
		t.Errorf("join: %q", got)
	}
}

func TestSearchPrefs(t *testing.T) {
	if got := Search(SearchPrefs{
		Recency: "any",
	}); got != "" {
		t.Errorf("nothing to say: %q", got)
	}
	got := Search(SearchPrefs{
		Allowed:      []string{"a.com", "b.org"},
		Preferred:    []string{"c.com"},
		Recency:      "week",
		Language:     "de",
		Instructions: "Primary sources.",
	})
	for _, want := range []string{"## Search preferences", "Only these sites are available to you: a.com, b.org", "Prefer these sources when they have what you look for: c.com", "the last week", `"de"`, "- Primary sources."} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s",
				want,
				got)
		}
	}
	if strings.Contains(got, "never available") {
		t.Errorf("blocked sites are not set:\n%s", got)
	}
	if blocked := Search(SearchPrefs{
		Blocked: []string{"x.com"},
	}); !strings.Contains(blocked, "never available to you: x.com") || strings.Contains(blocked, "\n\n\n") {
		t.Errorf("blocked:\n%s", blocked)
	}
}

func TestSkills(t *testing.T) {
	if Skills(nil) != "" {
		t.Error("no skills")
	}
	if got := Skills([]Skill{{"a", "A text\n"}, {"b", "B text"}}); got != "## Skill: a\n\nA text\n\n## Skill: b\n\nB text" {
		t.Errorf("skills: %q", got)
	}
	if strings.TrimSpace(KnowledgeReadOnly) == "" || strings.Contains(KnowledgeReadOnly, "remember") && !strings.Contains(KnowledgeReadOnly, "cannot add") {
		t.Errorf("read-only knowledge prompt: %s", KnowledgeReadOnly)
	}
}
