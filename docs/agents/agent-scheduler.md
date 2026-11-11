# The agent scheduler

How the server starts [agents](agent-defs.md) by itself, at the times of their CRON `[schedule]`. This page is about the mechanism (a loop inside the server, what it checks, what it skips); the fields and the syntax of `[schedule]` are in [agent-defs.md](agent-defs.md#schedule-running-on-a-schedule), and the runs it starts are ordinary [researches](research.md).

Code: `internal/agent_research/scheduler.go` (the scheduler, `Manager.RunSchedules`), `internal/agent/agent_defs/cron.go` (`ParseCron`, `Cron.Next`, `Cron.MinGap`), `agent_defs.Schedule` in `internal/agent/agent_defs/def.go` (`Next`, `TopicAt`), the startup check in `internal/agent/agent_defs/validate.go` (`checkSchedule`).

## In short

- There **is** a continuous checker: one background goroutine in the server process (`scheduler.loop`), started once at startup by `Manager.RunSchedules()`. There is no external cron and no job table.
- It knows the next due time of every scheduled agent. It sleeps until the earliest one (at most 1 minute at a time, so a changed clock is noticed), then starts a research for each agent that is due.
- If the previous scheduled run of that agent is still working, the new one is **skipped**, not queued.
- Nothing is stored. After a restart the next time after the start is used: the times the server was down are not caught up.

## Startup

`internal/sys/server/server.go` calls `research.RunSchedules()` at the end of the server setup, after the agent definitions, the knowledge base and the other settings are in place. The goroutine stops with the manager's context (server shutdown), and `Manager.Wait` waits for it.

`RunSchedules`:

1. Builds the scheduler from `registry().List()`: every definition with a `[schedule]` that is not `paused` becomes an entry (`scheduled`) with `next = Schedule.Next(now)`: the first time after now the expression matches, read in the schedule's time zone. Each one is logged (`agent research: scheduled agent`, with `cron`, `timezone`, `user`, `next`).
2. If there are no entries it returns 0 and starts nothing.
3. If the manager has no LLM client (`ANTHROPIC_API_KEY` not set) it logs a warning and starts nothing: the agents will not run.
4. Otherwise it starts the loop goroutine and returns the number of scheduled agents (the server logs `N agent(s) run on a schedule`).

An invalid schedule never gets this far: a bad expression, an unknown time zone, a missing `cron` or `topic`, an expression that never matches, or one that runs more often than every 5 minutes (`agent_defs.MinScheduleInterval`) is an error when the definitions are loaded, and the server does not start.

## The loop

```
loop:
    wait = min(max(earliest next - now, 0), 1 minute)      # 1 minute if no agent has a next time
    sleep wait   (or return when the context is done)
    tick(now)
```

`tick(now)` goes through the entries. For each one whose `next` is not after `now` it:

1. takes `due = next`,
2. sets `next = Schedule.Next(now)`: the next match **after now**, not after `due`,
3. calls `fire(entry, due)`.

Because `next` jumps to after `now`, an agent fires **at most once per tick**, even if several of its times have passed (the machine was suspended, the clock jumped, a previous `fire` was slow). The skipped times are not run later.

Cron times are whole minutes (`Cron.Next` truncates to the minute and goes to the one after), so the loop wakes at the due time itself, not at a polling interval; the 1-minute cap only bounds how long it sleeps blind.

`Cron.Next` looks for the next match with a bounded search (8 years; the zero time if there is none) and does not go back when a daylight saving gap makes `time.Date` answer an earlier time.

## Starting a run: `fire`

For an entry that is due at `due`:

1. **Is the previous run still working?** Each entry keeps `lastRun`, the ID of the research its last successful `fire` started (0 at startup). If it is set, `fire` loads that run (`Manager.Get(user, lastRun)`); if its status is `running` (`Status.Active()`), the run is skipped and a warning is logged (`scheduled run skipped, the previous one is still working`). If the run cannot be loaded, it is not treated as working.
2. **The topic**: `Schedule.TopicAt(due)` renders the `topic` template with `{{.Date}}` (`YYYY-MM-DD`) and `{{.Time}}` (RFC 3339), both of the due time in the schedule's time zone. A template error skips the run (logged as an error).
3. **The start**: `Manager.Start(ctx, schedule.User, Options{Topic, Agent: <agent name>})`, the same call as `POST /v1/gb/research`. The research goes on in the background; `fire` returns as soon as it is recorded. On success `lastRun` is set to the new run's ID and `scheduled run started` is logged with the `run_id`.

A failed start (logged as an error: `scheduled run could not be started`) does not stop the scheduler and is not retried: the entry is already moved on to its next time. Everything `Start` checks applies, so a scheduled run can also fail because, for example, the agent's cost caps or the per-user limit of working researches (`MaxActivePerUser`, shared with the runs of the same `user` that people start) is reached, or because the server is shutting down.

Each entry has its own mutex around `fire`, so the check and the start of one agent do not interleave.

## What it guarantees and what it does not

| | |
|---|---|
| Runs at the due minute | Yes, while the server is up and has an LLM. |
| At most one scheduled run of an agent at a time | Yes, per process: the check uses the run of the previous due time. |
| Looks at manual runs of the same agent | No. Only the scheduled run it started itself counts. |
| Catches up the times the server was down | No. After a start the next time after it is used. |
| Remembers `lastRun` across a restart | No (memory only). A run started before the restart is not known to it. |
| Retries a failed start | No. It waits for the next match. |
| Several servers on one database | Not coordinated. Every process that runs `RunSchedules` starts its own runs. Keep the `[schedule]` sections in one server (or `paused`). |
| Reload without restart | No. The schedules are read at startup, with the definitions. |

Only the server runs schedules. `gligabao_agent` ignores the section, and an agent used only as a sub-agent role is never started by the scheduler. The schedule is not part of `agent_hash`.

## Seeing it

- **Logs** (logrus, all start with `agent research:`): `scheduled agent` (at startup, with the first `next`), `scheduled run started` (with `run_id`), `scheduled run skipped, the previous one is still working`, `scheduled run skipped, the topic cannot be made`, `scheduled run could not be started`, and the warning when there is no LLM.
- **`GET /v1/gb/research/agents`**: the `schedule` of each agent, with `next_run`.
- **The runs**: ordinary researches of the schedule's `user`, with `agent_name` set (`GET /v1/gb/research`, `gligabao research ls`). There is no separate record of the schedule.

## Tests

`internal/agent/agent_defs/cron_test.go` (the parser, `Next`, never-matching expressions, parse errors, `MinGap`) and `internal/agent_research/agent_defs_test.go` (the scheduler through `export_test.go`: runs start when due and the topic template is filled, and a run is skipped while the previous one works).
