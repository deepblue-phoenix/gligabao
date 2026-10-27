package httputil

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
)

// Authenticator authenticates a request from the API key it carries and returns the ID of the user the key belongs to. Any error
// rejects the request (401).
type Authenticator func(ctx context.Context, apiKey string) (userID string, err error)

// maxAuthBody is the largest body Authenticate reads to set the user_id of (the handlers keep their own, tighter limits).
const maxAuthBody = 32 << 20

// Authenticate wraps next so that every request must carry an API key ("Authorization: Bearer <key>") that auth accepts, and acts
// as the user auth returns: the request's user_id (the "user_id" of a JSON body, or the "user_id" query parameter when there is no
// body) is set to that user, replacing whatever the caller sent, so a caller cannot act for anyone but the owner of its key.
// A request without a key, or whose key auth rejects, gets 401 and never reaches next. With a nil auth, next is returned as is.
func Authenticate(auth Authenticator, next http.Handler) http.Handler {
	if auth == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		if r.Method == http.MethodGet || r.Method == http.MethodDelete {
			q := r.URL.Query()
			q.Set("user_id", userID)
			r.URL.RawQuery = q.Encode()
			next.ServeHTTP(w, r)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAuthBody))
		if err != nil {
			Error(w, http.StatusBadRequest, "cannot read the body: "+err.Error())
			return
		}
		obj := map[string]json.RawMessage{}
		if len(bytes.TrimSpace(raw)) > 0 {
			if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
				Error(w, http.StatusBadRequest, "invalid JSON body: expected an object")
				return
			}
		}
		obj["user_id"], _ = json.Marshal(userID)
		raw, _ = json.Marshal(obj)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		r.Header.Set("content-length", strconv.Itoa(len(raw)))
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
