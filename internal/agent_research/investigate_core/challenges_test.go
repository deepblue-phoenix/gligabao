package investigate_core

import (
	"errors"
	"strings"
	"testing"
)

func challengeFixture(t *testing.T) (Frontier, Notes) {
	t.Helper()
	f := mustRoot(t)
	f, a := mustAdd(t, f, "q_001", in("What is the memory use of the model?", 0.8))
	f = inProgress(t, f, a)
	n, _, err := Append(Notes{
		Objective: "o",
	}, SectionUnverified, []Entry{{
		Text:       "The model needs 11 GB at fp16",
		QuestionID: a,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return f, n
}

func openIn(strategy Strategy, query string) OpenInput {
	return OpenInput{
		TargetType:      TargetClaim,
		TargetID:        "c_001",
		Negation:        "the model needs substantially more than 11 GB",
		Strategy:        strategy,
		ExpectedIfFalse: "benchmarks report higher peak memory",
		Query:           query,
	}
}

func TestOpenValidation(t *testing.T) {
	f, n := challengeFixture(t)
	limits := DefaultLimits()
	chs, ch, err := Open(nil, openIn(StrategyDiscrepancy, "model peak memory conflicting benchmark figure"), f, n, limits)
	if err != nil || ch.ID != "ch_001" || ch.Status != ChallengeOpen || ch.Outcome != "" || ch.QuestionID != "q_002" || ch.AttemptSeq != 0 {
		t.Fatalf("%v %+v", err, ch)
	}
	cases := []struct {
		name string
		in   OpenInput
		want string
	}{
		{"unknown claim", OpenInput{
			TargetType: TargetClaim,
			TargetID:   "c_009",
		}, "unknown claim"},
		{"unknown question", OpenInput{
			TargetType: TargetQuestionAnswer,
			TargetID:   "q_009",
		}, "unknown question"},
		{"conclusion without text", OpenInput{
			TargetType: TargetConclusion,
		}, "no conclusion"},
		{"bad target type", OpenInput{
			TargetType: "belief",
		}, "target_type"},
		{"bad strategy", openIn("wishful", "x"), "strategy"},
		{"empty negation", func() OpenInput { i := openIn(StrategyCriticism, "model problem"); i.Negation = ""; return i }(), "must not be empty"},
		{"empty expected", func() OpenInput { i := openIn(StrategyCriticism, "model problem"); i.ExpectedIfFalse = " "; return i }(), "must not be empty"},
		{"same query as another challenge", openIn(StrategyCriticism, "Model  peak memory, conflicting benchmark figure!"), "repeats challenge"},
		{"strategy reused", openIn(StrategyDiscrepancy, "another conflicting memory figure"), "already used"},
		{"query is the claim again", openIn(StrategyCriticism, "The model needs 11 GB at fp16"), "not adversarial"},
	}
	for _, c := range cases {
		if _, _, err := Open(chs, c.in, f, n, limits); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	// "claim + not" is allowed.
	if _, _, err := Open(chs, openIn(StrategyNegation, "The model needs 11 GB at fp16 not"), f, n, limits); err != nil {
		t.Errorf("claim plus a new word must pass: %v", err)
	}
	// A query that repeats an attempt of the question is rejected.
	f2, _, _ := AddAttempt(f, "q_002", Attempt{
		Query: "model memory use fp16",
		Tool:  "web_search",
	})
	if _, _, err := Open(nil, openIn(StrategyCriticism, "Model memory use FP16"), f2, n, limits); !errors.Is(err, ErrDuplicateQuestion) {
		t.Errorf("attempt repeat: %v", err)
	}
	// The cap on one target.
	capped := limits
	capped.MaxChallengesPerTarget = 1
	if _, _, err := Open(chs, openIn(StrategyCriticism, "model reported problem"), f, n, capped); err == nil || !strings.Contains(err.Error(), "already has 1") {
		t.Errorf("cap: %v", err)
	}
}

func TestOpenOtherTargets(t *testing.T) {
	f, n := challengeFixture(t)
	n, _ = SetScratch(n, "Conclusion: the model fits into 16 GB of memory.")
	limits := DefaultLimits()
	_, ch, err := Open(nil, OpenInput{
		TargetType:      TargetQuestionAnswer,
		TargetID:        "q_002",
		Negation:        "the model does not fit",
		Strategy:        StrategyNegation,
		ExpectedIfFalse: "out of memory reports",
		Query:           "model does not fit in 16 GB",
	}, f, n, limits)
	if err != nil || ch.QuestionID != "q_002" || ch.TargetID != "q_002" {
		t.Fatalf("%v %+v", err, ch)
	}
	_, ch, err = Open(nil, OpenInput{
		TargetType:      TargetConclusion,
		TargetID:        "ignored",
		Negation:        "the model needs more than 16 GB",
		Strategy:        StrategyStaleness,
		ExpectedIfFalse: "newer versions are bigger",
		Query:           "model newer version memory requirements",
	}, f, n, limits)
	if err != nil || ch.QuestionID != "q_001" || ch.TargetID != "" {
		t.Fatalf("%v %+v", err, ch)
	}
}

func TestCloseRules(t *testing.T) {
	f, n := challengeFixture(t)
	chs, _, _ := Open(nil, openIn(StrategyCriticism, "model reported problem"), f, n, DefaultLimits())
	cases := []struct {
		name    string
		outcome Outcome
		ev      string
		srcs    []string
	}{
		{"survived without a search", OutcomeSurvived, "", nil},
		{"refuted without sources", OutcomeRefuted, "found", nil},
		{"refuted without evidence", OutcomeRefuted, " ", []string{"s_001"}},
		{"weakened without sources", OutcomeWeakened, "found", nil},
		{"bad outcome", "maybe", "", nil},
	}
	for _, c := range cases {
		if _, _, err := Close(chs, "ch_001", c.outcome, c.ev, c.srcs, ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if _, _, err := Close(chs, "ch_009", OutcomeInconclusive, "", nil, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown: %v", err)
	}
	closed, ch, err := Close(chs, "ch_001", OutcomeInconclusive, "paywalled", nil, "")
	if err != nil || ch.Status != ChallengeClosed || ch.Outcome != OutcomeInconclusive || chs[0].Status != ChallengeOpen {
		t.Fatalf("%v %+v", err, ch)
	}
	if _, _, err := Close(closed, "ch_001", OutcomeInconclusive, "", nil, ""); !errors.Is(err, ErrBadStatus) {
		t.Errorf("a challenge closes once: %v", err)
	}
	// Executed: it can survive; the attempt is in the question's log.
	f2, linked, err := LinkAttempt(f, chs, "ch_001", "web_search", "nothing against")
	if err != nil || linked[0].AttemptSeq != 1 {
		t.Fatalf("%v %+v", err, linked)
	}
	if q, _ := f2.Get("q_002"); len(q.Attempts) != 1 || q.Attempts[0].Query != "model reported problem" {
		t.Fatalf("%+v", q.Attempts)
	}
	if _, _, err := LinkAttempt(f2, linked, "ch_001", "web_search", ""); !errors.Is(err, ErrBadStatus) {
		t.Errorf("link twice: %v", err)
	}
	survived, _, err := Close(linked, "ch_001", OutcomeSurvived, "", nil, "")
	if err != nil || survived[0].Outcome != OutcomeSurvived {
		t.Fatalf("%v", err)
	}
	if _, _, err := Close(linked, "ch_001", OutcomeRefuted, "x", []string{"s_001", "s_001"}, "c_002"); err != nil {
		t.Fatal(err)
	}
}

func TestChallengeHelpers(t *testing.T) {
	chs := []Challenge{
		{
			ID:         "ch_001",
			TargetType: TargetQuestionAnswer,
			TargetID:   "q_002",
			Status:     ChallengeClosed,
			Outcome:    OutcomeSurvived,
		},
		{
			ID:         "ch_002",
			TargetType: TargetQuestionAnswer,
			TargetID:   "q_003",
			Status:     ChallengeOpen,
		},
		{
			ID:         "ch_003",
			TargetType: TargetConclusion,
			Status:     ChallengeClosed,
			Outcome:    OutcomeWeakened,
		},
	}
	if !RequiresAnswerChallenge(Question{
		Depth: 0,
	}) || !RequiresAnswerChallenge(Question{
		Depth: 1,
	}) || RequiresAnswerChallenge(Question{
		Depth: 2,
	}) {
		t.Error("depth 0 and 1 need an answer challenge")
	}
	if !AnswerChallengeClosed(chs, "q_002") || AnswerChallengeClosed(chs, "q_003") || AnswerChallengeClosed(chs, "q_009") {
		t.Error("AnswerChallengeClosed")
	}
	if got := OpenChallenges(chs); len(got) != 1 || got[0].ID != "ch_002" {
		t.Errorf("%+v", got)
	}
	if ConclusionSurvived(chs) {
		t.Error("weakened is not survived")
	}
	if s := ChallengeSummary(chs); !strings.Contains(s, "[ch_002]") || strings.Contains(s, "ch_001") {
		t.Errorf("%s", s)
	}
}
