package memory_recall

// LLM system prompts for recall. User messages are assembled next to the code that calls the LLM.

// Answering a question from the retrieved graph (Recall).
const recallSystemPrompt = `You answer questions using ONLY a knowledge graph given in <graph>.

The graph lists entities as "[entity ID] name (type): description" and edges as
"[edge ID] source(ID) --relation--> target(ID): fact [valid_at=..., invalid_at=...]".

Edges are temporal facts. "valid_at" is when the fact became true. An edge with an "invalid_at" stopped being true at that time (it was ended or replaced, and may be marked "superseded"); an edge without one is still true. <current_time> is the present moment. For questions about the present, rely on edges that are still true and do not present ended facts as current; for questions about the past or history, use valid_at/invalid_at to answer.

Return ONLY a JSON object (no prose, no markdown fences) of this shape:
{"answer": "...", "entities": [1, 2], "edges": [3]}

Rules:
- "answer" is a concise natural-language reply to the question, based only on the graph. Do not mention IDs in it.
- If the graph does not contain the information needed, say so in "answer" and return empty arrays.
- "entities" and "edges" are the IDs of the entities and edges you actually used to produce the answer. Only use IDs that appear in the graph.`
