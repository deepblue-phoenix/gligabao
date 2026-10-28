package memory_query

// Geo for Query (see package internal/memory/memory_geo): finds the places a question mentions ("Where does Alice live in Paris?" mentions
// Paris). It runs when the call has a geo resolver (memory_core.WithGeoResolver, the same switch as for Remember), with one LLM
// call of its own (the detect_geo stage, which takes a model like the other stages: see memory_core.QueryStages).
//
// For now the places are only extracted: they are in QueryResult.Geo (for Go callers, not in the JSON), in the trajectory and in
// the log, and nothing else uses them: the retrieval and the answer are the same with or without them. The resolver is not
// asked for coordinates either. A failure here is logged and recorded, and never fails Query.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_geo"
)

const (
	// maxQueryPlaces bounds how many places are kept, however many the LLM finds.
	maxQueryPlaces = 10
	// maxQueryPlaceLen bounds the text and the query of a place (bytes).
	maxQueryPlaceLen = 200
)

// QueryPlace is a place the question mentions.
type QueryPlace struct {
	// Text is the place as the question writes it ("Paris", "the Alps").
	Text string `json:"text"`
	// Kind is what the place is: one of geo.Kinds ("country", "region", "city" or "place").
	Kind string `json:"kind"`
	// Query is what a geo resolver should be asked for: the name, with what disambiguates it when the question says so
	// ("Paris, Texas"). It is Text if there is nothing to add.
	Query string `json:"query"`
}

const detectQueryGeoSystemPrompt = `You find the places that a question mentions, so that they can be looked up later. You do not answer the question.

<question> holds the question.

Return ONLY a JSON object (no prose, no markdown fences) of this shape:
{"places": [{"text": "Paris", "kind": "city", "query": "Paris, France"}]}

Rules:
- A place is a location on Earth that has coordinates: a country, a region (a state, province, county, or a named area such as "Silicon Valley" or "the Alps"), a city, town or village, or anything smaller or more specific such as a landmark, a building, a street address, a mountain, a lake or an island.
- "text" is the place exactly as the question writes it. Only places that are in the question.
- "kind" is one of: "country", "region", "city", "place" (anything smaller than a city, or not an administrative unit).
- "query" is what a geocoder should be asked: the name, followed by the place it lies in when that is needed to tell it from others of the same name and the question says or implies it ("Springfield, Illinois, USA"). Never invent a country or region the question does not suggest. If nothing needs adding, it is the same as "text".
- An organization, a person, a product, an event or a concept is NOT a place, even if its name contains a place name ("Bank of America", "Paris Hilton"). Words that point at a place without naming it ("here", "home", "nearby", "abroad") are not places.
- Prefer precision to recall: when you are not sure that something is a place, leave it out. Most questions mention no place, and an empty list is a normal answer.`

// parseQueryPlaces decodes the LLM's answer and keeps the valid, distinct places, in the order given: those whose text is in
// the question (an LLM that makes one up is ignored). A kind that is not one of geo.Kinds becomes "place"; an empty or too long
// query becomes the text.
func parseQueryPlaces(out, question string) ([]QueryPlace, error) {
	js, err := memory_core.JSONObject(out)
	if err != nil {
		return nil, err
	}
	var v struct {
		Places []struct {
			Text  string `json:"text"`
			Kind  string `json:"kind"`
			Query string `json:"query"`
		} `json:"places"`
	}
	if err := json.Unmarshal([]byte(js), &v); err != nil {
		return nil, fmt.Errorf("memory: decode LLM detect_geo output: %w", err)
	}
	q := strings.ToLower(strings.Join(strings.Fields(question), " "))
	var res []QueryPlace
	seen := map[string]bool{}
	for _, p := range v.Places {
		text := strings.Join(strings.Fields(p.Text), " ")
		key := strings.ToLower(text)
		if text == "" || len(text) > maxQueryPlaceLen || seen[key] || !strings.Contains(q, key) {
			continue
		}
		seen[key] = true
		kind := memory_geo.NormKind(p.Kind)
		if kind == "" {
			kind = memory_geo.KindPlace
		}
		query := strings.Join(strings.Fields(p.Query), " ")
		if query == "" || len(query) > maxQueryPlaceLen {
			query = text
		}
		res = append(res, QueryPlace{Text: text, Kind: kind, Query: query})
		if len(res) == maxQueryPlaces {
			break
		}
	}
	return res, nil
}

// detectPlaces asks the LLM which places the question mentions. llm is already the one of the detect_geo stage. It returns the
// places (nil if there are none) and the error, if the call or its output failed; the caller only records it.
func detectPlaces(ctx context.Context, llm memory_core.LLM, question string) ([]QueryPlace, error) {
	resp, err := llm.Complete(ctx, ai.Request{
		System:    detectQueryGeoSystemPrompt,
		Messages:  []ai.Message{{Role: ai.RoleUser, Content: "<question>\n" + question + "\n</question>"}},
		MaxTokens: 1024,
	})
	if err != nil {
		return nil, err
	}
	if resp.StopReason == "max_tokens" {
		return nil, errors.New("LLM output truncated")
	}
	return parseQueryPlaces(resp.Text, question)
}
