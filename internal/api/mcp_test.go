package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

// TestMCPRunsTheRESTHandlers checks that the MCP tools go through the validation of the REST handlers: with no LLM, remember and
// query answer 503 there and a tool error here, and an invalid user_id is the handler's 400.
func TestMCPRunsTheRESTHandlers(t *testing.T) {
	dbtest.Run(t, func(t *testing.T, d *sql.DB) {
		database := &db.DB{Relational: d, Graph: d}
		mux := http.NewServeMux()
		RegisterMCP(mux, mgrFor(t, database, nil), database, nil, nil, nil, nil, nil, "test")
		srv := httptest.NewServer(mux)
		defer srv.Close()
		cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil).Connect(t.Context(),
			&sdk.StreamableClientTransport{Endpoint: srv.URL + "/v1/gb/mcp", DisableStandaloneSSE: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer cs.Close()
		for name, args := range map[string]map[string]any{
			"remember": {"user_id": "u1", "text": "Alice works at Acme."},
			"query":    {"user_id": "u1", "query": "Where does Alice work?"},
		} {
			res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, "LLM not configured") {
				t.Fatalf("%s: %+v", name, res)
			}
		}
	})
}
