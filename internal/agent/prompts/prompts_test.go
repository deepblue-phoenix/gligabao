package prompts

import (
	"strings"
	"testing"
)

func TestPromptsAreLoaded(t *testing.T) {
	for name, p := range map[string]string{"Research": Research, "Knowledge": Knowledge, "Spawn": Spawn, "Messaging": Messaging, "WebSearch": WebSearch} {
		if strings.TrimSpace(p) == "" || p != strings.TrimSpace(p) {
			t.Errorf("%s: empty or not trimmed: %q", name, p)
		}
	}
}

// The spawn prompt is the policy of the main agent: it must tell it to decide whether spawning is warranted and to give a reason.
func TestSpawnPromptSaysWhenAndWhy(t *testing.T) {
	for _, want := range []string{"really warranted", "reason", "wait_subagents"} {
		if !strings.Contains(Spawn, want) {
			t.Errorf("the spawn prompt lacks %q", want)
		}
	}
}

func TestMessagingPromptMentionsTheTools(t *testing.T) {
	for _, want := range []string{"list_siblings", "send_message", "read_messages"} {
		if !strings.Contains(Messaging, want) {
			t.Errorf("the messaging prompt lacks %q", want)
		}
	}
}

func TestSubagent(t *testing.T) {
	p := Subagent("alpha", `covers "Asia" & {{x}}`)
	if !strings.Contains(p, `"alpha"`) || !strings.Contains(p, `covers "Asia" & {{x}}`) {
		t.Errorf("prompt: %s", p)
	}
}

func TestJoin(t *testing.T) {
	if got := Join("a\n", "", " b"); got != "a\n\nb" {
		t.Errorf("join: %q", got)
	}
}

func TestSearchPrefs(t *testing.T) {
	if got := Search(SearchPrefs{Recency: "any"}); got != "" {
		t.Errorf("nothing to say: %q", got)
	}
	got := Search(SearchPrefs{Allowed: []string{"a.com", "b.org"}, Preferred: []string{"c.com"}, Recency: "week", Language: "de", Instructions: "Primary sources."})
	for _, want := range []string{"## Search preferences", "Only these sites are available to you: a.com, b.org", "Prefer these sources when they have what you look for: c.com", "the last week", `"de"`, "- Primary sources."} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "never available") {
		t.Errorf("blocked sites are not set:\n%s", got)
	}
	if blocked := Search(SearchPrefs{Blocked: []string{"x.com"}}); !strings.Contains(blocked, "never available to you: x.com") || strings.Contains(blocked, "\n\n\n") {
		t.Errorf("blocked:\n%s", blocked)
	}
}

func TestRoles(t *testing.T) {
	got := Roles(RolesInfo{
		MaxPerAgent: 4, Messaging: true,
		OnDemand: []Role{{Name: "regional", Does: "covers a region.", When: "there are several regions", MaxInstances: 2}},
		OnStart:  []Role{{Name: "news", Does: "reads the news."}},
	})
	for _, want := range []string{"at most 4", "`regional`: covers a region. Start it when: there are several regions (at most 2 at once)", "`role` argument", "message each other", "started for you", "`news`: reads the news.", "wait_subagents"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	only := Roles(RolesInfo{MaxPerAgent: 5, OnStart: []Role{{Name: "news", Does: "d"}}})
	if strings.Contains(only, "spawn_subagent") || !strings.Contains(only, "started for you") {
		t.Errorf("on_start only:\n%s", only)
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
