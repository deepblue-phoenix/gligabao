package web

import (
	"context"
	"errors"

	"github.com/deepblue-phoenix/gligabao/internal/llm"
)

// searchAnthropic runs the search in Anthropic's cloud (llm.Client.WebSearch) and maps its answer.
func searchAnthropic(ctx context.Context,
	d Deps,
	req SearchRequest) (*SearchResponse, error) {
	if d.LLM == nil {
		return nil, errors.New("web: anthropic search needs an Anthropic client")
	}
	var loc *llm.WebSearchLocation
	if l := req.Location; l != nil {
		loc = &llm.WebSearchLocation{
			Country:  l.Country,
			Region:   l.Region,
			City:     l.City,
			Timezone: l.Timezone,
		}
	}
	r, err := d.LLM.WebSearch(ctx, llm.WebSearchRequest{
		System:         req.System,
		Prompt:         req.Query,
		Model:          req.Model,
		MaxTokens:      req.MaxTokens,
		MaxUses:        req.MaxUses,
		AllowedDomains: req.AllowedDomains,
		BlockedDomains: req.BlockedDomains,
		Location:       loc,
	})
	if err != nil {
		return nil, err
	}
	out := &SearchResponse{
		Provider:     Anthropic,
		Answer:       r.Text,
		Queries:      r.Queries,
		Searches:     r.Searches,
		Model:        r.Model,
		InputTokens:  r.InputTokens,
		OutputTokens: r.OutputTokens,
		CostUSD:      r.CostUSD,
	}
	for _, s := range r.Sources {
		out.Results = append(out.Results, Result{
			URL:     s.URL,
			Title:   s.Title,
			PageAge: s.PageAge,
		})
	}
	return out, nil
}
