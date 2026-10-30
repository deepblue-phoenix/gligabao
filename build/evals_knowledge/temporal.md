# Manifest: temporal

- **Name:** `temporal` (`build/evals_knowledge/temporal.json`)
- **Creation date:** 2026-10-30 (first commit `ad69365`; rewritten with everyday words on 2026-10-31, commit `20c8c82`)
- **Summary:** 20 synthetic cases on temporal reasoning and state changes. Abstract entities (`Entity A`, `Entity B`, ...) and everyday relations (partnership, lease, suspension, baton, access badge, sublicense, permit, mandate, council, guild, board) are written as plain prose. Facts start, end, expire, are renewed, replaced, corrected or only announced. Every question is asked "as of" an explicit date and has a short, checkable answer.

## Eval dataset generation skill

How this dataset was generated, so another generation (more cases, a variant, a regeneration) gives a comparable result.

### Goal

Test the temporal side of `remember` (`valid_at`, `invalid_at`, `expired_at`, supersession) and of `query` (the state as of a date) end to end, with a failing case that is easy to read and debug. Each case isolates one temporal behaviour; see the table in [docs/evals/knowledge.md](../../docs/evals/knowledge.md#the-temporal-eval) for the behaviour of each case.

### How it was generated

- **Generator:** an LLM coding agent, prompted by hand and reviewed by hand (no script). The cases are written directly into the JSON file. There is no seed or random step.
- **Process:**
  1. List the temporal behaviours to cover (one per case): supersession without an explicit end, expiry by date and by duration, event-driven end, derived end, announced (future) change, correction, restatement, backfilled history, late reports, relative dates, boundaries (touching end/start dates), several simultaneous changes, counting questions.
  2. For each behaviour, write 1-4 short `episode` texts with a `reference_time`, then one question as of a date, then the expected answer and (where it applies) the expected edges.
  3. Review: every date and answer is computed by hand from the texts; the question has exactly one right answer.
- **Rules every case follows:**
  - Entities are abstract labels (`Entity A`, `Entity B`, ...), never real names, so the model cannot use world knowledge.
  - Relations are everyday words. The rules of a relation (e.g. a partnership is exclusive and one per entity; a sublicense is valid only while its issuer holds a master license) are stated as a sentence in the first line of the text, not left implicit.
  - Dates are explicit (`10 January 2024`), except in the cases that test relative dates.
  - `reference_time` of each episode is the date of its report, in UTC (`...T00:00:00Z`).
  - Each question ends with the answer format ("Answer with the entity's label only."), and `expect_contains` holds the short expected answer(s) (`Entity C`, `SUSPENDED`, a count).
  - A question about a past date is asked only where the fact at that date ended in its own text, because superseded facts are not retrieved by `query`. Otherwise the question asks about the state after the change.
  - `expect_edges` lists the facts that must be stored (`source`, `target`, `valid_at`, and `invalid_at` / `ended` / `superseded` as appropriate). It is omitted where several facts link the same two entities, and the case then checks the answer only.
  - `reasoning` is `"none"` for every stage (`extract_graph`, `summarize_chunk`, `resolve_edges`, `resolve_series`, `detect_geo`, `extract_image`, `query`), so the model is not asked to think and the results are comparable between models.
- **Case naming:** `temporal_<behaviour>` in snake case (e.g. `temporal_partnership_swap`).

### Format

- File: `build/evals_knowledge/temporal.json`, a map `{"name": "temporal", "description": "...", "cases": [...]}`.
- Case fields used: `name`, `reasoning`, `remember` (list of `{kind: "episode", text: [lines], reference_time}`), `query`, `expect_contains`, `expect_edges`. See the format of the cases in [docs/evals/knowledge.md](../../docs/evals/knowledge.md).

### To add or regenerate cases

1. Pick a behaviour that no case covers yet (see the table in the docs) and give it a `temporal_<behaviour>` name.
2. Follow the rules above; copy the shape of an existing case (e.g. `temporal_backfilled_history`).
3. Add a row to the table in `docs/evals/knowledge.md` and bump the case count there, in the summary above and in `description` of the JSON if it changes.
4. Run the eval (`./run_evals.sh`) and check that the new case is correct by reading the failure, not by changing the expectation to fit the model.
5. Update this manifest if the rules change.

### Known limits

- The dataset was written in English, in a single style of prose; it does not test paraphrase, other languages or long texts.
- Only one question per case.
- Cases are tied to the current semantics of supersession (superseded facts are not retrieved by `query`).
