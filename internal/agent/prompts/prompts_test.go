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
