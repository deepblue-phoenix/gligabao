# Go structs

> **Note:** keep this file up to date. Whenever a Go struct is added, renamed or removed (in any non-test `.go` file, including unexported and function-local structs), update the list here.

## Structs

>>> ROOT

| Struct                                                    | Description                                                                                                            |
| --------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `gligabao.App`                                            | describes the embedding program so the server can report its version alongside gligabao's.                             |
| `gligabao.ServeOptions`                                   | holds the serve settings that are not part of the Config file, such as paths and embedder-supplied providers.          |
| `monitoring.Hooks`                                        | holds the optional callbacks an embedding program supplies to observe system events.                                   |

>>> AI

| Struct                                                    | Description                                                                                                            |
| --------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `internal/ai.Client`                                      | calls the Anthropic LLM API with the configured key, model and price table.                                            |
| `internal/ai.EmbedUsage`                                  | records the token usage and cost of an embeddings request.                                                             |
| `internal/ai.Embedder`                                    | computes text embeddings using the OpenAI embeddings API.                                                              |
| `internal/ai.Image`                                       | is an image (media type and data) attached to a Message.                                                               |
| `internal/ai.Message`                                     | is a single conversation turn with a role, content and optional images.                                                |
| `internal/ai.ModelInfo`                                   | describes a model (name, provider, kind) that gligabao actively supports.                                              |
| `internal/ai.Price`                                       | holds a model's price in USD per 1M tokens, including cache and long-context rates.                                    |
| `internal/ai.PriceTable`                                  | looks up model prices by the longest key that prefixes a response model name.                                          |
| `internal/ai.Request`                                     | describes a single LLM call, with zero values falling back to client defaults.                                         |
| `internal/ai.Response`                                    | is the result of an LLM call with its text, model, token usage and cost.                                               |
| `internal/ai.Usage`                                       | is the token usage (input, output, cache write and cache read) of one call.                                            |
| `internal/ai.WebSearchRequest`                            | asks the model to research a prompt using the web search tool.                                                         |
| `internal/ai.WebSearchResponse`                           | is the result of a web search with its text, queries, sources and usage.                                               |
| `internal/ai.WebSource`                                   | is a web page that backs the answer of a web search.                                                                   |
| `internal/ai.apiError`                                    | decodes the error object of an Anthropic API response.                                                                 |
| `internal/ai.apiRequest`                                  | is the JSON request body sent to the Anthropic messages API.                                                           |
| `internal/ai.apiResponse`                                 | is the JSON response body decoded from the Anthropic messages API.                                                     |
| `internal/ai.contentBlock`                                | is one content block (text or image) of an Anthropic API message.                                                      |
| `internal/ai.embedRequest`                                | is the JSON request body sent to the OpenAI embeddings API.                                                            |
| `internal/ai.embedResponse`                               | is the JSON response body decoded from the OpenAI embeddings API.                                                      |
| `internal/ai.imageSource`                                 | is the base64 image source of an image content block.                                                                  |
| `internal/ai.openAIError`                                 | decodes the error object of an OpenAI API response.                                                                    |
| `internal/ai.thinking`                                    | is the extended-thinking setting (type and token budget) of an API request.                                            |
| `internal/ai.webSearchAPIResponse`                        | is the JSON response body of a web search call to the Anthropic API.                                                   |
| `internal/ai.webSearchBlock`                              | reads the parts of a web search content block whatever its type.                                                       |
| `internal/ai/atif.Agent`                                  | describes the agent that produced an ATIF trajectory.                                                                  |
| `internal/ai/atif.Builder`                                | records an ATIF trajectory step by step and is safe for concurrent use.                                                |
| `internal/ai/atif.FinalMetrics`                           | holds the token, cost and step totals over a whole trajectory.                                                         |
| `internal/ai/atif.Metrics`                                | holds the LLM usage of one agent step.                                                                                 |
| `internal/ai/atif.Observation`                            | holds the results of a step's tool calls.                                                                              |
| `internal/ai/atif.ObservationResult`                      | is the result of one tool call, linked to its call ID.                                                                 |
| `internal/ai/atif.Step`                                   | is one turn of the trajectory with its message, tool calls, observation and metrics.                                   |
| `internal/ai/atif.ToolCall`                               | is a function the agent invoked, with its arguments.                                                                   |
| `internal/ai/atif.Trajectory`                             | is a complete ATIF document holding the agent, its steps and the final metrics.                                        |

>>> API

| Struct                                                    | Description                                                                                                            |
| --------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `internal/api/admin.Counts`                               | is the number of artifacts, entities and edges.                                                                        |
| `internal/api/admin.DatasetWebSummary`                    | counts what the web searches of one dataset hold.                                                                      |
| `internal/api/admin.EvalTotals`                           | holds the case counts over all experiments.                                                                            |
| `internal/api/admin.InferenceBreakdown`                   | holds the inference totals sharing one value of a dimension such as stage or model.                                    |
| `internal/api/admin.InferenceSums`                        | holds the totals (count, tokens, cost) of a set of inferences.                                                         |
| `internal/api/admin.Latency`                              | summarizes the durations of web search calls (average, median and maximum).                                            |
| `internal/api/admin.Named`                                | is a database shown in the admin UI under a name such as "standard" or "eval".                                         |
| `internal/api/admin.RememberRunCounts`                    | counts remember runs overall and per status.                                                                           |
| `internal/api/admin.WebEdge`                              | is a fact that a web search result yielded.                                                                            |
| `internal/api/admin.WebEntity`                            | is an entity that a web search result yielded.                                                                         |
| `internal/api/admin.WebSearch`                            | is one web search made by remember for a key entity of a text.                                                         |
| `internal/api/admin.WebSearchTotals`                      | holds the totals over web searches read from trajectories.                                                             |
| `internal/api/admin.caseHistory`                          | is the progression of one eval case over the runs of an experiment.                                                    |
| `internal/api/admin.casePoint`                            | is one plotted result of a case in its history, one dot per run.                                                       |
| `internal/api/admin.caseRunWebSearches`                   | holds the web searches of one case result of a run.                                                                    |
| `internal/api/admin.datasetView`                          | is a dataset with its counts as listed in the admin UI.                                                                |
| `internal/api/admin.evalOverview`                         | is the admin UI's eval overview with totals and experiments.                                                           |
| `internal/api/admin.experimentDetail`                     | is an experiment with its runs for the admin detail view.                                                              |
| `internal/api/admin.experimentView`                       | is an experiment with its counts as listed in the admin UI.                                                            |
| `internal/api/admin.imageDisk`                            | describes the image directory next to what the database tracks.                                                        |
| `internal/api/admin.imagesView`                           | is the admin UI's image statistics together with the disk state.                                                       |
| `internal/api/admin.inferencesResponse`                   | is the admin API response listing inferences with breakdowns and pagination.                                           |
| `internal/api/admin.lookup`                               | holds the entities, edges and artifacts that the anchors of one dataset point to.                                      |
| `internal/api/admin.overview`                             | is the admin UI's overview of datasets and totals.                                                                     |
| `internal/api/admin.rememberRunsResponse`                 | is the admin API response listing remember runs with counts and pagination.                                            |
| `internal/api/admin.tsAnchor`                             | is one anchor of a series with a label for its target.                                                                 |
| `internal/api/admin.tsEntity`                             | is an entity a series belongs to and how it is related.                                                                |
| `internal/api/admin.tsSeries`                             | is a series with its metadata, anchors and entities as shown in the admin UI.                                          |
| `internal/api/admin.webDataset`                           | is a dataset with web searches as listed in the web search stats.                                                      |
| `internal/api/admin.webSearchStats`                       | is the answer of the web search statistics endpoint.                                                                   |
| `internal/api/datasets.createRequest`                     | is the request body for creating a dataset.                                                                            |
| `internal/api/ontologies.loadRequest`                     | is the request body for loading an ontology.                                                                           |
| `internal/api/recall.recallRequest`                       | is the request body for a recall call.                                                                                 |
| `internal/api/remember.fetchRequest`                      | is the timeseries fetch part of a remember request.                                                                    |
| `internal/api/remember.rememberRequest`                   | is the request body for a remember call.                                                                               |
| `internal/api/remember.timeseriesRequest`                 | is the timeseries part of a remember request.                                                                          |
| `internal/api/timeseries.anchorView`                      | is the API view of a series anchor.                                                                                    |
| `internal/api/timeseries.attachRequest`                   | is the request body for attaching a series to a dataset.                                                               |
| `internal/api/timeseries.seriesView`                      | is the API view of a series and its point count.                                                                       |
| `internal/api/training.EvalManager`                       | starts trained-model evals in the background, one at a time per model.                                                 |
| `internal/api/training.runManager`                        | provides the shared endpoints for the runs of one kind (an eval).                                                |

>>> CHUNK

| Struct                                                    | Description                                                                                                            |
| --------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `internal/chunk.Chunk`                                    | is one piece of a text with its token count and offsets.                                                               |
| `internal/chunk.Config`                                   | sets how a text is chunked (size, overlap and tokenizer).                                                              |
| `internal/chunk.Estimator`                                | is the default pure-Go tokenizer that estimates BPE token counts.                                                      |
| `internal/chunk.span`                                     | is a byte range of the text.                                                                                           |
| `internal/chunk.splitter`                                 | holds the state while splitting a text into chunks.                                                                    |
| `internal/chunk.unit`                                     | is a trimmed span of text that fits within one chunk.                                                                  |

>>> DB

| Struct                                                    | Description                                                                                                            |
| --------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `internal/db.Config`                                      | selects and locates the databases to open.                                                                             |
| `internal/db.DB`                                          | holds the open relational, graph and vector clients and the shared settings.                                           |
| `internal/db/graph/model.Artifact`                        | is a piece of ingested data (text plus timing) that entities and edges are anchored to.                                |
| `internal/db/graph/model.Counts`                          | is the number of rows of each kind a dataset holds.                                                                    |
| `internal/db/graph/model.Edge`                            | is a fact relating two entities, with its bi-temporal validity.                                                        |
| `internal/db/graph/model.EdgeArtifact`                    | links an edge to an artifact it was inferred from.                                                                     |
| `internal/db/graph/model.EdgeSession`                     | links an edge to a session it was inferred from.                                                                       |
| `internal/db/graph/model.Entity`                          | is a thing mentioned in the text such as a person, place or concept.                                                   |
| `internal/db/graph/model.EntityArtifact`                  | links an entity to an artifact that mentions it.                                                                       |
| `internal/db/graph/model.EntitySession`                   | links an entity to a session in which it was mentioned.                                                                |
| `internal/db/graph/model.EvidenceEdge`                    | is a graph edge cited by an answer.                                                                                    |
| `internal/db/graph/model.EvidenceEntity`                  | is a graph entity cited by an answer.                                                                                  |
| `internal/db/graph/model.ImageArtifact`                   | is a stored image's metadata and the location of its file.                                                             |
| `internal/db/graph/model.ImageExtraction`                 | is a stored LLM extraction of an image with its text blocks.                                                           |
| `internal/db/graph/model.ImageGroup`                      | counts images or extractions sharing a media type or theme, with their total size.                                     |
| `internal/db/graph/model.ImageStats`                      | summarizes all image artifacts and extractions of a database.                                                          |
| `internal/db/graph/model.ImageTextBlock`                  | is one region of text found in an image.                                                                               |
| `internal/db/graph/model.NewChunkArtifact`                | is a chunk artifact to insert as a child of its parent artifact.                                                       |
| `internal/db/graph/model.NewChunkSummaryArtifact`         | is a chunk summary artifact to insert as a child of its chunk.                                                         |
| `internal/db/graph/model.NewEdge`                         | is an edge to insert between two entity IDs.                                                                           |
| `internal/db/graph/model.NewImage`                        | is an image artifact with its first extraction, stored in one transaction.                                             |
| `internal/db/graph/model.NewImageExtraction`              | is what an LLM read from an image, ready to store.                                                                     |
| `internal/db/graph/model.NewSeries`                       | is a series to insert or whose metadata to update.                                                                     |
| `internal/db/graph/model.NewSeriesAnchor`                 | links a series to an artifact, entity or edge.                                                                         |
| `internal/db/graph/model.NewSeriesObservationArtifact`    | is a series observation artifact to insert under the artifact that anchors the series.                                 |
| `internal/db/graph/model.NewWebSearchArtifact`            | is a web search result artifact to insert under the artifact the searched entity came from.                            |
| `internal/db/graph/model.OntologyClass`                   | is a class of an ontology, a node of the ontology graph.                                                               |
| `internal/db/graph/model.OntologyCounts`                  | is the size (classes, properties, edges) of a stored ontology.                                                         |
| `internal/db/graph/model.OntologyEdge`                    | is a relationship between two ontology classes referenced by IRI.                                                      |
| `internal/db/graph/model.Series`                          | is a stored series without its points.                                                                                 |
| `internal/db/graph/model.SeriesAnchor`                    | is a stored link between a series and its target.                                                                      |
| `internal/db/graph/model.SeriesPoint`                     | is one value of a series at a time.                                                                                    |
| `internal/db/graph/postgres.Tx`                           | is a PostgreSQL graph transaction implementing model.Tx.                                                               |
| `internal/db/graph/sqlite.Tx`                             | is a SQLite graph transaction implementing model.Tx.                                                                   |
| `internal/db/relational/model.Dataset`                    | describes a dataset.                                                                                                   |
| `internal/db/relational/model.Inference`                  | is one recorded invocation of a model (LLM call, embedding, web search or Jev classification) with its usage and cost. |
| `internal/db/relational/model.InferenceFilter`            | selects inferences by type, stage, model and reasoning level.                                                          |
| `internal/db/relational/model.InferenceGroup`             | sums the inferences sharing a type, stage, model and thinking level.                                                   |
| `internal/db/relational/model.InferenceStat`              | sums the recorded inferences of one type.                                                                              |
| `internal/db/relational/model.Model`                      | is an AI model record that graph entities and edges reference.                                                         |
| `internal/db/relational/model.Ontology`                   | is the metadata of a loaded ontology.                                                                                  |
| `internal/db/relational/model.RememberRun`                | is the record of one execution of the remember pipeline, stage by stage.                                               |
| `internal/db/relational/model.RememberRunFilter`          | selects remember runs by user, dataset and status.                                                                     |
| `internal/db/relational/model.RememberStage`              | records how one stage of a remember run went.                                                                          |
| `internal/db/relational/model.RememberStageDef`           | describes a stage of the remember pipeline.                                                                            |
| `internal/db/relational/postgres.rememberCol`             | describes a column of the remember runs table and whether it is jsonb.                                                 |
| `internal/db/relational/postgres.stageRow`                | holds the columns of one remember stage as scanned from PostgreSQL.                                                    |
| `internal/db/relational/sqlite.stageRow`                  | holds the columns of one remember stage as scanned from SQLite.                                                        |
| `internal/db/vector.Chromem`                              | is a vector Store backed by the pure-Go chromem-go database.                                                           |
| `internal/db/vector.Match`                                | is a similarity search result.                                                                                         |
| `internal/db/vector.Pinecone`                             | is a vector Store backed by a Pinecone index via its REST API.                                                         |
| `internal/db/vector.QueryOptions`                         | tunes a similarity search (top-k, filter, values).                                                                     |
| `internal/db/vector.Stats`                                | describes the vectors of one dataset.                                                                                  |
| `internal/db/vector.Vector`                               | is an embedding with an ID and optional metadata.                                                                      |
| `internal/db/vector.pcDeleteRequest`                      | is the Pinecone delete request body.                                                                                   |
| `internal/db/vector.pcQueryRequest`                       | is the Pinecone query request body.                                                                                    |
| `internal/db/vector.pcQueryResponse`                      | is the Pinecone query response body.                                                                                   |
| `internal/db/vector.pcUpsertRequest`                      | is the Pinecone upsert request body.                                                                                   |
| `internal/db/vector.pcVector`                             | is a vector as sent to or read from Pinecone.                                                                          |
| `internal/db/vector.pineconeError`                        | decodes the error body of a Pinecone response.                                                                         |

>>> EVALS

| Struct                                                    | Description                                                                                                            |
| --------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `internal/evals/db/model.CaseRef`                         | identifies an eval case by its eval and case name.                                                                     |
| `internal/evals/db/model.Experiment`                      | is a named series of eval runs with the cases they executed.                                                           |
| `internal/evals/db/model.ExperimentArtifact`              | is an artifact of the dataset a case ran in.                                                                           |
| `internal/evals/db/model.ExperimentCaseRun`               | is the result of one case within an experiment run.                                                                    |
| `internal/evals/db/model.ExperimentRun`                   | is one execution of an experiment with its case counts.                                                                |
| `internal/evals/db/model.ExperimentTrajectory`            | references the ATIF trajectory of one remember or recall call of a case.                                               |
| `internal/evals/db/model.TrainedModelCase`                | is the outcome for one case of a trained-model run, with the judge's score.                                            |
| `internal/evals/db/model.TrainedModelRun`                 | is one run of a trained model against a set of cases, judged by an external LLM.                                       |
| `internal/evals/db/model.TrainingExperiment`              | is a named series of eval runs of trained models.                                                                      |
| `internal/evals/db/model.TrainingExperimentModel`         | summarizes the trained model evaluated by a run so runs can be compared by recipe.                                     |
| `internal/evals/db/model.TrainingExperimentRun`           | is one eval run of a trained model within a training experiment.                                                       |
| `internal/evals/experiments.CaseOutcome`                  | is what one experiment case produced.                                                                                  |
| `internal/evals/experiments.Options`                      | describes the experiment and run to start.                                                                             |
| `internal/evals/experiments.Tracker`                      | records one run of an experiment, case by case.                                                                        |
| `internal/evals/knowledge.Artifact`                       | is one text to remember in a knowledge eval case, with its reference time.                                             |
| `internal/evals/knowledge.Case`                           | is one knowledge eval case: texts to remember, then a question and the checks.                                         |
| `internal/evals/knowledge.ExpectEdge`                     | is a temporal fact that must be stored between two entities.                                                           |
| `internal/evals/knowledge.ExpectSeries`                   | is a stored series that a case checks for.                                                                             |
| `internal/evals/knowledge.Outcome`                        | is what one knowledge eval case produced.                                                                              |
| `internal/evals/knowledge.rememberedChunk`                | is a chunk as reported by the remember response.                                                                       |
| `internal/evals/knowledge.verboseLLM`                     | wraps an LLM and prints the input and output of every call.                                                            |
| `internal/evals/training.Case`                            | is one trained-model eval case with a question and its expected answer.                                                |
| `internal/evals/training.CaseResult`                      | is the outcome of one example in a run.                                                                                |
| `internal/evals/training.Config`                          | says how a trained-model eval run goes.                                                                                |
| `internal/evals/training.Eval`                            | is a named set of cases loaded from one eval file.                                                                     |
| `internal/evals/training.Example`                         | is one training dataset record or eval case input and expected output.                                                 |
| `internal/evals/training.Generation`                      | is what the trained model answered to a prompt.                                                                        |
| `internal/evals/training.Progress`                        | tracks how far an eval run is, with counts of judged outcomes.                                                         |
| `internal/evals/training.evalFile`                        | is the JSON content of an eval file.                                                                                   |
| `internal/evals/training.judgeCase`                       | is one case as presented to the judge.                                                                                 |
| `internal/evals/training.verdict`                         | is the judge's score and reason for one case.                                                                          |

>>> MEMORY

| Struct                                                    | Description                                                                                                            |
| --------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `internal/memory.DatasetVectorStats`                      | is the state of all the vector indexes of a dataset.                                                                   |
| `internal/memory.GraphExport`                             | is a whole dataset graph with its entities and edges.                                                                  |
| `internal/memory.IndexStats`                              | is the state of one vector index of a dataset.                                                                         |
| `internal/memory.preparedOntology`                        | is a parsed ontology ready to be stored.                                                                               |
| `internal/memory/memory_core.CallOptions`                 | holds the settings of one memory call, from which each pipeline reads what it uses.                                    |
| `internal/memory/memory_core.Fact`                        | is the memory layer's view of an edge: a statement with the time it held and its source.                               |
| `internal/memory/memory_core.FetchEntity`                 | is the entity a series is fetched for.                                                                                 |
| `internal/memory/memory_core.SeriesAnchor`                | says what a caller-supplied series is anchored to.                                                                     |
| `internal/memory/memory_core.SeriesData`                  | is a series returned by a provider.                                                                                    |
| `internal/memory/memory_core.SeriesEdge`                  | selects a fact by its endpoint names and relation.                                                                     |
| `internal/memory/memory_core.SeriesEntity`                | selects an entity by name and optionally type.                                                                         |
| `internal/memory/memory_core.SeriesFetch`                 | asks Remember to fetch series from registered providers for the entities of a text.                                    |
| `internal/memory/memory_core.SeriesFetchRequest`          | is what a provider's fetch callback is asked for.                                                                      |
| `internal/memory/memory_core.SeriesPoint`                 | is one value at a time, serialized as a [timestamp, value] pair.                                                       |
| `internal/memory/memory_core.SeriesProvider`              | is a registered source of timeseries.                                                                                  |
| `internal/memory/memory_core.SeriesSpec`                  | is a timeseries the caller supplies with its points and anchors.                                                       |
| `internal/memory/memory_core.ToolTiming`                  | records the model, timing and tokens of a traced step.                                                                 |
| `internal/memory/memory_core.Trace`                       | records one Remember, Recall or image call as an ATIF trajectory.                                                      |
| `internal/memory/memory_core.modelLLM`                    | wraps an LLM to use a stage's model and reasoning level.                                                               |
| `internal/memory/memory_core.recordingLLM`                | wraps an LLM and stores an inference record for every successful call.                                                 |
| `internal/memory/memory_core.stageKey`                    | is the context key under which the current pipeline stage is stored.                                                   |
| `internal/memory/memory_core.tracingLLM`                  | wraps an LLM and records each call's input and output in the trace.                                                    |
| `internal/memory/memory_image.ImageExtraction`            | is what the LLM read from an image.                                                                                    |
| `internal/memory/memory_image.ImageResult`                | summarizes what RememberImage stored.                                                                                  |
| `internal/memory/memory_ontology.Class`                   | is a named class of the ontology.                                                                                      |
| `internal/memory/memory_ontology.Edge`                    | is a relationship between two ontology classes.                                                                        |
| `internal/memory/memory_ontology.Ontology`                | is the result of parsing an ontology document.                                                                         |
| `internal/memory/memory_ontology.edgeKey`                 | is the key used to deduplicate ontology edges.                                                                         |
| `internal/memory/memory_ontology.graphIndex`              | is an ordered, de-duplicated index of graph values built during parsing.                                               |
| `internal/memory/memory_ontology.rdfParser`               | decodes RDF/XML into triples.                                                                                          |
| `internal/memory/memory_ontology.term`                    | is the object of an RDF triple: an IRI, blank node or literal.                                                         |
| `internal/memory/memory_ontology.triple`                  | is an RDF subject-predicate-object statement.                                                                          |
| `internal/memory/memory_recall.Evidence`                  | lists the entities and edges an answer is based on.                                                                    |
| `internal/memory/memory_recall.RecallResult`              | is the answer to a query together with its supporting evidence.                                                        |
| `internal/memory/memory_recall.graphContext`              | is the subgraph handed to the recall LLM.                                                                              |
| `internal/memory/memory_recall.recallContext`             | is everything the recall LLM is given: the memory, the current time and the question.                                  |
| `internal/memory/memory_recall.recallOutput`              | is the structured answer the recall LLM returns.                                                                       |
| `internal/memory/memory_remember.ChunkResult`             | is the result for one chunk of a chunked text, itself an artifact.                                                     |
| `internal/memory/memory_remember.Contradiction`           | says that an existing edge cannot hold together with a new edge.                                                       |
| `internal/memory/memory_remember.EdgePlan`                | is the outcome of comparing new edges against existing ones.                                                           |
| `internal/memory/memory_remember.Extraction`              | is the structured entities and edges extracted from a block of text.                                                   |
| `internal/memory/memory_remember.Manager`                 | is the goroutine that runs queued remember pipeline requests.                                                          |
| `internal/memory/memory_remember.Outcome`                 | is what a run of Remember returned, a result or an error.                                                              |
| `internal/memory/memory_remember.QueueHooks`              | holds the enqueue and dequeue callbacks of the manager's queue.                                                        |
| `internal/memory/memory_remember.Request`                 | asks the manager for one run of Remember.                                                                              |
| `internal/memory/memory_remember.Result`                  | summarizes what was stored by Remember.                                                                                |
| `internal/memory/memory_remember.SeriesFetchResult`       | is the outcome of fetching series from one provider for one entity.                                                    |
| `internal/memory/memory_remember.SeriesObservationResult` | is the observation made of one series in a Remember call.                                                              |
| `internal/memory/memory_remember.SummaryResult`           | is a stored chunk summary, an artifact of its own.                                                                     |
| `internal/memory/memory_remember.WebSearchResult`         | is the web search made for one key entity of a Remember call.                                                          |
| `internal/memory/memory_remember.candidates`              | holds the stored facts a Remember call compares new facts with, and how they were found.                               |
| `internal/memory/memory_remember.chunkPart`               | says that a stored text is a chunk of a longer one.                                                                    |
| `internal/memory/memory_remember.chunkSummary`            | is the summary of a chunk before it is stored.                                                                         |
| `internal/memory/memory_remember.chunkVector`             | is a chunk whose text is embedded together with entities and edges.                                                    |
| `internal/memory/memory_remember.job`                     | holds what one Remember call shares between the steps it runs on each text.                                            |
| `internal/memory/memory_remember.keyEntity`               | is an entity of the call that may be searched for on the web.                                                          |
| `internal/memory/memory_remember.merger`                  | accumulates the results of a text's chunks into the text's result.                                                     |
| `internal/memory/memory_remember.observable`              | is a stored series that an observation may be made of.                                                                 |
| `internal/memory/memory_remember.observationPart`         | says that a stored text is the observation of a series.                                                                |
| `internal/memory/memory_remember.pair`                    | pairs a new edge with the existing edge it is compared to.                                                             |
| `internal/memory/memory_remember.pendingVectors`          | holds embeddings computed before a transaction commits and written after it.                                           |
| `internal/memory/memory_remember.queued`                  | is a queued remember run identified by its run ID and context.                                                         |
| `internal/memory/memory_remember.runLog`                  | is the log of one Remember call.                                                                                       |
| `internal/memory/memory_remember.runState`                | is the state shared by the views of a run log.                                                                         |
| `internal/memory/memory_remember.seriesAssignment`        | is the LLM's decision to fetch from a provider for an entity.                                                          |
| `internal/memory/memory_remember.seriesFetched`           | is the outcome of one series fetch callback.                                                                           |
| `internal/memory/memory_remember.similarEntity`           | is a stored entity found by similarity for an endpoint of a new fact.                                                  |
| `internal/memory/memory_remember.summaryVector`           | is a chunk summary whose text is embedded next to its chunk's.                                                         |
| `internal/memory/memory_remember.webPart`                 | says that a stored text is the result of a web search.                                                                 |
| `internal/memory/memory_remember.webSearched`             | is the outcome of the search tool call for one entity.                                                                 |
| `internal/memory/memory_timeseries.AnchorResult`          | is one stored series anchor.                                                                                           |
| `internal/memory/memory_timeseries.Move`                  | is a change between two consecutive points of a series.                                                                |
| `internal/memory/memory_timeseries.Observed`              | describes the series an observation is about.                                                                          |
| `internal/memory/memory_timeseries.Result`                | is one stored series.                                                                                                  |
| `internal/memory/memory_timeseries.Stats`                 | summarizes the points of a series, computed in Go and stored as JSON.                                                  |

>>> SYS

| Struct                                                    | Description                                                                                                            |
| --------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `internal/sys/buildinfo.Component`                        | is the version information of one program or library.                                                                  |
| `internal/sys/config.Config`                              | holds the application configuration, from the TOML file and environment.                                               |
| `internal/sys/config.Price`                               | is a model's price in USD per 1M tokens as set in the configuration.                                                   |
| `internal/sys/jev.Answer`                                 | is the answer to one Jev question, with fields depending on its type.                                                  |
| `internal/sys/jev.Call`                                   | is the timing of one Jev call.                                                                                         |
| `internal/sys/jev.Classification`                         | is Jev's judgment of a text.                                                                                           |
| `internal/sys/jev.Client`                                 | calls the Jev API.                                                                                                     |
| `internal/sys/jev.Question`                               | is a typed question about the state sent to Jev.                                                                       |
| `internal/sys/jev.Response`                               | is the result of a Jev evaluation.                                                                                     |
| `internal/sys/server.Options`                             | configures the HTTP server.                                                                                            |
| `internal/sys/sidecar.Client`                             | calls the training sidecar's HTTP API.                                                                                 |
| `internal/sys/sidecar.HealthResponse`                     | is the body of the sidecar's health endpoint.                                                                          |
| `internal/sys/sidecar.InferError`                         | is the error of a generation that failed on the sidecar.                                                               |
| `internal/sys/sidecar.InferRequest`                       | is the body of the sidecar's infer request.                                                                            |
| `internal/sys/sidecar.InferResponse`                      | is the sidecar's answer to an infer request.                                                                           |
| `internal/sys/sidecar.StartTrainingJobRequest`            | is the body of the sidecar's training job start request.                                                               |
| `internal/sys/sidecar.StatusError`                        | is the error for a sidecar response with a non-200 status.                                                             |
| `internal/sys/sidecar.TrainedModelFile`                   | is one file of a trained model's weights.                                                                              |
| `internal/sys/sidecar.TrainedModelInfo`                   | describes the weights a completed training job saved.                                                                  |
| `internal/sys/sidecar.TrainingEvent`                      | is one status or progress event of a training job.                                                                     |
| `internal/sys/sidecar.TrainingJob`                        | is a training job started on the sidecar.                                                                              |

>>> TRAINING

| Struct                                                    | Description                                                                                                            |
| --------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `internal/training.DatasetInfo`                           | is a training dataset as the API shows it, combining its record and manifest.                                          |
| `internal/training.DatasetProblem`                        | is a dataset subdirectory that has a manifest but is not usable.                                                       |
| `internal/training.Error`                                 | is a failure of a training operation with its kind.                                                                    |
| `internal/training.FeedEvent`                             | is a sidecar event tagged with its run and job as the admin UI receives it.                                            |
| `internal/training.FileStats`                             | describes a data file's size, hash and record count.                                                                   |
| `internal/training.InferRequest`                          | is a prompt for a trained model.                                                                                       |
| `internal/training.InferResult`                           | is a trained model's answer and the ID of the stored request.                                                          |
| `internal/training.LocalDataset`                          | is a dataset found in the training datasets directory.                                                                 |
| `internal/training.Manifest`                              | is the content of a dataset.toml manifest.                                                                             |
| `internal/training.Monitor`                               | follows training jobs and fans their events out to subscribers.                                                        |
| `internal/training.Preview`                               | is the beginning of a dataset's data file.                                                                             |
| `internal/training.RunEvent`                              | is one event of the feed of an eval run.                                                                   |
| `internal/training.RunManager`                            | starts runs of one kind in the background, one at a time per trained model.                                            |
| `internal/training.RunOps`                                | holds the storage functions that store and read the runs (of the evals).                                                   |
| `internal/training.RunOptions`                            | holds the fields of the request body that starts a run.                                                                |
| `internal/training.RunParams`                             | holds the settings a run was started with.                                                                             |
| `internal/training.RunSpec`                               | is a run ready to start with its model, config and examples.                                                           |
| `internal/training.RunStream`                             | is the live feed of one running run, with its watchers and pending answers.                                            |
| `internal/training.RunWatch`                              | is a run and its cases so far, plus a live event feed.                                                                 |
| `internal/training.StartJobRequest`                       | is what the caller of StartJob chooses for a training job.                                                             |
| `internal/training.Subscription`                          | is a subscriber to the monitor's events, delivered on a channel.                                                       |
| `internal/training.runningRun`                            | tracks a running run's ID and cancel function.                                                                         |
| `internal/training/db/model.TrainedModel`                 | is the record of the weights a completed training job produced.                                                        |
| `internal/training/db/model.TrainedModelFile`             | is one file of a trained model's weights.                                                                              |
| `internal/training/db/model.TrainedModelInference`        | is one stored request to run a prompt through a trained model.                                                         |
| `internal/training/db/model.TrainingDataset`              | is the record of a training dataset's JSONL file and its manifest.                                                     |
| `internal/training/db/model.TrainingJobEvent`             | is one event of a training job as reported by the sidecar.                                                             |
| `internal/training/db/model.TrainingJobRun`               | is one training job started on the sidecar, updated as its progress arrives.                                           |
| `internal/training/db/model.TrainingJobRunUpdate`         | is a partial update of a training job run where only set fields are written.                                           |
| `internal/training_audits.Config`                         | holds the settings of the engine that runs an audit (batch size, judge model, callbacks).                              |
| `internal/training_audits.Dataset`                        | is a training dataset read for an audit, with its examples and digest.                                                 |
| `internal/training_audits.Error`                          | is a failure of an audit operation with a Kind the HTTP handlers map to a status.                                      |
| `internal/training_audits.Event`                          | is one event of the live feed of a running audit.                                                                      |
| `internal/training_audits.Example`                        | is one record of a training dataset: a question and the answer the model was trained to give.                          |
| `internal/training_audits.Feed`                           | is the live feed of one running audit, with its watchers and pending answers.                                          |
| `internal/training_audits.Generation`                     | is the model's answer to one question, with its token counts.                                                          |
| `internal/training_audits.Manager`                        | starts training data audits in the background, one at a time per trained model.                                        |
| `internal/training_audits.Options`                        | holds the fields of the request body that starts an audit.                                                             |
| `internal/training_audits.Ops`                            | is the database backend the audit manager works over.                                                                  |
| `internal/training_audits.Progress`                       | is the state of a running audit after a judge batch.                                                                   |
| `internal/training_audits.RunParams`                      | holds the settings an audit was started with.                                                                          |
| `internal/training_audits.Watch`                          | is an audit and its case runs so far, plus a live event feed.                                                          |
| `internal/training_audits.judgeCase`                      | is one question/answer pair sent to the judge.                                                                         |
| `internal/training_audits.runningAudit`                   | tracks a running audit's ID and cancel function.                                                                       |
| `internal/training_audits.verdict`                        | is the judge's score and reason for one case.                                                                          |
| `internal/training_audits/api.Handlers`                   | are the HTTP endpoints of the training audits.                                                                         |
| `internal/training_audits/audittest.Judge`                | is a fake judge for the audit tests.                                                                                   |
| `internal/training_audits/audittest.Sidecar`              | is a fake sidecar for the audit tests.                                                                                 |
| `internal/training_audits/core.AuditCaseRun`              | is one dataset example of an audit, asked and judged.                                                                  |
| `internal/training_audits/core.AuditRun`                  | is one audit of a trained model over one dataset.                                                                      |
| `internal/training_audits/core.AuditStats`                | holds the statistics of an audit as a whole (judged, correct, tokens).                                                 |
| `internal/training_audits/core.Counts`                    | counts the case runs of a run by status.                                                                               |
| `internal/training_audits/core.Timing`                    | holds the creation, start and end times of a run.                                                                      |
| `internal/training_audits/db.Store`                       | holds the functions of a database backend as one value.                                                                |
