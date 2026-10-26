// Package knowledge is the executor of the knowledge evals (core.TypeKnowledge): end-to-end checks of the knowledge extraction
// against the real LLM. A case remembers some texts in order and then asks a question about them, behind an in-process HTTP server
// in eval mode (the endpoints /v1/gb/eval/remember and /v1/gb/eval/query, which return the ATIF trajectory of every call along
// with its result). It checks what was stored in the dataset of the case (the temporal facts, the chunks, the web searches, the
// timeseries) and what the answer contains.
//
// Every run of a case has a dataset of its own, named after the case and the eval run (see DatasetName), so runs do not interfere and
// the graphs of all of them are kept in the database that the Executor is given (the eval database, never reset).
//
// Files: files.go (reading the evals of build/evals_knowledge), executor.go (Executor, which runs the cases and records them),
// case.go (running one case), checks.go (the checks of what was stored), reasoning.go (the -reasoning setting), post.go,
// verbose.go (the printing of the LLM calls).
package knowledge
