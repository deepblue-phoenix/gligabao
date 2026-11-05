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
- A move, replacement or departure ends the old fact: "X moved its office from A to B" gives an edge X located_in B (valid_at = the move date) and an edge X located_in A with invalid_at = the move date. A fact that is only planned for the future has not ended yet. If the text says a fact ended but not when it started, set "valid_at" equal to "invalid_at" (not <reference_time>).
- Only include facts stated or clearly implied by the text. If there is nothing to extract, return {"entities": [], "edges": []}.`

// Summary of a chunk of a long text (Summarize).
const summarySystemPrompt = `You summarize a passage taken from a longer document.

Write a faithful summary of the passage inside <text>, in two to four sentences of plain prose (no lists, no markdown, no preamble such as "This passage"). Name the people, organizations, places and dates that matter, say what happens or is claimed, and keep every figure exact. Use only what the passage says: add nothing from outside it, and do not comment on it. The passage may begin and end mid-sentence, because it was cut out of the document: summarize what is there. Reply with the summary only.`

// Classification of the graph of an artifact (classifyGraph).
const classifySystemPrompt = `You classify a knowledge graph: you say what kind of graph it is, from its entities and facts.

The graph is inside <graph>. Return ONLY a JSON object (no prose, no markdown fences) of this shape:
{"classes": ["..."]}

Rules:
- Give one to three classes, the best fitting first. Most graphs need one; give more only if the graph clearly is several kinds of thing.
- A class names the kind of situation or domain the graph describes as a whole (for example corporate_ownership, supply_chain, family, software_dependencies, geopolitics), not one of its entities and not a single fact.
- A class is a short lowercase snake_case name of one to four words.
- If <allowed_classes> is given, you MUST choose exactly ONE class, only from it, exactly as written: the one that fits the graph closest, even if none fits well. Ignore the one-to-three rule and the idea of making up a class.
- Otherwise, if <existing_classes> is given, reuse one of them when it fits the graph; make a new class only when none of them does.`
