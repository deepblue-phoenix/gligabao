package sqlite

import (
	"database/sql"
	"fmt"
)

//---------------------------------------------------------------------------------------------

// The eval tables reference gb_datasets and gb_trained_models, which are created by the relational backend
// (internal/db/relational/sqlite), so that schema must be applied first.
// All DATETIME columns hold RFC 3339 UTC strings (SQLite has no real datetime type; see relational/sqlite/db_time.go).
const schema = `
-- ---------------------------------------------------------------------------------------------------------------------
-- EVALS. One set of tables for both kinds of eval; what is specific to a kind is in that kind's metadata table.
--
--   type  'knowledge'  knowledge-extraction evals (internal/evals/knowledge, internal/evals/experiments): remember, then recall, checked by
--                      programmatic checks. Run by the runner, into the eval database.
--         'training'   evals of trained models (internal/evals/training, internal/api/training/evals.go): the model is asked the cases
--                      of an eval (bin/evals_training/*.json) and an external LLM judges the answers. Run by the server (the
--                      admin UI's Eval button, the API) or by the command-line runner internal/evals/training/cmd.
--   UNIVERSAL tables (every eval has these; each has a type column):
--     gb_experiments_defs            a named series of runs
--     gb_experiment_cases_defs       the cases (eval + case_name) an experiment has executed
--     gb_experiment_runs        one execution: one or more cases
--     gb_experiment_case_results     the result of one case in one run (both kinds: one row per case)
--     gb_experiment_case_runs_trajectories  ATIF trajectories of a case (the trajectory files; only knowledge cases have them for now)
--   METADATA tables (the columns only one kind has):
--     gb_knowledge_case_meta     one row per knowledge case result: answer, checks, dataset the case built its graph in
--     gb_training_case_meta      one row per training case result: the question, the dataset's answer, the model's, the judge's score
--     gb_training_eval_run_meta  one row per training run: trained model, eval, judge, counters and score of the eval run
--
-- type is the same on a row and on its parent: the composite foreign keys (<parent id>, type) -> parent (id, type) make the
-- database refuse a run of one type under an experiment of the other, a result under a run of the other type, and a metadata
-- row on a result of the wrong type.
--
-- TERMS. A CASE is one input and what is expected or checked (an input/output combination, plus metadata) to be evaluated. An
-- EVAL is a set of cases. A RUN executes some cases once; a RESULT is the outcome of one case in one run, so a case has many
-- results and a run has many results; an EXPERIMENT is a named series of runs. gb_experiment_cases_defs lists the cases (the
-- definitions are in the eval files, not in the database); gb_experiment_case_results has the outcomes, one row per case of a
-- run for both kinds of eval.
--
-- EVALS AND CASES. A case is identified by its eval and its name (case_name), for both kinds. Knowledge evals: an eval is a file in
-- bin/evals_knowledge/ (its name without ".json") and the case is one of its cases. Training evals: an eval is a file in
-- bin/evals_training/ (its name without ".json"; named after the training dataset its cases ask about), whose cases are
-- {name, instruction, input, output}, the output being the answer expected. A training eval run is one run of the eval against
-- one trained model: a run, with one case result per case (case_name = the case's name) and the run's own columns in
-- gb_training_eval_run_meta. The cases are meant to be ones the model never saw in training, so the score measures generalization.
-- (Asking a model the questions of its OWN training dataset is not an eval but a training data audit, see the end of this file.)
-- ---------------------------------------------------------------------------------------------------------------------

-- An experiment is a named series of runs.
--   type    see above; the name is unique within a type
--   config  JSON object with the free-form settings under test
CREATE TABLE IF NOT EXISTS gb_experiments_defs (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	type        TEXT NOT NULL CHECK (type IN ('knowledge', 'training')),
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	config      TEXT NOT NULL DEFAULT '{}',
	created_at  DATETIME NOT NULL,
	UNIQUE (type, name),
	UNIQUE (id, type)
);

-- The cases (a case name within an eval) that an experiment has executed.
-- A row is added the first time one of the experiment's runs executes the case.
CREATE TABLE IF NOT EXISTS gb_experiment_cases_defs (
	experiment_id INTEGER NOT NULL,
	type          TEXT NOT NULL,
	eval         TEXT NOT NULL DEFAULT '',
	case_name     TEXT NOT NULL,
	created_at    DATETIME NOT NULL,
	PRIMARY KEY (experiment_id, eval, case_name),
	FOREIGN KEY (experiment_id, type) REFERENCES gb_experiments_defs(id, type) ON DELETE CASCADE
);

-- One execution of an experiment: a pass over one or more cases (training: one eval run, over the cases of its eval).
--   experiment_id  NULL for a run that is in no experiment (a training eval run started without an experiment name);
--                  knowledge runs always have one
--   status         'running', then when finished, by type: knowledge 'passed' (every case passed) or 'failed' (at least one case
--                  did not pass: a check did not hold, or it errored); training 'completed' (the eval run ran to the end; it has
--                  a score, not a verdict), 'error' (the run itself errored) or 'cancelled'. 'failed' is a verdict about checks
--                  and only knowledge has it; 'error' is "could not be run to the end" and means that in every type. The CHECK
--                  enforces the set of each type.
--   params         JSON object describing how the run was invoked (knowledge: evals dir, selected cases, models, reasoning;
--                  training: batch_size, max_tokens, temperature, limit, ...)
--   total_cases    the cases of the run (training: the examples of the eval), set when the run is finished, like the counts below
--   passed_cases, failed_cases, error_cases, judged_cases: how many cases ended with that status (see the case statuses in
--                  gb_experiment_case_results): each counts one status and nothing else. passed_cases and failed_cases are
--                  the knowledge verdicts (0 for training), judged_cases the training cases that were scored (whatever the
--                  score: not a verdict; 0 for knowledge), error_cases those that could not be run, in both. What is left of
--                  total_cases (total - the four) was not reached, e.g. the examples of a cancelled training run.
--   cost_usd       the LLM cost (the judge's for training), set when the run is finished
CREATE TABLE IF NOT EXISTS gb_experiment_runs (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	type          TEXT NOT NULL CHECK (type IN ('knowledge', 'training')),
	experiment_id INTEGER,
	status        TEXT NOT NULL,
	params        TEXT NOT NULL DEFAULT '{}',
	total_cases   INTEGER NOT NULL DEFAULT 0,
	passed_cases  INTEGER NOT NULL DEFAULT 0,
	failed_cases  INTEGER NOT NULL DEFAULT 0,
	error_cases   INTEGER NOT NULL DEFAULT 0,
	judged_cases  INTEGER NOT NULL DEFAULT 0,
	cost_usd      REAL NOT NULL DEFAULT 0,
	started_at    DATETIME NOT NULL,
	finished_at   DATETIME,
	UNIQUE (id, type),
	CHECK (type <> 'knowledge' OR experiment_id IS NOT NULL),
	CHECK (status = 'running'
		OR (type = 'knowledge' AND status IN ('passed', 'failed'))
		OR (type = 'training' AND status IN ('completed', 'error', 'cancelled'))),
	FOREIGN KEY (experiment_id, type) REFERENCES gb_experiments_defs(id, type) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_gb_experiment_runs_experiment ON gb_experiment_runs(experiment_id);

-- The result of one case within one run (the universal part; the rest is in the metadata table of its type): one row per case,
-- for knowledge and for training. A training case is one case of the eval: its case_name is the case's name in the eval (as for a
-- knowledge case) and its row is written when its judge batch is done, so it is never 'running'.
--   status       the set depends on the type (the CHECK enforces the sets); 'failed' is only a verdict on checks, 'error' only
--                means the case could not be run to the end:
--                knowledge: 'passed' or 'failed' (a check did not hold: a missing edge, the answer lacks an expected string), or
--                'error' (the case could not be run to the end);
--                training: 'judged' (the model answered and the judge scored it; the score is in the metadata, not a verdict,
--                so a score of 0 is still 'judged') or 'error' (the model or the judge errored: no score).
--                'running' and 'cancelled' are not used by cases.
--   error        why it failed or could not be run
--   cost_usd     the LLM cost of the case (knowledge: its remember and recall calls; training: 0, the judge's cost is per batch and
--                is in gb_training_eval_run_meta.judge_cost_usd and the run's cost_usd)
--   finished_at  NULL while the case runs
CREATE TABLE IF NOT EXISTS gb_experiment_case_results (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	type        TEXT NOT NULL CHECK (type IN ('knowledge', 'training')),
	run_id      INTEGER NOT NULL,
	eval       TEXT NOT NULL DEFAULT '',
	case_name   TEXT NOT NULL,
	status      TEXT NOT NULL,
	error       TEXT NOT NULL DEFAULT '',
	cost_usd    REAL NOT NULL DEFAULT 0,
	started_at  DATETIME NOT NULL,
	finished_at DATETIME,
	UNIQUE (id, type),
	CHECK ((type = 'knowledge' AND status IN ('passed', 'failed', 'error')) OR (type = 'training' AND status IN ('judged', 'error'))),
	FOREIGN KEY (run_id, type) REFERENCES gb_experiment_runs(id, type) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_gb_experiment_case_results_run ON gb_experiment_case_results(run_id);
-- a training run has one result per example
CREATE UNIQUE INDEX IF NOT EXISTS idx_gb_experiment_case_results_training_case ON gb_experiment_case_results(run_id, eval, case_name) WHERE type = 'training';

-- The ATIF trajectories of a case (internal/ai/atif), in call order. Only the reference is kept here:
-- trajectory_id is the ATIF trajectory_id, and path is the JSON file the trajectory was stored in ('' if none).
--   kind: 'remember' or 'recall'
--   artifact_ids: JSON array of the IDs of the artifacts (in the dataset of the case's gb_knowledge_case_meta) the call is
--   about (remember: its input; recall: the evidence's sources)
CREATE TABLE IF NOT EXISTS gb_experiment_case_runs_trajectories (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	case_result_id   INTEGER NOT NULL REFERENCES gb_experiment_case_results(id) ON DELETE CASCADE,
	seq           INTEGER NOT NULL,
	kind          TEXT NOT NULL CHECK (kind IN ('remember', 'recall')),
	trajectory_id TEXT NOT NULL,
	agent_name    TEXT NOT NULL DEFAULT '',
	steps         INTEGER NOT NULL DEFAULT 0,
	cost_usd      REAL NOT NULL DEFAULT 0,
	path          TEXT NOT NULL DEFAULT '',
	artifact_ids  TEXT NOT NULL DEFAULT '[]',
	created_at    DATETIME NOT NULL,
	UNIQUE (case_result_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_gb_experiment_case_runs_trajectories_tid ON gb_experiment_case_runs_trajectories(trajectory_id);

-- KNOWLEDGE metadata of a case (type 'knowledge'): what only the knowledge-extraction cases have.
--   answer         the recall's answer
--   problems       JSON array of strings, the failed checks; passed_checks: JSON array of strings, the checks that held
--   dataset_id     the dataset the case ran in (NULL if it was never created); it holds the graph the case built
--   steps          total ATIF steps over the case's trajectories
CREATE TABLE IF NOT EXISTS gb_knowledge_case_meta (
	case_result_id   INTEGER PRIMARY KEY,
	type          TEXT NOT NULL DEFAULT 'knowledge' CHECK (type = 'knowledge'),
	answer        TEXT NOT NULL DEFAULT '',
	problems      TEXT NOT NULL DEFAULT '[]',
	passed_checks TEXT NOT NULL DEFAULT '[]',
	dataset_id    INTEGER REFERENCES gb_datasets(id),
	steps         INTEGER NOT NULL DEFAULT 0,
	FOREIGN KEY (case_result_id, type) REFERENCES gb_experiment_case_results(id, type) ON DELETE CASCADE
);

-- TRAINING metadata of a run (type 'training'; internal/evals/training, internal/api/training/evals.go; the API calls the run an eval):
-- what only the eval runs of trained models have. The trained model is asked the cases of an eval (bin/evals_training/*.json) and
-- an external LLM scores its answers against the expected ones, a batch of questions per LLM call. The counters are rewritten after every batch, so
-- the row shows the progress while the eval run runs. The status, the times and the settings (batch_size,
-- max_tokens, temperature, limit; the params) are those of the universal gb_experiment_runs row. A run still 'running' after a restart of the
-- server is set to 'error' when the server starts.
--   trained_model_id  the model (gb_trained_models) that was asked; deleting the model deletes this row (and the run with its cases)
--   eval             the eval whose cases were asked (the file name in bin/evals_training without ".json"); a run asks one eval, so
--                    its case results' eval column (which the universal tables key on) is this value
--   judge_model      the model that judged ('' until its first reply)
--   num_examples     the questions asked; num_answered the ones the model answered; num_judged the ones scored; num_errored the ones
--                    where the model or the judge errored; num_correct/num_partial/num_incorrect split num_judged by score
--                    (>= 4 / 2-3 / 0-1); score_sum the sum of the scores (0 to 5 each) of the judged ones
--   num_batches      judge batches done; judge_calls the LLM calls made (a batch may need a retry); judge_*_tokens its tokens
--   judge_cost_usd   the judge's LLM cost so far (the run's cost_usd gets it when the run is finished)
--   error            why the eval run errored ('' otherwise)
CREATE TABLE IF NOT EXISTS gb_training_eval_run_meta (
	run_id              INTEGER PRIMARY KEY,
	type                TEXT NOT NULL DEFAULT 'training' CHECK (type = 'training'),
	trained_model_id    INTEGER NOT NULL REFERENCES gb_trained_models(id) ON DELETE CASCADE,
	eval                TEXT NOT NULL,
	judge_model         TEXT NOT NULL DEFAULT '',
	num_examples        INTEGER NOT NULL DEFAULT 0,
	num_answered        INTEGER NOT NULL DEFAULT 0,
	num_judged          INTEGER NOT NULL DEFAULT 0,
	num_errored         INTEGER NOT NULL DEFAULT 0,
	num_correct         INTEGER NOT NULL DEFAULT 0,
	num_partial         INTEGER NOT NULL DEFAULT 0,
	num_incorrect       INTEGER NOT NULL DEFAULT 0,
	score_sum           INTEGER NOT NULL DEFAULT 0,
	num_batches         INTEGER NOT NULL DEFAULT 0,
	judge_calls         INTEGER NOT NULL DEFAULT 0,
	judge_input_tokens  INTEGER NOT NULL DEFAULT 0,
	judge_output_tokens INTEGER NOT NULL DEFAULT 0,
	judge_cost_usd      REAL NOT NULL DEFAULT 0,
	error               TEXT NOT NULL DEFAULT '',
	FOREIGN KEY (run_id, type) REFERENCES gb_experiment_runs(id, type) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_gb_training_eval_run_meta_model ON gb_training_eval_run_meta(trained_model_id, run_id);
-- The foreign keys cascade downwards only, so deleting a trained model deletes the metadata of its eval runs but would leave their
-- universal rows behind: this trigger deletes the run of each (its case results and their metadata go with it).
CREATE TRIGGER IF NOT EXISTS trg_gb_training_eval_run_meta_deleted AFTER DELETE ON gb_training_eval_run_meta
BEGIN
	DELETE FROM gb_experiment_runs WHERE type = 'training' AND id = OLD.run_id;
END;

-- TRAINING metadata of a case result (type 'training'): the outcome for one case (question) of an eval run, written with its
-- universal row when its judge batch is done. The universal row's status is 'judged' or 'error', its error says why
-- the model or the judge errored.
--   idx          the case's position in the eval (0-based; the case's name is the universal row's case_name); batch the judge batch (1-based)
--   instruction, input  the question; reference the expected answer; response the model's ('' if it failed)
--   score        the judge's, 0 to 5 (NULL unless judged); reason its explanation
--   input_tokens, output_tokens, duration_ms  of the model's answer
CREATE TABLE IF NOT EXISTS gb_training_case_meta (
	case_result_id INTEGER PRIMARY KEY,
	type          TEXT NOT NULL DEFAULT 'training' CHECK (type = 'training'),
	idx           INTEGER NOT NULL,
	batch         INTEGER NOT NULL,
	instruction   TEXT NOT NULL,
	input         TEXT NOT NULL DEFAULT '',
	reference     TEXT NOT NULL DEFAULT '',
	response      TEXT NOT NULL DEFAULT '',
	score         INTEGER,
	reason        TEXT NOT NULL DEFAULT '',
	input_tokens  INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	duration_ms   INTEGER NOT NULL DEFAULT 0,
	FOREIGN KEY (case_result_id, type) REFERENCES gb_experiment_case_results(id, type) ON DELETE CASCADE
);

-- ---------------------------------------------------------------------------------------------------------------------
-- TRAINING DATA AUDITS of trained models (internal/api/training/audits.go, internal/evals/training). An audit asks a trained model the
-- questions of its OWN fine-tuning dataset and has an LLM judge, a batch of questions per call, how closely its answers reproduce
-- the dataset's. Every case is something the model was trained on, so it checks that the training took (and, through the examples
-- it fails, finds bad training data); it measures memorization and fit, NOT generalization. That is what an EVAL of a trained model
-- is for (the 'training' rows of the universal tables above: its cases are meant to be ones the model never saw).
--
-- The two are kept apart on purpose, and an audit has tables of its own instead of rows of the universal eval tables: it has no
-- experiments, no knowledge-style case definitions and no type column to share. One run per audit, one case per example.
-- ---------------------------------------------------------------------------------------------------------------------

-- ONE ROW PER AUDIT of a trained model. The counters are rewritten after every judge batch, so the row shows the progress while the
-- audit runs. A run still 'running' after a restart of the server is set to 'error' when the server starts.
--   trained_model_id  the model (gb_trained_models) that was asked; deleting the model deletes the audit (and its cases)
--   status            'running', 'completed' (it ran to the end: it has a score, not a verdict), 'error' (error says why) or 'cancelled'
--   dataset, dataset_sha256  the training dataset the cases came from, and the digest of the file that was read
--   params            JSON object: batch_size, max_tokens, temperature, limit
--   judge_model       the model that judged ('' until its first reply)
--   num_examples      the questions asked; num_answered the ones the model answered; num_judged the ones scored; num_errored the ones
--                     where the model or the judge errored; num_correct/num_partial/num_incorrect split num_judged by score
--                     (>= 4 / 2-3 / 0-1); score_sum the sum of the scores (0 to 5 each) of the judged ones
--   num_batches       judge batches done; judge_calls the LLM calls made (a batch may need a retry); judge_*_tokens its tokens
--   judge_cost_usd    the judge's LLM cost so far
--   error             why the audit errored ('' otherwise)
CREATE TABLE IF NOT EXISTS gb_training_audit_runs (
	id                  INTEGER PRIMARY KEY AUTOINCREMENT,
	trained_model_id    INTEGER NOT NULL REFERENCES gb_trained_models(id) ON DELETE CASCADE,
	status              TEXT NOT NULL CHECK (status IN ('running', 'completed', 'error', 'cancelled')),
	dataset             TEXT NOT NULL DEFAULT '',
	dataset_sha256      TEXT NOT NULL DEFAULT '',
	params              TEXT NOT NULL DEFAULT '{}',
	judge_model         TEXT NOT NULL DEFAULT '',
	num_examples        INTEGER NOT NULL DEFAULT 0,
	num_answered        INTEGER NOT NULL DEFAULT 0,
	num_judged          INTEGER NOT NULL DEFAULT 0,
	num_errored         INTEGER NOT NULL DEFAULT 0,
	num_correct         INTEGER NOT NULL DEFAULT 0,
	num_partial         INTEGER NOT NULL DEFAULT 0,
	num_incorrect       INTEGER NOT NULL DEFAULT 0,
	score_sum           INTEGER NOT NULL DEFAULT 0,
	num_batches         INTEGER NOT NULL DEFAULT 0,
	judge_calls         INTEGER NOT NULL DEFAULT 0,
	judge_input_tokens  INTEGER NOT NULL DEFAULT 0,
	judge_output_tokens INTEGER NOT NULL DEFAULT 0,
	judge_cost_usd      REAL NOT NULL DEFAULT 0,
	error               TEXT NOT NULL DEFAULT '',
	started_at          DATETIME NOT NULL,
	finished_at         DATETIME
);
CREATE INDEX IF NOT EXISTS idx_gb_training_audit_runs_model ON gb_training_audit_runs(trained_model_id, id);

-- ONE ROW PER EXAMPLE of an audit, written when its judge batch is done (so it is never 'running').
--   idx          the example's position in the dataset (0-based; unique within a run); batch the judge batch (1-based)
--   instruction, input  the question; reference the dataset's answer; response the model's ('' if it failed)
--   status       'judged' (the model answered and the judge scored it) or 'error' (the model or the judge errored; error says why)
--   score        the judge's, 0 to 5 (NULL unless judged); reason its explanation
--   input_tokens, output_tokens, duration_ms  of the model's answer
CREATE TABLE IF NOT EXISTS gb_training_audit_cases (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id        INTEGER NOT NULL REFERENCES gb_training_audit_runs(id) ON DELETE CASCADE,
	idx           INTEGER NOT NULL,
	batch         INTEGER NOT NULL,
	instruction   TEXT NOT NULL,
	input         TEXT NOT NULL DEFAULT '',
	reference     TEXT NOT NULL DEFAULT '',
	response      TEXT NOT NULL DEFAULT '',
	status        TEXT NOT NULL CHECK (status IN ('judged', 'error')),
	error         TEXT NOT NULL DEFAULT '',
	score         INTEGER,
	reason        TEXT NOT NULL DEFAULT '',
	input_tokens  INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	duration_ms   INTEGER NOT NULL DEFAULT 0,
	UNIQUE (run_id, idx)
);`

// initSchema applies the eval schema.
func initSchema(d *sql.DB) error {
	if _, err := d.Exec(schema); err != nil {
		return fmt.Errorf("init evals schema: %w", err)
	}
	return nil
}
