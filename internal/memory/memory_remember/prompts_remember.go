package memory_remember

// LLM system prompts for remember. User messages are assembled next to the code that calls the LLM.

// Entity and temporal edge extraction (Extract).
const entitySystemPrompt = `You extract a temporal knowledge graph from text.

The text was written or observed at <reference_time>. Return ONLY a JSON object (no prose, no markdown fences) of this shape:
{
  "entities": [{"name": "...", "type": "...", "description": "..."}],
  "edges": [{"source": "...", "target": "...", "relation": "...", "description": "...", "valid_at": "...", "invalid_at": "..."}]
}

Rules:
- Entities are specific, meaningful things: people, organizations, places, projects, products, concepts, events, etc.
- "type" is a single lowercase word such as person, organization, place, project, product, concept, event, other.
- "description" of an entity is a short factual summary based only on the text (may be empty).
- Edges are facts, directed: source --relation--> target. "relation" is a short lowercase snake_case verb phrase, e.g. works_at, lives_in, founded, part_of.
- "description" of an edge is the fact as one self-contained sentence using full names, e.g. "Alice works at Acme."
- "source" and "target" MUST exactly match the "name" of an entity you listed.
- Use the most complete, canonical name for each entity; do not list the same entity twice.
- "valid_at" is the ISO 8601 UTC datetime (e.g. 2023-03-01T00:00:00Z) at which the fact became true. If the text does not say when it started (including present-tense statements such as "Alice works at Acme", or "Alice started working at Globex" with no date), use <reference_time>.
- Resolve relative expressions ("yesterday", "last year", "two weeks ago") against <reference_time>. For a date with partial precision use its earliest instant (a month -> the 1st at 00:00:00Z, a year -> 1 January).
- "invalid_at" is the datetime at which the fact stopped being true, only if the text says it ended ("Alice left Acme in June 2024" -> edge Alice works_at Acme with invalid_at 2024-06-01T00:00:00Z). Otherwise null.
- Only include facts stated or clearly implied by the text. If there is nothing to extract, return {"entities": [], "edges": []}.`

// Summary of a chunk of a long text (Summarize).
const summarySystemPrompt = `You summarize a passage taken from a longer document.

Write a faithful summary of the passage inside <text>, in two to four sentences of plain prose (no lists, no markdown, no preamble such as "This passage"). Name the people, organizations, places and dates that matter, say what happens or is claimed, and keep every figure exact. Use only what the passage says: add nothing from outside it, and do not comment on it. The passage may begin and end mid-sentence, because it was cut out of the document: summarize what is there. Reply with the summary only.`

// Duplicate / contradiction detection between new and existing facts (resolveEdges).
const resolveSystemPrompt = `You compare NEW facts with EXISTING facts in a temporal knowledge base.

New facts are listed as "[n] fact (valid_at -> invalid_at)" in <new_facts>; existing ones as "[id] fact (valid_at -> invalid_at)" in <existing_facts>. "?" means unknown.

Return ONLY a JSON object (no prose, no markdown fences) of this shape:
{"duplicates": [{"new": 0, "existing": 12}], "contradictions": [{"new": 1, "existing": 7}]}

Rules:
- "duplicates": a new fact states exactly the same thing as an existing fact (wording may differ).
- "contradictions": an existing fact can no longer be true at the same time as a new fact, because the new one replaces, ends or conflicts with it (e.g. "Alice works at Acme" vs "Alice works at Globex" or "Alice joined Globex"; "Bob lives in Rome" vs "Bob lives in Paris"). Facts that merely involve the same entities, or can both be true together (e.g. "Alice works at Acme" vs "Alice lives in Paris"), are NOT contradictions.
- A pair is never both a duplicate and a contradiction.
- "new" is the number from <new_facts>; "existing" is the ID from <existing_facts>. Use only numbers and IDs that appear there.
- If there are no duplicates or contradictions, return empty arrays.`
