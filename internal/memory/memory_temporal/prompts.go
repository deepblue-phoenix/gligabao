package memory_temporal

// LLM system prompt of the temporal reconciliation. The user message is assembled next to the code that calls the LLM.

// Duplicate / contradiction detection between new and existing facts (ResolveEdges).
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
