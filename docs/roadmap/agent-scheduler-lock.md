# Note: a lock for the agent scheduler

**Problem.** The [agent scheduler](../agents/agent-scheduler.md) has no lock. Every server process that calls `Manager.RunSchedules()` keeps its own in-memory `next` and `lastRun`. With several servers on one database (a distributed setup), each server fires every due time, so one scheduled agent starts **one run per server** (two servers, two runs of the same agent for the same user and topic). The "skip if the previous run still works" check does not help: each server only knows the runs it started itself.

**Today's workaround.** Keep the `[schedule]` sections on one server only, or mark them `paused` on the others.

**Fix to build.** Make one server win each due time, using the database (Postgres):

- Either a **session advisory lock** (`pg_try_advisory_lock`) held by the one process that runs the scheduler loop; the others try again every tick, so another one takes over if the holder dies (the repo already uses `pg_advisory_lock` for the schema, `internal/db/graph/postgres/db_schema.go`).
- Or a **per-fire claim**: a small table (agent name, due time) with a unique key, and `INSERT ... ON CONFLICT DO NOTHING` before `Start`; only the server whose insert succeeds starts the run. This also records the last due time per agent, so it can fix the other gaps: catching up times missed while the server was down, and the "previous run still working" check across restarts and servers.

The per-fire claim is the more complete one; the advisory lock is the smaller change. Open points: the lock for SQLite (single process, so none is needed), and the table's name and clean-up.
