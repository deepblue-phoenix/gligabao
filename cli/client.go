package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// Environment variables of the client commands.
const (
	// EnvServer is the URL of the server the commands talk to (the default of --server). Without it the commands use the local daemon.
	EnvServer = "GLIGABAO_URL"
	// EnvUser is the default of --user.
	EnvUser = "GLIGABAO_USER"
	// EnvAPIKey is the API key sent to a server given with --server (the same variable as the Python client's).
	EnvAPIKey = "GLIGABAO_API_KEY"
)

// Exit codes of the client commands.
const (
	exitOther       = 1
	exitUsage       = 2 // bad usage, or a 400 of the server
	exitNotFound    = 3
	exitConflict    = 4
	exitUnavailable = 5 // the server (or its LLM) is not available: 5xx, or the daemon could not start
)

// exitError is an error that ends the process with a given exit code. msg is printed to stderr as {"error": msg, "status": ...}.
type exitError struct {
	code   int
	status int
	msg    string
}

func (e *exitError) Error() string { return e.msg }

// exitCodeOf maps an HTTP status to the exit code.
func exitCodeOf(status int) int {
	switch {
	case status == 404:
		return exitNotFound
	case status == 409:
		return exitConflict
	case status >= 500:
		return exitUnavailable
	case status >= 400:
		return exitUsage
	}
	return exitOther
}

// printError writes the error to w as {"error": ..., "status": ...}.
func printError(w io.Writer, err error) {
	out := map[string]any{"error": err.Error()}
	var ee *exitError
	if errors.As(err, &ee) && ee.status != 0 {
		out["status"] = ee.status
	}
	b, _ := json.Marshal(out)
	fmt.Fprintln(w, string(b))
}

// codeOf is the exit code of err.
func codeOf(err error) int {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return exitOther
}

func usageErr(format string, args ...any) error {
	return &exitError{code: exitUsage, msg: fmt.Sprintf(format, args...)}
}

//---------------------------------------------------------------------------------------------

// clientFlags are the persistent flags of the client commands.
type clientFlags struct {
	server string
	user   string
}

// apiClient calls the REST API of a gligabao server.
type apiClient struct {
	base string
	key  string // sent as a bearer token; only for a server given with --server (the local daemon has no authentication)
	user string
}

// do sends the request and returns the status and the body of the response. No timeout: a remember takes as long as its LLM calls.
func (c *apiClient) do(ctx context.Context, method, path string, query url.Values, body []byte) (int, []byte, error) {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, nil, usageErr("bad request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, &exitError{code: exitUnavailable, msg: fmt.Sprintf("cannot reach the server at %s: %v", c.base, err)}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, b, err
}

// call is do for the commands: a failed request (status 400 and up) becomes an exitError with the server's message, and the body of
// a successful one is returned.
func (c *apiClient) call(ctx context.Context, method, path string, query url.Values, body []byte) ([]byte, error) {
	status, b, err := c.do(ctx, method, path, query, body)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		msg := strings.TrimSpace(string(b))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		if msg == "" {
			msg = http.StatusText(status)
		}
		return nil, &exitError{code: exitCodeOf(status), status: status, msg: msg}
	}
	return b, nil
}

// client returns the client of the target: the server of --server (GLIGABAO_URL), or the local daemon, started first if it is not running.
func (c *CLI) client(cmd *cobra.Command) (*apiClient, error) {
	server := c.flags.server
	if server != "" {
		if _, err := url.ParseRequestURI(server); err != nil {
			return nil, usageErr("bad --server %q: %v", server, err)
		}
		return &apiClient{base: strings.TrimRight(server, "/"), key: os.Getenv(EnvAPIKey), user: c.flags.user}, nil
	}
	d, err := c.localDaemon(cmd)
	if err != nil {
		return nil, err
	}
	if err := d.ensure(cmd.Context()); err != nil {
		return nil, err
	}
	return &apiClient{base: d.url(), user: c.flags.user}, nil
}

// localDaemon builds the local daemon from the layout (see resolveLayout).
func (c *CLI) localDaemon(cmd *cobra.Command) (*daemon, error) {
	l, err := resolveLayout(c.configPath, cmd.Flags().Changed("config") || cmd.Root().PersistentFlags().Changed("config"))
	if err != nil {
		return nil, err
	}
	return newDaemon(l)
}

// emit prints a response body to stdout as indented JSON (as it is if it is not JSON).
func emit(cmd *cobra.Command, b []byte) {
	var out bytes.Buffer
	if json.Indent(&out, bytes.TrimSpace(b), "", "  ") != nil {
		out.Reset()
		out.Write(bytes.TrimSpace(b))
	}
	fmt.Fprintln(cmd.OutOrStdout(), out.String())
}

// readBody reads a request body given as JSON text, "@file" or "-" (stdin).
func readBody(cmd *cobra.Command, spec string) ([]byte, error) {
	switch {
	case spec == "-":
		return io.ReadAll(cmd.InOrStdin())
	case strings.HasPrefix(spec, "@"):
		return os.ReadFile(spec[1:])
	}
	return []byte(spec), nil
}

// bodyFromFlags starts the request body from --json (an object: JSON text, @file or -), if given.
func bodyFromFlags(cmd *cobra.Command, spec string) (map[string]any, error) {
	body := map[string]any{}
	if spec == "" {
		return body, nil
	}
	b, err := readBody(cmd, spec)
	if err != nil {
		return nil, usageErr("--json: %v", err)
	}
	if err := json.Unmarshal(b, &body); err != nil {
		return nil, usageErr("--json is not a JSON object: %v", err)
	}
	return body, nil
}

// pairs turns STAGE=VALUE flags into a map.
func pairs(flag string, list []string) (map[string]string, error) {
	if len(list) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(list))
	for _, kv := range list {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || v == "" {
			return nil, usageErr("--%s %q: want STAGE=VALUE", flag, kv)
		}
		m[k] = v
	}
	return m, nil
}
