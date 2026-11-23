# Finish the GCNII* weights tracking: restart recovery, versions, LoRA in S3

**Date** 2026-11-23, **Version** 0.235.0, **Status** open

## Context

0.235.0 stores every finished GCNII* model in S3 (`model_weights/gcnii/<user>/<job id>/`, local directory without a bucket) and records it in
`gb_trained_models` (`model_type`, `user_id`, `storage`). See `docs/gcnii.md` ("Weights and the trained models table"), `internal/modeltrain/weights.go`
and `internal/api/model/weights.go`. Kept out of scope: sargo (Rust) was not changed, and the LoRA weights stay on the Modal volume.

## Remaining work

- [ ] A training whose instance restarts while it runs is `lost` (sargo trains in memory), also if sargo finished it between the last beat and the restart: the finished model stays in that instance's directory, unrecorded. A new instance could record a `done` sargo job that has a `lost` row of its own instance id.
- [ ] A watcher that cannot reach sargo does not beat, so a long sargo outage makes the job `lost` after 90 seconds (`beatStale`); make it configurable if trainings of big datasets need more.
- [ ] Sargo keeps one model per user: `/infer` and `/embed` always use the latest. Add `model_id` (a row) to them: restore that row's files into a per-model directory of sargo and load it from there (needs a change in `rust/sargo/src/gcnii/users.rs`).
- [ ] The row has no `hidden`/`depth` actually trained (0 = sargo's default); have sargo report them in the job.
- [ ] Deployment (`internal/training/deploy.go`) refuses `gcnii` rows (409); a deployer for them would fetch from the weights store instead of the ML sidecar.
- [ ] Optionally move the LoRA adapters to `model_weights/lora/<job id>/` (the store takes `[]byte`; adapters need a streaming `Put`).
- [ ] The PostgreSQL code of the new columns/query is untested (set `POSTGRES_TEST_DSN` and run `go test ./internal/training/db/...`).

## How to pick it up

`go test ./internal/api/model ./internal/modeltrain ./internal/training/...`. Terraform: `gloflow_emergence/ops/tf_main/gf_apps_core/gligabao_meta.tf` already allows `model_weights/*` (not applied/validated: no `terraform` on the dev machine).
