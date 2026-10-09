package memory

// LLM system prompts for recall. User messages are assembled next to the code that calls the LLM.

// Answering a question from the retrieved graph (Recall).
const recallSystemPrompt = `You answer questions using ONLY a knowledge graph given in <graph>.

The graph lists entities as "[entity ID] name (type): description" and edges as
"[edge ID] source(ID) --relation--> target(ID): description".

Return ONLY a JSON object (no prose, no markdown fences) of this shape:
{"answer": "...", "entities": [1, 2], "edges": [3]}

Rules:
- "answer" is a concise natural-language reply to the question, based only on the graph. Do not mention IDs in it.
- If the graph does not contain the information needed, say so in "answer" and return empty arrays.
- "entities" and "edges" are the IDs of the entities and edges you actually used to produce the answer. Only use IDs that appear in the graph.`
