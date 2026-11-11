# Deep research loop: influences

Several research systems separate a dynamic **question/gap queue** (the frontier: what is still to ask) from an accumulated **knowledge workspace** (the notes: what is known). The [deep research loop](deep-research-loop.md) follows the same split. This page summarizes the systems that implement it.

The motivation is shared: a single growing conversation drifts, bloats and stops too early. Keeping *what to ask* and *what is known* in two explicit structures lets the agent pick the next step from the gap between them, keeps each context small, and gives a clear point to stop (the frontier is empty or exhausted) before writing.

## Stanford STORM

STORM (Synthesis of Topic Outlines through Retrieval and Multi-perspective Questioning) generates Wikipedia-depth, cited reports from scratch. It is the canonical academic example of the split.

- **Pre-writing stage:** the work is done before any article text exists. Related topics are surveyed first to discover perspectives, so the questions do not come from the model's first guess alone.
- **Frontier:** simulated expert personas (e.g. academic, critic, engineer) each hold a conversation with a topic expert. The questioner reads the conversation so far and asks the next question, so follow-ups are discovered dynamically and the set of open inquiries grows as a tree.
- **Grounding:** the expert side turns each question into search queries, filters untrustworthy sources and answers only from retrieved material, so every answer carries citations.
- **Notes:** conversations and snippets are collected and organized into an outline tree (a draft outline first, refined with the conversation content) and an evidence table.
- **Writing:** the article is written section by section from the outline and the cited evidence for that section, then polished (e.g. deduplicated, lead section added). Writing starts only once the perspective-driven questions are satisfied.
- **Takeaway for us:** diverse question sources (perspectives) widen coverage; the outline is a view of the notes that decides what the report says and in which order.

## LangChain Open Deep Research (LangGraph)

LangChain's open-source deep research agent, built on LangGraph. It separates the control loop from the working notes to avoid token bloat and context drift.

- **Scoping:** before research, the agent clarifies the request if needed and turns the conversation into a research brief, the single source of truth for the rest of the run.
- **Supervisor as frontier manager:** a planner agent decomposes the brief into discrete, isolated research questions and decides how many sub-agents to run. It reflects after each round to decide whether more research is needed, and it has a bounded number of iterations and parallel sub-agents.
- **Sub-agents:** each takes one question into an isolated, short-lived context window with its own search tools and a tool-call budget. Parallel runs are possible because the contexts do not share a history.
- **Compression:** a sub-agent ends by compressing its findings into a cleaned summary that keeps the claims and their source URLs and drops the raw pages. Only this goes back to the supervisor.
- **Shared state (`notes`):** the compressed findings are appended to the graph state through a reducer. The supervisor sees notes, not raw tokens.
- **Writing:** once the supervisor judges the plan complete, a separate synthesis step writes the final report from the brief and the notes in one pass.
- **Takeaway for us:** isolation plus compression keeps the planner's context small; hard limits on iterations and parallelism bound cost.

## DualGraph (arXiv 2026)

A recent paper that formalizes the split as *Dual-Graph Co-evolution*: two graphs that are updated together as the research proceeds. The description below comes from a secondary summary, not from the paper itself; check the paper before relying on details.

- **Outline Graph (OG):** the dynamic frontier. Nodes are planned sections and unresolved sub-questions; edges give structure and dependencies. Each node tracks its citations, so it is visible which aspects still lack empirical evidence.
- **Knowledge Graph (KG):** the structured notes. Entities, relations and citation-backed claims are extracted from retrieved pages. Clustering groups related entities and claims.
- **Loop:** the agent compares the KG (what is proven) with the OG (what still needs answering). Nodes with no or weak evidence produce the next batch of search queries; new evidence can in turn add, split or close OG nodes. Both graphs change every round, hence "co-evolution".
- **Takeaway for us:** the gap is computed from the two structures rather than judged freely by the model, and the notes can be a graph instead of flat text.

## GPT-Researcher

A widely used open-source research framework. It is simpler than the graph systems but keeps the split through a planner/executor pipeline.

- **Planning:** a planner agent generates an initial set of research questions or sub-queries from the task (often after a first quick search for context). Queries are expanded dynamically if source validation fails.
- **Execution:** one crawler agent per sub-query runs in parallel, scrapes the sources and summarizes each one with the source tracked.
- **Notes:** the results are aggregated into an isolated markdown context. Redundant text is pruned and the context is filtered by embedding (cosine) similarity to the query, so only relevant passages stay.
- **Writing:** a publisher/writer agent receives only the distilled context plus the source list and writes the report, with citations.
- **Takeaway for us:** a cheap, parallel, query-per-source pass with a relevance filter is a good baseline for the extraction step.

## What they share

- **Explicit frontier:** open questions are a first-class, inspectable structure, not implicit in the chat history.
- **Compressed evidence:** raw pages are reduced to cited claims before they reach the planner or the writer.
- **Gap-driven next step:** the next queries come from what is missing or unverified, not from the last message.
- **Separate synthesis:** the report is written only after exploration ends, from the notes alone.
- **Isolation (STORM, LangGraph):** parallel perspectives or sub-agents work in short-lived contexts and report back only distilled results.

## Mapping to this project

| Pattern | In the loop |
| --- | --- |
| Frontier / task queue / Outline Graph | the [question frontier](deep-research-loop.md#the-question-frontier) (bounded, prioritized, depth-capped, with branching and pruning) |
| Notes / evidence units / Knowledge Graph | the [`notes.md` working state](deep-research-loop.md#working-state-notesmd) and the claim ledger (`UNVERIFIED` -> `VERIFIED`) |
| Sub-agent with isolated context | [sub-agents](subagents.md) |
| Separate writer | the [exploration/synthesis split](deep-research-loop.md#two-loops) and the gated `finalize_report` tool |

## Comparison

| System | Frontier (what to ask) | Notes (what is known) |
| --- | --- | --- |
| Stanford STORM | Multi-perspective persona question queue | Hierarchical outline with grounded citation sets |
| LangGraph Deep Research | Supervisor task queue with isolated context delegation | Reducer state appending compressed evidence units |
| DualGraph | Outline Graph (OG) with node-level citation tracking | Entity/relation Knowledge Graph (KG) with clustering |
| GPT-Researcher | Programmatic sub-query queue and fallback crawler | Aggregated markdown context filtered by cosine similarity |
