package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// post sends body as JSON to url and decodes the reply into out. It stops when ctx ends.
func post(ctx context.Context, url string, body, out any) error {
	b, _ := json.Marshal(body)
	// A chunked text is extracted chunk by chunk in one request, so it can take long.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("gb-user-id", EvalUser) // the handlers take the user from this header only
	resp, err := (&http.Client{Timeout: 30 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(data))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			msg = e.Error // the server's reason, without the JSON around it
		}
		return fmt.Errorf("status %d: %s", resp.StatusCode, msg)
	}
	return json.Unmarshal(data, out)
}
