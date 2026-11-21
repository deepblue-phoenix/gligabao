// Package knowledge is the executor of the knowledge evals (core.TypeKnowledge): end-to-end checks of the knowledge extraction
// against the real LLM. A case remembers some texts in order and then asks a question about them, through the eval endpoints
// (/v1/gb/eval/remember and /v1/gb/eval/query, which return the ATIF trajectory of every call along with its result) of a private
// in-process mux (see Caller and MuxCaller): no listener is opened, and the routes are not mounted anywhere else. It checks what was stored in the dataset of the case (the temporal facts, the chunks, the web searches, the
// timeseries) and what the answer contains.
//
// Everything acts as an owner (Executor.Owner, a user id). Every run of a case has an eval dataset of its own (kind 'eval', hidden from
// the regular dataset functions), named after the eval run and the case (see DatasetName), so runs do not interfere and the graphs of
// all of them are kept in the database that the Executor is given, until the retention sweep of internal/evals/runmanager deletes them.
//
// Files: files.go (reading the evals of build/evals_knowledge), executor.go (Executor, which runs the cases and records them),
// case.go (running one case), commentary.go (the interactive LLM commentary on failed cases; the prompt itself is in ../commentary), checks.go (the checks of what was stored), reasoning.go (the -reasoning setting), caller.go (Caller, MuxCaller, the call of the eval endpoints; the store that tells where a trajectory went), rerun.go (Rerunner),
// verbose.go (the printing of the LLM calls).
package knowledge
