// Package synth generates synthetic evals: knowledge evals whose cases an LLM writes from a definition the user gives (what the eval
// dataset is about, what the data corpus the cases run on looks like, the model, how many cases, one case per call or in bulk). The
// definition is versioned and can be changed and generated again; the cases can be edited one by one.
//
// A synthetic eval is an ordinary user eval (package usereval: its JSON in the user's files, its def and cases in the database), so it
// runs like any other. What is specific to it is in the gb_synth_* tables (core.SynthDef, core.SynthGeneration, core.SynthCase).
//
// The package has the pure part (the prompts and the parsing of the answers: prompt.go), the generator that calls the LLM
// (generate.go) and the Manager that ties it to the database and the files (manager.go).
package synth
