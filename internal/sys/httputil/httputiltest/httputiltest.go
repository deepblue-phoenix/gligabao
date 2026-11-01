// Package httputiltest has helpers for the tests of the handlers of the API.
package httputiltest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
)

// NewRequest is httptest.NewRequest for the handlers of the routes of users, which take the user from the gb-user-id header only
// (httputil.UserID): the "user_id" of the JSON body, or of the query string, if there is one, is what the header is set to, so a
// test can keep writing the user in the request it builds. That field itself is never read by the handlers.
func NewRequest(method, target string, body io.Reader) *http.Request {
	var raw []byte
	if body != nil {
		raw, _ = io.ReadAll(body)
		body = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, body)
	user := req.URL.Query().Get("user_id")
	var obj struct {
		UserID string `json:"user_id"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.UserID != "" {
		user = obj.UserID
	}
	if user != "" {
		req.Header.Set(httputil.UserIDHeader, user)
	}
	return req
}
