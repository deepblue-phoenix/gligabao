package httputil

import (
	"context"
	"net/http"
	"strings"

	log "github.com/sirupsen/logrus"
)

// UserIDHeader is the request header that carries the ID of the user a request acts as. It is the only place the API takes the
// user from (see UserID): a "user_id" in the query string or in the JSON body of a request is never read.
const UserIDHeader = "gb-user-id"

// UserID returns the ID of the user r acts as: its UserIDHeader header, trimmed ("" if there is none). Validate it with
// memory_core.ResolveUserID.
func UserID(r *http.Request) string { return strings.TrimSpace(r.Header.Get(UserIDHeader)) }

// Authenticator authenticates a request from the API key it carries and returns the ID of the user the key belongs to. Any error
// rejects the request (401).
type Authenticator func(ctx context.Context, apiKey string) (userID string, err error)

// Authenticate wraps next so that the request acts as one user, which the handlers read with UserID:
//
//   - With an auth hook (auth not nil), every request must carry an API key ("Authorization: Bearer <key>") that auth accepts, and
//     acts as the user auth returns: the UserIDHeader header is set to it, replacing whatever the caller sent. A request without a
//     key, or whose key auth rejects, gets 401 and never reaches next.
//   - Without one (auth nil), the request acts as the user in its UserIDHeader header, trusted as is. Without the header it gets 400.
func Authenticate(auth Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth == nil {
			if UserID(r) == "" {
				Error(w, http.StatusBadRequest, "missing user: send the "+UserIDHeader+" header")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		key, ok := bearerToken(r)
		if !ok {
			w.Header().Set("www-authenticate", "Bearer")
			Error(w, http.StatusUnauthorized, "missing API key (send Authorization: Bearer <key>)")
			return
		}
		userID, err := auth(r.Context(), key)
		if err != nil || strings.TrimSpace(userID) == "" {
			if err != nil {
				log.WithError(err).Debug("authentication failed")
			}
			w.Header().Set("www-authenticate", "Bearer")
			Error(w, http.StatusUnauthorized, "invalid API key")
			return
		}
		r.Header.Set(UserIDHeader, userID)
		next.ServeHTTP(w, r)
	})
}

// bearerToken returns the API key of an "Authorization: Bearer <key>" header.
func bearerToken(r *http.Request) (string, bool) {
	scheme, key, ok := strings.Cut(strings.TrimSpace(r.Header.Get("authorization")), " ")
	key = strings.TrimSpace(key)
	if !ok || !strings.EqualFold(scheme, "Bearer") || key == "" {
		return "", false
	}
	return key, true
}
