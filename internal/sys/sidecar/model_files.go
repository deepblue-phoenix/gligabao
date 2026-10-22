package sidecar

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// DownloadModelFile streams one file of a trained model from the sidecar into w (GET /models/file): modelPath is the model's
// Path on the sidecar's models volume ("trained/<job id>"), file one of the model's files (relative to that directory). It
// returns the number of bytes written. A failure is a *StatusError (the sidecar answers 400 for an invalid path, 404 for a file
// that is not there, 502 when it cannot read it) or a transport error; a body that ends early is an error too, but w may
// already have got part of the file, so the caller must not trust w after an error.
func (c *Client) DownloadModelFile(ctx context.Context, modelPath, file string, w io.Writer) (int64, error) {
	q := url.Values{"model_path": {modelPath}, "file": {file}}
	resp, err := c.Open(ctx, http.MethodGet, "/models/file", q.Encode(), nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, &StatusError{Method: http.MethodGet, Path: "/models/file", Code: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	n, err := io.Copy(w, resp.Body)
	if err != nil {
		return n, fmt.Errorf("sidecar GET /models/file %s/%s: reading the body: %w", modelPath, file, err)
	}
	return n, nil
}
