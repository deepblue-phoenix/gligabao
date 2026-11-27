package evals

import "net/http"

// Register mounts the user routes of the evals on mux. a wraps a handler so that the request acts as one user (the authentication of
// the other user routes of the API). Nothing is mounted without a Service.
//
// The routes with a name and one more segment (/evals/{name}/runs, /evals/{name}/history) are two patterns (GET and POST) that pick by
// the last segment (ByName), because Go's mux rejects /evals/{name}/runs next to /evals/runs/{id}; the reserved names (runs,
// case_runs, trajectories) keep every other route apart.
func Register(mux *http.ServeMux,
	a func(http.HandlerFunc) http.Handler,
	s *Service) {
	if s == nil {
		return
	}
	const base = "/v1/gb/evals"
	byName := s.DefByName(UserOwner)
	mux.Handle("GET "+base, a(s.List(UserOwner)))
	mux.Handle("PUT "+base+"/{name}", a(s.Put(UserOwner)))
	mux.Handle("GET "+base+"/{name}", a(s.Get(UserOwner)))
	// The synthetic evals. The routes put the verb before the name ("generate/{name}", "cases/{name}/{case}"), because Go's mux rejects
	// /evals/synth/{name}/generate next to /evals/{name}/files/{path...}.
	mux.Handle("GET "+base+"/synth", a(s.SynthList(UserOwner)))
	mux.Handle("POST "+base+"/synth", a(s.SynthCreate(UserOwner)))
	mux.Handle("GET "+base+"/synth/{name}", a(s.SynthGet(UserOwner)))
	mux.Handle("PUT "+base+"/synth/{name}", a(s.SynthUpdate(UserOwner)))
	mux.Handle("POST "+base+"/synth/generate/{name}", a(s.SynthGenerate(UserOwner)))
	mux.Handle("PUT "+base+"/synth/cases/{name}/{case}", a(s.SynthEditCase(UserOwner)))
	mux.Handle("DELETE "+base+"/{name}", a(s.Delete(UserOwner)))
	mux.Handle("PUT "+base+"/{name}/files/{path...}", a(s.PutFile(UserOwner)))
	mux.Handle("DELETE "+base+"/{name}/files/{path...}", a(s.DeleteFile(UserOwner)))
	mux.Handle("POST "+base+"/{name}/{sub}", a(ByName(map[string]http.HandlerFunc{
		"runs": s.StartRun(UserOwner),
	})))
	mux.Handle("GET "+base+"/{name}/{sub}", a(ByName(map[string]http.HandlerFunc{
		"runs":    s.ListRuns(byName),
		"history": s.History(byName),
	})))
	mux.Handle("GET "+base+"/runs/{id}", a(s.GetRun(UserOwner)))
	mux.Handle("POST "+base+"/runs/{id}/cancel", a(s.CancelRun(UserOwner)))
	mux.Handle("DELETE "+base+"/runs/{id}", a(s.DeleteRun(UserOwner)))
	mux.Handle("GET "+base+"/case_runs/{id}", a(s.GetCase(UserOwner)))
	mux.Handle("GET "+base+"/case_runs/{id}/graph", a(s.Graph(UserOwner, nil)))
	mux.Handle("GET "+base+"/case_runs/{id}/temporal", a(s.Temporal(UserOwner)))
	mux.Handle("GET "+base+"/case_runs/{id}/debug", a(s.Debug(UserOwner)))
	mux.Handle("POST "+base+"/case_runs/{id}/commentary", a(s.Commentary(UserOwner)))
	mux.Handle("POST "+base+"/case_runs/{id}/rerun", a(s.Rerun(UserOwner)))
	mux.Handle("GET "+base+"/bookmarks", a(s.ListBookmarks(UserOwner)))
	mux.Handle("POST "+base+"/bookmarks", a(s.AddBookmark(UserOwner)))
	mux.Handle("DELETE "+base+"/bookmarks/{id}", a(s.DeleteBookmark(UserOwner)))
	mux.Handle("GET "+base+"/trajectories/{id}", a(s.Trajectory(UserOwner)))
}
