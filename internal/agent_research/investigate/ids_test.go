package investigate

import "testing"

func TestNextIDs(t *testing.T) {
	cases := []struct {
		name, got, want string
	}{
		{"question empty", NextQuestionID(nil), "q_001"},
		{"question max+1", NextQuestionID([]string{"q_001", "q_007", "q_003"}), "q_008"},
		{"claim ignores other prefixes", NextClaimID([]string{"d_009", "c_002", "q_050"}), "c_003"},
		{"datapoint", NextDatapointID([]string{"c_001"}), "d_001"},
		{"source", NextSourceID([]string{"s_009"}), "s_010"},
		{"challenge", NextChallengeID([]string{"ch_001", "ch_002"}), "ch_003"},
		{"past 999", NextQuestionID([]string{"q_999"}), "q_1000"},
		{"garbage ignored", NextSourceID([]string{"s_x", "s_", "s_-4"}), "s_001"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, c.got, c.want)
		}
	}
}

func TestDefaultLimits(t *testing.T) {
	l := DefaultLimits()
	want := Limits{MaxDepth: 3, MaxAttempts: 4, MaxQuestions: 40, NotesMaxTokens: 6000, MinSources: 5, MinDomains: 3, MinIndependentSources: 2,
		MinChallenges: 1, MaxChallengesPerTarget: 3, MaxReopens: 1}
	if l != want {
		t.Fatalf("limits: %+v", l)
	}
}
