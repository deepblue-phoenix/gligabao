package model

import (
	"net/http"
)

// Register puts the GCNII* routes that are not training on mux, under prefix + "/gcnii" (the server passes "/v1/gb/training", see the list in
// the package doc). mux is the one of the training routes, which sits behind the authentication, so the handlers can take the user from the
// request.
//
// svc is sargo. It is nil when sargo is off (nothing was started), and then NOTHING is mounted: these routes are the part of the training
// endpoints that needs sargo. This is the pattern for every part of the training endpoints that depends on an optional backend: the
// endpoints of the training as a whole are mounted whenever training mode is on, and each part that needs something that may not be there
// (today only sargo) is mounted by a function like this one, which checks for it and mounts nothing without it. A route is never mounted
// to answer 503 for a backend that was not started.
//
// sync (may be nil) restores the model of the user before it is used.
func Register(mux *http.ServeMux,
	prefix string,
	svc Service,
	sync Syncer) {
	if svc == nil {
		return
	}
	base := prefix + "/gcnii"
	mux.Handle("POST "+base+"/infer", Infer(svc, sync))
	mux.Handle("POST "+base+"/embed", Embed(svc, sync))
}
