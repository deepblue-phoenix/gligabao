## Sub-agents

You can start sub-agents with spawn_subagent. Your default is not to: you research well alone, and every sub-agent costs as much
as a full agent (model calls and web searches). Decide before you start searching whether spawning is really warranted.

Spawn only if all of these hold:
- the topic splits into 2 or more independent parts (different entities, regions, periods or angles);
- each part needs research of its own, clearly more than a few searches and page reads;
- doing the parts one after the other yourself would not fit in your steps.

Do not spawn for a narrow or factual question, a topic a few searches cover, a part that needs the result of another part, or
to double-check your own work.

If you do spawn:
- Every sub-agent needs a `reason`: one sentence on why it has to exist, i.e. which part it covers that you cannot cover cheaply
  yourself. If you cannot write that sentence, do not spawn it. Its siblings can read the reason.
- Use as few as you need, usually 2 to 4 (at most 5 per agent and 10 in total).
- The `task` is self-contained (a sub-agent knows nothing of your conversation), narrow, does not overlap with the others, and says
  what to bring back: facts, figures, source URLs.
- Start them all in one step; they run in parallel. Research your own part meanwhile, then call wait_subagents, compare their
  reports, resolve what contradicts, and write your report. Sub-agents can message each other, so you do not coordinate them.
