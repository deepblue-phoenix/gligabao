package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research/db"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/investigate_core"
)

// InvestigateStore is the implementation of investigate.Store on PostgreSQL: the tables of the frontier, the claims and sources, the challenges
// and the notes versions (see sql/schema.sql). Every method that changes state runs inside the transaction of InTx.
type InvestigateStore struct{ D *sql.DB }

var _ investigate_core.Store = InvestigateStore{}

// NewInvestigateStore returns the store of the investigative loop's state on d.
func NewInvestigateStore(d *sql.DB) investigate_core.Store { return InvestigateStore{D: d} }

// InTx runs fn in a transaction.
func (s InvestigateStore) InTx(ctx context.Context, fn func(investigate_core.Tx) error) error {
	tx, err := s.D.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("investigate: begin: %w", err)
	}
	if err := fn(investigateTx{q: tx}); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("investigate: commit: %w", err)
	}
	return nil
}

type investigateTx struct{ q db.Execer }

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (t investigateTx) UpsertQuestion(ctx context.Context, runID int64, q investigate_core.Question) error {
	sources, err := json.Marshal(append([]string{}, q.TargetSources...))
	if err != nil {
		return err
	}
	at := now()
	_, err = t.q.ExecContext(ctx, `
		INSERT INTO gb_agent_frontier_questions (run_id, id, parent_id, depth, kind, question, rationale, priority, status, validation_criteria,
			target_sources, answer, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (run_id, id) DO UPDATE SET kind = excluded.kind, question = excluded.question, rationale = excluded.rationale,
			priority = excluded.priority, status = excluded.status, validation_criteria = excluded.validation_criteria,
			target_sources = excluded.target_sources, answer = excluded.answer, updated_at = excluded.updated_at`,
		runID, q.ID, nullable(q.ParentID), q.Depth, string(q.Kind), q.Question, q.Rationale, q.Priority, string(q.Status), q.ValidationCriteria,
		string(sources), q.Answer, at, at)
	if err != nil {
		return fmt.Errorf("investigate: upsert question %s: %w", q.ID, err)
	}
	return nil
}

func (t investigateTx) AddDependency(ctx context.Context, runID int64, questionID, dependsOnID string) error {
	if _, err := t.q.ExecContext(ctx, `
		INSERT INTO gb_agent_frontier_deps (run_id, question_id, depends_on_id) VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING`, runID, questionID, dependsOnID); err != nil {
		return fmt.Errorf("investigate: add dependency %s -> %s: %w", questionID, dependsOnID, err)
	}
	return nil
}

func (t investigateTx) InsertAttempt(ctx context.Context, runID int64, questionID string, a investigate_core.Attempt) error {
	if _, err := t.q.ExecContext(ctx, `
		INSERT INTO gb_agent_frontier_attempts (run_id, question_id, seq, query, tool, outcome, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		runID, questionID, a.Seq, a.Query, a.Tool, a.Outcome, now()); err != nil {
		return fmt.Errorf("investigate: insert attempt %d of %s: %w", a.Seq, questionID, err)
	}
	return nil
}

func (t investigateTx) InsertTransition(ctx context.Context, runID int64, tr investigate_core.Transition) (int, error) {
	var seq int
	if err := t.q.QueryRowContext(ctx, `
		INSERT INTO gb_agent_frontier_transitions (run_id, question_id, seq, from_status, to_status, trigger, actor, reason, priority, attempts,
			caused_by, state_seq, created_at)
		VALUES ($1, $2, (SELECT COALESCE(MAX(seq), 0) + 1 FROM gb_agent_frontier_transitions WHERE run_id = $3 AND question_id = $4), $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		RETURNING seq`,
		runID, tr.QuestionID, runID, tr.QuestionID, string(tr.From), string(tr.To), string(tr.Trigger), string(tr.Actor), tr.Reason, tr.Priority,
		tr.Attempts, tr.CausedBy, tr.StateSeq, now()).Scan(&seq); err != nil {
		return 0, fmt.Errorf("investigate: insert transition of %s: %w", tr.QuestionID, err)
	}
	return seq, nil
}

func (t investigateTx) UpsertSource(ctx context.Context, runID int64, s investigate_core.Source) error {
	if _, err := t.q.ExecContext(ctx, `
		INSERT INTO gb_agent_sources (run_id, id, url, domain, title, note, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (run_id, id) DO UPDATE SET title = excluded.title, note = excluded.note`,
		runID, s.ID, s.URL, s.Domain, s.Title, s.Note, now()); err != nil {
		return fmt.Errorf("investigate: upsert source %s: %w", s.ID, err)
	}
	return nil
}

func (t investigateTx) UpsertClaim(ctx context.Context, runID int64, c investigate_core.Claim) error {
	at := now()
	if _, err := t.q.ExecContext(ctx, `
		INSERT INTO gb_agent_claims (run_id, id, question_id, kind, text, value, unit, status, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (run_id, id) DO UPDATE SET question_id = excluded.question_id, text = excluded.text, value = excluded.value, unit = excluded.unit,
			status = excluded.status, updated_at = excluded.updated_at`,
		runID, c.ID, nullable(c.QuestionID), string(c.Kind), c.Text, c.Value, c.Unit, string(c.Status), at, at); err != nil {
		return fmt.Errorf("investigate: upsert claim %s: %w", c.ID, err)
	}
	if _, err := t.q.ExecContext(ctx, `DELETE FROM gb_agent_claim_sources WHERE run_id = $1 AND claim_id = $2`, runID, c.ID); err != nil {
		return fmt.Errorf("investigate: upsert claim %s: %w", c.ID, err)
	}
	for _, l := range c.Links {
		if _, err := t.q.ExecContext(ctx, `INSERT INTO gb_agent_claim_sources (run_id, claim_id, source_id, stance) VALUES ($1, $2, $3, $4)`,
			runID, c.ID, l.SourceID, string(l.Stance)); err != nil {
			return fmt.Errorf("investigate: link claim %s to %s: %w", c.ID, l.SourceID, err)
		}
	}
	return nil
}

func (t investigateTx) UpsertChallenge(ctx context.Context, runID int64, c investigate_core.Challenge) error {
	var attemptID any
	if c.AttemptSeq > 0 {
		var id int64
		if err := t.q.QueryRowContext(ctx, `SELECT id FROM gb_agent_frontier_attempts WHERE run_id = $1 AND question_id = $2 AND seq = $3`,
			runID, c.QuestionID, c.AttemptSeq).Scan(&id); err != nil {
			return fmt.Errorf("investigate: attempt %d of %s for challenge %s: %w", c.AttemptSeq, c.QuestionID, c.ID, err)
		}
		attemptID = id
	}
	at := now()
	var closedAt any
	if c.Status == investigate_core.ChallengeClosed {
		closedAt = at
	}
	if _, err := t.q.ExecContext(ctx, `
		INSERT INTO gb_agent_challenges (run_id, id, question_id, target_type, target_id, negation, strategy, expected_if_false, query, attempt_id,
			status, outcome, evidence, result_claim_id, created_at, closed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (run_id, id) DO UPDATE SET attempt_id = excluded.attempt_id, status = excluded.status, outcome = excluded.outcome,
			evidence = excluded.evidence, result_claim_id = excluded.result_claim_id,
			closed_at = COALESCE(gb_agent_challenges.closed_at, excluded.closed_at)`,
		runID, c.ID, nullable(c.QuestionID), string(c.TargetType), c.TargetID, c.Negation, string(c.Strategy), c.ExpectedIfFalse, c.Query, attemptID,
		string(c.Status), string(c.Outcome), c.Evidence, nullable(c.ResultClaimID), at, closedAt); err != nil {
		return fmt.Errorf("investigate: upsert challenge %s: %w", c.ID, err)
	}
	for _, sid := range c.SourceIDs {
		if _, err := t.q.ExecContext(ctx, `INSERT INTO gb_agent_challenge_sources (run_id, challenge_id, source_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			runID, c.ID, sid); err != nil {
			return fmt.Errorf("investigate: challenge %s cites %s: %w", c.ID, sid, err)
		}
	}
	return nil
}

func (t investigateTx) AddNotesVersion(ctx context.Context, runID int64, notes, reason string, stateSeq int) (int, error) {
	var seq int
	if err := t.q.QueryRowContext(ctx, `
		INSERT INTO gb_agent_notes_versions (run_id, seq, state_seq, reason, text, created_at)
		VALUES ($1, (SELECT COALESCE(MAX(seq), 0) + 1 FROM gb_agent_notes_versions WHERE run_id = $2), $3, $4, $5, $6)
		RETURNING seq`, runID, runID, stateSeq, reason, notes, now()).Scan(&seq); err != nil {
		return 0, fmt.Errorf("investigate: add notes version of run %d: %w", runID, err)
	}
	return seq, nil
}

// LatestNotesVersion is the latest notes version of the run.
func (s InvestigateStore) LatestNotesVersion(ctx context.Context, runID int64) (investigate_core.NotesVersion, bool, error) {
	var v investigate_core.NotesVersion
	err := s.D.QueryRowContext(ctx, `SELECT seq, state_seq, reason, text FROM gb_agent_notes_versions WHERE run_id = $1 ORDER BY seq DESC LIMIT 1`,
		runID).Scan(&v.Seq, &v.StateSeq, &v.Reason, &v.Text)
	if errors.Is(err, sql.ErrNoRows) {
		return v, false, nil
	}
	if err != nil {
		return v, false, fmt.Errorf("investigate: latest notes version of run %d: %w", runID, err)
	}
	return v, true, nil
}

// StateSeq is the seq of the last research state of the run (0 if none).
func (s InvestigateStore) StateSeq(ctx context.Context, runID int64) (int, error) {
	var seq int
	if err := s.D.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM gb_agent_research_states WHERE run_id = $1`, runID).Scan(&seq); err != nil {
		return 0, fmt.Errorf("investigate: state seq of run %d: %w", runID, err)
	}
	return seq, nil
}

// Load reads the frontier, the challenges and the latest notes text of a run.
func (s InvestigateStore) Load(ctx context.Context, runID int64) (investigate_core.Loaded, error) {
	var out investigate_core.Loaded
	qs, err := s.loadQuestions(ctx, runID)
	if err != nil {
		return out, err
	}
	out.Frontier = investigate_core.NewFrontier(qs)
	if out.Challenges, err = s.loadChallenges(ctx, runID); err != nil {
		return out, err
	}
	v, ok, err := s.LatestNotesVersion(ctx, runID)
	if err != nil {
		return out, err
	}
	if ok {
		out.NotesText = v.Text
	}
	return out, nil
}

func (s InvestigateStore) loadQuestions(ctx context.Context, runID int64) ([]investigate_core.Question, error) {
	rows, err := s.D.QueryContext(ctx, `
		SELECT id, parent_id, depth, kind, question, rationale, priority, status, validation_criteria, target_sources, answer
		FROM gb_agent_frontier_questions WHERE run_id = $1`, runID)
	if err != nil {
		return nil, fmt.Errorf("investigate: load questions of run %d: %w", runID, err)
	}
	defer rows.Close()
	byID := map[string]*investigate_core.Question{}
	var order []string
	for rows.Next() {
		var q investigate_core.Question
		var parent sql.NullString
		var kind, status, sources string
		if err := rows.Scan(&q.ID, &parent, &q.Depth, &kind, &q.Question, &q.Rationale, &q.Priority, &status, &q.ValidationCriteria, &sources, &q.Answer); err != nil {
			return nil, fmt.Errorf("investigate: load questions of run %d: %w", runID, err)
		}
		q.ParentID, q.Kind, q.Status = parent.String, investigate_core.Kind(kind), investigate_core.Status(status)
		if err := json.Unmarshal([]byte(sources), &q.TargetSources); err != nil {
			return nil, fmt.Errorf("investigate: target_sources of %s: %w", q.ID, err)
		}
		if len(q.TargetSources) == 0 {
			q.TargetSources = nil
		}
		byID[q.ID] = &q
		order = append(order, q.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.pairs(ctx, `SELECT question_id, depends_on_id FROM gb_agent_frontier_deps WHERE run_id = $1 ORDER BY question_id, depends_on_id`, runID,
		func(q, dep string) { byID[q].DependsOn = append(byID[q].DependsOn, dep) }); err != nil {
		return nil, err
	}
	if err := s.pairs(ctx, `SELECT question_id, MAX(seq)::text FROM gb_agent_frontier_transitions WHERE run_id = $1 GROUP BY question_id`, runID,
		func(q, seq string) { byID[q].TransSeq, _ = strconv.Atoi(seq) }); err != nil {
		return nil, err
	}
	arows, err := s.D.QueryContext(ctx, `SELECT question_id, seq, query, tool, outcome FROM gb_agent_frontier_attempts WHERE run_id = $1 ORDER BY question_id, seq`, runID)
	if err != nil {
		return nil, fmt.Errorf("investigate: load attempts of run %d: %w", runID, err)
	}
	defer arows.Close()
	for arows.Next() {
		var qid string
		var a investigate_core.Attempt
		if err := arows.Scan(&qid, &a.Seq, &a.Query, &a.Tool, &a.Outcome); err != nil {
			return nil, err
		}
		byID[qid].Attempts = append(byID[qid].Attempts, a)
	}
	if err := arows.Err(); err != nil {
		return nil, err
	}
	out := make([]investigate_core.Question, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// pairs runs a query of two text columns and calls fn on each row.
func (s InvestigateStore) pairs(ctx context.Context, query string, runID int64, fn func(a, b string)) error {
	rows, err := s.D.QueryContext(ctx, query, runID)
	if err != nil {
		return fmt.Errorf("investigate: load run %d: %w", runID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return err
		}
		fn(a, b)
	}
	return rows.Err()
}

func (s InvestigateStore) loadChallenges(ctx context.Context, runID int64) ([]investigate_core.Challenge, error) {
	rows, err := s.D.QueryContext(ctx, `
		SELECT c.id, c.question_id, c.target_type, c.target_id, c.negation, c.strategy, c.expected_if_false, c.query, a.seq,
			c.status, c.outcome, c.evidence, c.result_claim_id
		FROM gb_agent_challenges c LEFT JOIN gb_agent_frontier_attempts a ON a.id = c.attempt_id
		WHERE c.run_id = $1`, runID)
	if err != nil {
		return nil, fmt.Errorf("investigate: load challenges of run %d: %w", runID, err)
	}
	defer rows.Close()
	var out []investigate_core.Challenge
	for rows.Next() {
		var c investigate_core.Challenge
		var qid, result sql.NullString
		var seq sql.NullInt64
		var tt, strategy, status, outcome string
		if err := rows.Scan(&c.ID, &qid, &tt, &c.TargetID, &c.Negation, &strategy, &c.ExpectedIfFalse, &c.Query, &seq, &status, &outcome, &c.Evidence, &result); err != nil {
			return nil, err
		}
		c.QuestionID, c.ResultClaimID, c.AttemptSeq = qid.String, result.String, int(seq.Int64)
		c.TargetType, c.Strategy, c.Status, c.Outcome = investigate_core.TargetType(tt), investigate_core.Strategy(strategy), investigate_core.ChallengeStatus(status), investigate_core.Outcome(outcome)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	srows, err := s.D.QueryContext(ctx, `SELECT challenge_id, source_id FROM gb_agent_challenge_sources WHERE run_id = $1 ORDER BY challenge_id, source_id`, runID)
	if err != nil {
		return nil, fmt.Errorf("investigate: load challenge sources of run %d: %w", runID, err)
	}
	defer srows.Close()
	for srows.Next() {
		var cid, sid string
		if err := srows.Scan(&cid, &sid); err != nil {
			return nil, err
		}
		for i := range out {
			if out[i].ID == cid {
				out[i].SourceIDs = append(out[i].SourceIDs, sid)
			}
		}
	}
	if err := srows.Err(); err != nil {
		return nil, err
	}
	return investigate_core.SortChallenges(out), nil
}
