// Package dbimport loads an archive written by package dbexport (what a gligabao_agent that ran in Modal learned: its knowledge base, the
// runs and the costs it recorded, its embeddings and its images) into the databases of another gligabao, the one that started the
// agent. The archive has the IDs of the agent's database; here they are remapped, because the target has rows of its own.
//
// What the import does, table by table (every table is read in the order of the manifest, parents first):
//
//   - Datasets (gb_datasets) are matched by name: the live dataset of Options.UserID with the name of the archive's dataset is used, and
//     created if it does not exist. A dataset that was deleted in the archive is left out with everything in it. Every user_id of the
//     archive is replaced by Options.UserID: the knowledge belongs to the user that started the agent.
//   - Entities (gb_entities) and time series (gb_series) are merged into the ones the dataset already has (an entity by name key and
//     type, a series by provider, key and field): the stored row is kept, and what the archive knows about it (the artifacts and facts
//     that mention it, the points of the series, the place of the entity) is added to it. Everything else (artifacts, facts, images,
//     calls of models, runs of the remember and query pipelines, the log of what went into prompts) is added as new rows.
//   - The models of the archive (gb_models) are added when the target does not know them. The inferences (gb_inferences) are added with
//     their costs: the cost of a research is then the sum of what was spent in the container too.
//   - The agent runs: the run of the main agent is the run the caller already has (Options.RootRunID), so it and its states are not
//     imported; the runs of its sub-agents are, as its children, with their states and reports. The sessions that the agents
//     remembered under (Options.SessionPrefix followed by the run's ID) are renamed to the IDs the runs have in the target, so what each
//     agent contributed can be told, as for an agent that ran in this process.
//   - Vectors go to the vector store of the target, in the vector datasets of the datasets they were matched to, under the IDs and with
//     the metadata of the rows they were mapped to; the vectors of entities that were merged are not copied (the entity has its own).
//     They are copied only when the embeddings have the size the target stores. The image files are copied to the image directory,
//     into the directory of the dataset they now belong to.
//
// The rows are written in one transaction, so an import that fails leaves the relational database as it was; the vectors and the image
// files are written after it commits (a failure there is in Result.Warnings: the rows are in). Tables that are not part of the knowledge
// base or the agent runs (the ontologies, an empty set of tables from PostgreSQL) are left out. Importing the same archive twice adds
// its rows twice, but for the merged ones.
package dbimport
