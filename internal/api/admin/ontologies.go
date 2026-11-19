package admin

import (
	"net/http"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	log "github.com/sirupsen/logrus"
)

// OntologiesOf serves GET /admin/api/ontologies?db=<name> for one of dbs (the first without db): the ontologies of every user, each with
// its spec (the ontology as JSON), for the graph view of the admin UI.
func OntologiesOf(dbs []Named) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		name := r.URL.Query().Get("db")
		if name == "" && len(dbs) > 0 {
			name = dbs[0].Name
		}
		for _, n := range dbs {
			if n.Name != name {
				continue
			}
			list, err := relational.ListAllOntologies(r.Context(),
				n.DB)
			if err != nil {
				log.WithError(err).Error("admin: list ontologies failed")
				httputil.Error(w,
					http.StatusInternalServerError,
					"failed to list ontologies")
				return
			}
			httputil.WriteJSON(w,
				http.StatusOK,
				map[string]any{"ontologies": list})
			return
		}
		httputil.Error(w,
			http.StatusNotFound,
			"unknown database")
	}
}
