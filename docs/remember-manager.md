# Remember manager

`internal/memory/memory_remember/manager.go`

Every run of the [remember](remember.md) text pipeline is executed by **one goroutine, the remember manager**. The HTTP handlers (`POST /v1/gb/remember`, `POST /v1/gb/eval/remember`), and anything else that wants text remembered, do not call `memory_remember.Remember`: they send the manager a request. For now the manager **serializes** the runs: it takes the next request only when the previous run is over, so two runs never write at the same time.

Images (`image` in the request) are a different pipeline, have no run log, and are still remembered in the handler.

## Requests

```go
mgr := memory_remember.NewManager(ctx, db, llm)          // starts the goroutine; it runs until ctx is done

outcome := make(chan memory_remember.Outcome, 1)         // capacity at least 1; made by the sender
runID, err := mgr.Submit(memory_remember.Request{
    UserID: "u1", Dataset: "main", Kind: "knowledge", Text: text, Ref: ref,
    Opts:    opts,                                       // the options of Remember
    Ctx:     r.Context(),                                // cancels the run (nil: never)
    Outcome: outcome,                                    // optional
})
// runID is known here, before the run has started: the row in gb_remember_runs
o := <-outcome                                           // block until the run is over
// o.Result, o.Err: what Remember returned; o.OK() is o.Err == nil
```

- **`Submit` returns as soon as the request is queued**, with the **run ID**: the ID of the run's row in `gb_remember_runs` (the [run log](remember.md#run-log)). The row is created by `Submit`, so the ID exists before the run starts. `Manager.Status(ctx, runID)` reads the row later: its status, stages and error. It also fails at once, with nothing queued, for a request whose user, dataset or kind is invalid, or if the manager is closed (`ErrManagerClosed`).
- **`Outcome` is the completion channel**, optional. The sender makes it, with a capacity of at least 1, and the manager sends one `Outcome` to it when the run is over: the `Result` of `Remember` or its error. The handlers block on it, so for the client the call is still synchronous. The manager never blocks on it. Without it the request is fire and forget: pass a context that outlives the caller (`context.WithoutCancel`) and look at the run log.
- **`Ctx`** is the context of the run. A request whose context is done while it waits fails without running; a run in progress is cancelled with it (the handlers pass the request's context, so a client that leaves cancels its run).

## Queued runs

Between `Submit` and the start of the run the row is already in the table, with status `started` and every stage `not_started` (the schema has no status for a queued run; a stage is `skipped` only once the pipeline has passed it). When the run begins the row is overwritten with its parameters and start time. A run that never starts (cancelled in the queue, or the manager closing) is marked `failed` with the reason.

## The queue

The queue is two operations, `enqueue` and `dequeue`, which are the only places the manager touches it. By default it is a Go channel (256 runs; `Submit` blocks when it is full). An embedder with a queue system of its own can supply the operations as callbacks:

```go
mgr := memory_remember.NewManager(ctx, db, llm, memory_remember.WithQueue(memory_remember.QueueHooks{
    Enqueue: func(ctx context.Context, runID int64) error { /* push runID to the external queue */ },
    Dequeue: func(ctx context.Context) (int64, error)     { /* block until there is one; return when ctx is done */ },
}))
```

- The queue carries **only the run ID**. The request itself (its context, options and channels cannot be serialized) stays in the memory of the process, so the process that enqueues an ID must be the one that dequeues it; the external queue gives the queueing, persistence and ordering. A dequeued ID that is not a pending request of the process (left over from an earlier one) is logged and skipped.
- `Enqueue` may block, but must return when its context is done (the request's or the manager's); if it fails, `Submit` fails with its error and the run is marked `failed`.
- `Dequeue` must block until a run is available, and return when its context is done. An error while the manager is running is logged, and it is tried again after a second.

## Shutdown

When the context of the manager is done, the run in progress is cancelled, every request that is still waiting fails with `ErrManagerClosed` (its run is marked `failed` and its `Outcome` is sent), and `Submit` returns `ErrManagerClosed`. `Manager.Wait()` blocks until the goroutine has returned. The server stops the manager after the graceful HTTP shutdown, so requests in progress get their time first.
