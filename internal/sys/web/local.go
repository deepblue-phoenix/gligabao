package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

const (
	// DefaultMaxChars is how much of a page the model gets when the request sets no limit.
	DefaultMaxChars = 15000
	// maxPageBytes is how much of a page is downloaded.
	maxPageBytes = 1 << 20
)

// fetchLocal downloads the page with a GET and turns its HTML into text.
func fetchLocal(ctx context.Context,
	d Deps,
	req FetchRequest) (*FetchResponse, error) {
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		return nil, errors.New("url must start with http:// or https://")
	}
	maxChars := req.MaxChars
	if maxChars <= 0 {
		maxChars = DefaultMaxChars
	}
	httpReq, err := http.NewRequestWithContext(ctx,
		http.MethodGet,
		req.URL,
		nil)
	if err != nil {
		return nil, err
	}
	if err := checkDomain(httpReq.URL.Hostname(), req.AllowedDomains, req.BlockedDomains); err != nil {
		return nil, err
	}
	// A redirect may not leave the allowed sites. The client is the caller's; a copy carries the check.
	var client http.Client
	if d.HTTP != nil {
		client = *d.HTTP
	}
	client.CheckRedirect = func(r *http.Request,
		via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return checkDomain(r.URL.Hostname(), req.AllowedDomains, req.BlockedDomains)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: status %d",
			req.URL,
			resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return nil, err
	}
	return &FetchResponse{
		Provider: Local,
		URL:      req.URL,
		Text:     PageText(string(body), maxChars),
	}, nil
}

// hostMatches reports whether host is one of the listed hosts or a subdomain of one.
func hostMatches(host string,
	list []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, h := range list {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// checkDomain refuses a host that the allowed and blocked lists do not let through.
func checkDomain(host string,
	allowed, blocked []string) error {
	switch {
	case hostMatches(host, blocked):
		return fmt.Errorf("%s is blocked: you may not read pages of that site", host)
	case len(allowed) > 0 && !hostMatches(host, allowed):
		return fmt.Errorf("%s is not allowed: you may read pages only of %s",
			host,
			strings.Join(allowed, ", "))
	}
	return nil
}

var (
	reInvisible = regexp.MustCompile(`(?is)<(script|style|noscript)\b.*?</(script|style|noscript)>`)
	reTag       = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpace     = regexp.MustCompile(`\s+`)
)

// PageText turns HTML into plain text, cut to max characters. It is crude on purpose.
func PageText(html string,
	max int) string {
	s := reInvisible.ReplaceAllString(html, " ")
	s = reTag.ReplaceAllString(s, " ")
	s = strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}
