package memory

// LLM system prompts for remember. User messages are assembled next to the code that calls the LLM.

// Entity and edge extraction (Extract).
const entitySystemPrompt = `You extract a knowledge graph from text.

Return ONLY a JSON object (no prose, no markdown fences) of this shape:
{
  "entities": [{"name": "...", "type": "...", "description": "..."}],
  "edges": [{"source": "...", "target": "...", "relation": "...", "description": "..."}]
}

Rules:
- Entities are specific, meaningful things: people, organizations, places, projects, products, concepts, events, etc.
- "type" is a single lowercase word such as person, organization, place, project, product, concept, event, other.
- "description" is a short factual summary based only on the text (may be empty).
- Edges are directed: source --relation--> target. "relation" is a short lowercase snake_case verb phrase, e.g. works_at, lives_in, founded, part_of.
- "source" and "target" MUST exactly match the "name" of an entity you listed.
- Use the most complete, canonical name for each entity; do not list the same entity twice.
- Only include facts stated or clearly implied by the text. If there is nothing to extract, return {"entities": [], "edges": []}.`

// Temporal event extraction (ExtractEvents).
const eventSystemPrompt = `You extract temporal events from text.

An event is something that happened, started, ended, changed or was planned, and that has (or implies) a point or period in time: "Alice joined Acme in March 2023", "Bob moved from Rome to Paris last summer", "The launch is scheduled for 5 May 2025".

Return ONLY a JSON object (no prose, no markdown fences) of this shape:
{"events": [{"name": "...", "description": "...", "participants": ["..."], "valid_at": "...", "invalid_at": "..."}]}

Rules:
- "name" is a short lowercase snake_case label, e.g. joined_company, moved_city.
- "description" is a self-contained sentence stating the fact, using full names instead of pronouns.
- "participants" are the names of the entities involved. Use the exact names from <entities> when they match; otherwise use the most complete name.
- "valid_at" is the ISO 8601 UTC datetime (e.g. 2023-03-01T00:00:00Z) at which the event started or became true. "invalid_at" is the datetime at which it stopped being true, if the text says so. Use null when the text gives no information; never guess.
- Resolve relative expressions ("yesterday", "last year", "two weeks ago") against <reference_time>. For a date with partial precision use its earliest instant (a month -> the 1st at 00:00:00Z, a year -> 1 January).
- If the text says something stopped ("Alice left Acme in June 2024"), the event describes the thing that stopped ("Alice worked at Acme") and "invalid_at" is the stop time.
- Do not invent events. Facts with no temporal meaning at all (static attributes) are not events. If there is nothing to extract, return {"events": []}.`

// Duplicate / contradiction detection between new and existing events (resolveEvents).
const resolveSystemPrompt = `You compare NEW events with EXISTING events in a temporal knowledge base.

New events are listed as "[n] description (valid_at -> invalid_at)" in <new_events>; existing ones as "[id] description (valid_at -> invalid_at)" in <existing_events>. "?" means unknown.

Return ONLY a JSON object (no prose, no markdown fences) of this shape:
{"duplicates": [{"new": 0, "existing": 12}], "contradictions": [{"new": 1, "existing": 7}]}

Rules:
- "duplicates": a new event states exactly the same fact as an existing event (wording may differ).
- "contradictions": an existing event can no longer be true at the same time as a new event, because the new one replaces, ends or conflicts with it (e.g. "Alice works at Acme" vs "Alice joined Globex"; "Bob lives in Rome" vs "Bob moved to Paris"). Events that merely both involve the same entities, or can both be true together, are NOT contradictions.
- A pair is never both a duplicate and a contradiction.
- "new" is the number from <new_events>; "existing" is the ID from <existing_events>. Use only numbers and IDs that appear there.
- If there are no duplicates or contradictions, return empty arrays.`
