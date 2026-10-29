# Instructions for coding agents / LLMs (`internal/memory/`)

## Mark every use of the memory pipeline (`// MEMORY PIPELINE`)

Every use of the concepts and interfaces of `memory_pipeline/` (`Run`, `Names`, `Step`, `Stage`, `Each`, `Policy`/`Advisory`,
`Stop`, `Observer` and the code that implements it, the inference sink that the runner sets for each stage, ...) anywhere in
`gligabao/` (outside `memory_pipeline/` itself) must be wrapped in a marked block, so a human reader can find the uses at a glance.

The block starts with `//------------------`, and right below it `// MEMORY PIPELINE`; it ends with `//------------------`:

```go
	//------------------
	// MEMORY PIPELINE
	if err := memory_pipeline.Run(ctx, rememberPipeline, j, run); err != nil {
		return nil, err
	}
	//------------------
```

Rules:

- Nothing else goes in the markers: exactly these two lines at the start, exactly one at the end. Indent them like the code.
- The block is the smallest piece of code that is the use: a call, a pipeline definition (`var queryPipeline = ...`, with its doc
  comment), a method that implements `Observer`, a `return memory_pipeline.Stop`.
- Top-level declarations with a doc comment: leave a blank line below `// MEMORY PIPELINE`, so Go does not read the marker as part
  of the doc comment (`gofmt` also puts a blank line before the closing marker, which is fine).
- Neighbouring uses may share one block (e.g. `Start`, `Skip` and `Done` of a run log).
- Not marked: the `import` line, and mentions in comments or docs.
- Applies to tests too, and to every new or changed use: when you add code that uses the memory pipeline, mark it in the same change.
  When you move or delete such code, move or delete its markers.
- To find all uses: `rg -n "MEMORY PIPELINE" gligabao/`.

## Mark every use of the memory dataset (`// MEMORY DATASET`)

Same rule, same format, for `memory_dataset/` (`CreateDataset`, `ListDatasets`, `DeleteDataset`, `Dataset`, `ErrDatasetExists`,
`VectorIndex` and its constants, `VectorIndexes`, `VectorDataset`, `EntityDataset`, `EdgeDataset`, `ChunkDataset`,
`SummaryDataset`, ...): every use anywhere in `gligabao/` (outside `memory_dataset/` itself) is wrapped in a block that starts with
`//------------------` and `// MEMORY DATASET`, and ends with `//------------------`:

```go
	//------------------
	// MEMORY DATASET
	ns := memory_dataset.EntityDataset(dsID)
	//------------------
```

All the details of the memory pipeline rule above apply (smallest piece of code that is the use, indent like the code, blank line
below the marker before a doc comment, neighbouring uses may share a block, `import` lines and mentions in comments or docs are not
marked, tests included, mark new uses in the same change, move or delete the markers with the code). Two more cases:

- A use in the header of `if`, `for`, `switch`: the block is the whole statement, including its body.
- A use in a struct field: the block is that field.

If one piece of code uses both the pipeline and the dataset, nest the blocks (the dataset block inside the pipeline one, or the other way round).

To find all uses: `rg -n "MEMORY DATASET" gligabao/`.
