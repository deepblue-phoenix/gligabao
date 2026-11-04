package dbimport

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/dbexport"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_dataset"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_timeseries"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"

	log "github.com/sirupsen/logrus"
)

// Target is where an archive is loaded: the databases of the gligabao that starts the agent (what a db.DB holds; a type of its own
// so that this package does not depend on how the databases are opened).
type Target struct {
	// Relational is the relational and graph database, of the given Backend (config.DBBackendSQLite or config.DBBackendPostgres). It
	// must have the tables of the knowledge base and, to import the runs of the sub-agents, those of the research agents.
	Relational *sql.DB
	Backend    string
	// Vector is the vector store, EmbeddingDimensions the size of the embeddings it stores. Without a store (nil) the vectors of the
	// archive are not imported.
	Vector              vector.Store
	EmbeddingDimensions int
	// ImageDir is the directory of the image files ("" : the files are not copied).
	ImageDir string
}

// Options are the settings of an import.
type Options struct {
	// UserID owns what is imported (required): the datasets are looked up (and created) under it, and every user_id of the archive
	// becomes it.
	UserID string
	// RootRunID is the run of the target that stands for the main agent of the archive (its run has no parent). That run and its
	// states are not imported (the target recorded them itself); the runs of its sub-agents are, as its children. 0: no agent runs
	// are imported.
	RootRunID int64
	// SessionPrefix is the start of the session IDs that end with the ID of an agent run ("research-3", see agent_research.SessionID).
	// Those sessions are renamed to the IDs of the runs in the target. "" leaves the sessions as they are.
	SessionPrefix string
	// Runner is set on the runs of the sub-agents that are imported ("" keeps the archive's, which is "local": where the agent ran in
	// its container).
	Runner string
}

// TableResult counts what happened to the rows of one table.
type TableResult struct {
	// Rows is the number of rows in the archive.
	Rows int64 `json:"rows"`
	// Added are the rows that were inserted, Merged those that were matched with a row the target had (entities and series: the stored
	// row is kept), Skipped those that were left out (they belong to something that was not imported, or the table is not imported).
	Added   int64 `json:"added"`
	Merged  int64 `json:"merged"`
	Skipped int64 `json:"skipped"`
}

// Result says what an import did.
type Result struct {
	// Tables are the tables of the archive, by name.
	Tables map[string]*TableResult `json:"tables"`
	// Datasets maps the name of each dataset of the archive to its ID in the target.
	Datasets map[string]int64 `json:"datasets"`
	// Vectors are the vectors written, VectorsMerged those left out because their entity was merged with a stored one, and
	// VectorsSkipped those that could not be imported (no store, another embedding size, a row that was not imported).
	Vectors        int64 `json:"vectors"`
	VectorsMerged  int64 `json:"vectors_merged"`
	VectorsSkipped int64 `json:"vectors_skipped"`
	// Images are the image files copied.
	Images int `json:"images"`
	// Warnings are what was left out or went wrong after the rows were committed, and why.
	Warnings []string `json:"warnings,omitempty"`
}

// Summary is a line that says what was imported.
func (r *Result) Summary() string {
	var added, merged, skipped int64
	for _, t := range r.Tables {
		added, merged, skipped = added+t.Added, merged+t.Merged, skipped+t.Skipped
	}
	return fmt.Sprintf("%d rows added, %d merged into stored rows, %d skipped; %d vectors (%d for merged entities, %d not imported); %d images; %d warnings",
		added, merged, skipped, r.Vectors, r.VectorsMerged, r.VectorsSkipped, r.Images, len(r.Warnings))
}

func (r *Result) warnf(format string, args ...any) {
	w := fmt.Sprintf(format, args...)
	log.Warn("import: " + w)
	r.Warnings = append(r.Warnings, w)
}

func (r *Result) table(name string) *TableResult {
	if r.Tables[name] == nil {
		r.Tables[name] = &TableResult{}
	}
	return r.Tables[name]
}

// Tables of the archive that are imported, and those that are known but never imported.
var (
	imported = map[string]bool{
		"gb_models": true, "gb_inferences": true,
		"gb_artifacts": true, "gb_entities": true, "gb_edges": true, "gb_edge_artifacts": true, "gb_edge_events": true,
		"gb_artifact_classes": true, "gb_entity_artifacts": true, "gb_entity_sessions": true, "gb_edge_sessions": true,
		"gb_graph_in_ctx_counters": true, "gb_entity_geo": true,
		"gb_image_artifacts": true, "gb_image_extractions": true, "gb_image_text_blocks": true,
		"gb_series": true, "gb_series_points": true, "gb_series_anchors": true,
		"gb_remember_runs": true, "gb_remember_run_stages": true, "gb_remember_run_stage_outputs": true, "gb_query_runs": true,
		tableRuns: true, tableStates: true,
	}
	notImported = map[string]bool{
		tableDatasets:   true, // matched by name, before the rows
		"gb_ontologies": true, "gb_onto_classes": true, "gb_onto_edges": true,
	}
)

// Names of tables the import treats on its own.
const (
	tableDatasets  = "gb_datasets"
	tableModels    = "gb_models"
	tableRuns      = "gb_agent_research_runs"
	tableStates    = "gb_agent_research_states"
	tableEntities  = "gb_entities"
	tableEdges     = "gb_edges"
	tableArtifacts = "gb_artifacts"
	tableSeries    = "gb_series"
	tableInfs      = "gb_inferences"
	tableRemember  = "gb_remember_runs"
	tableStages    = "gb_remember_run_stages"
	tableQuery     = "gb_query_runs"
)

// importer is the state of one import.
type importer struct {
	r    *dbexport.Reader
	t    Target
	o    Options
	res  *Result
	pg   bool
	ids  map[string]map[int64]int64 // table -> ID in the archive -> ID in the target
	same map[string]map[int64]bool  // table -> archive IDs that were matched with a row the target had
	cols map[string]map[string]kind // table -> column -> kind, of the target's table

	roots        map[int64]bool // archive IDs of the runs of the main agent
	mergedSeries []int64        // IDs in the target of the series that were merged: their summary is recomputed
	images       map[string]string
	exportedAt   string
}

// Import loads the archive r into t. It returns what it did; the error is for a failure that left the relational database as it was
// (nothing of the rows is in then). Problems after the rows were committed (the vectors, the image files) are in Result.Warnings.
func Import(ctx context.Context, r *dbexport.Reader, t Target, o Options) (*Result, error) {
	if strings.TrimSpace(o.UserID) == "" {
		return nil, errors.New("dbimport: the user that owns the import is required")
	}
	if t.Relational == nil {
		return nil, errors.New("dbimport: no relational database")
	}
	if t.Backend != config.DBBackendSQLite && t.Backend != config.DBBackendPostgres {
		return nil, fmt.Errorf("dbimport: unknown database backend %q", t.Backend)
	}
	im := &importer{
		r: r, t: t, o: o, pg: t.Backend == config.DBBackendPostgres,
		res:   &Result{Tables: map[string]*TableResult{}, Datasets: map[string]int64{}},
		ids:   map[string]map[int64]int64{},
		same:  map[string]map[int64]bool{},
		cols:  map[string]map[string]kind{},
		roots: map[int64]bool{}, images: map[string]string{},
		exportedAt: r.Manifest.CreatedAt.UTC().Format(time.RFC3339),
	}
	if len(r.Manifest.Tables) == 0 {
		im.res.warnf("the archive has no tables (%s): nothing to import", skippedReasons(r.Manifest))
		return im.res, nil
	}

	if err := im.datasets(ctx); err != nil {
		return nil, err
	}
	tx, err := t.Relational.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("dbimport: %w", err)
	}
	defer tx.Rollback()
	tables := append([]dbexport.Table(nil), r.Manifest.Tables...)
	sort.SliceStable(tables, func(i, j int) bool { return level(tables[i].Name) < level(tables[j].Name) })
	for _, tb := range tables {
		if err := im.table(ctx, tx, tb); err != nil {
			return nil, fmt.Errorf("dbimport: table %s: %w", tb.Name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("dbimport: %w", err)
	}

	im.refreshSeries(ctx)
	im.vectors(ctx)
	im.copyImages()
	return im.res, nil
}

func runTable(name string) bool { return name == tableRuns || name == tableStates }

// level orders the tables beyond the order of their foreign keys (the manifest's, which each level keeps): the agent runs come first,
// because the sessions of every other row are renamed to the IDs of the runs they were mapped to; the runs of the pipelines, which
// hold IDs without a foreign key (the artifact, the inferences), come after the tables those are in; and what points at them (the
// outputs of their stages, the log of what went into prompts) last.
func level(name string) int {
	switch name {
	case tableRuns, tableStates:
		return 0
	case tableRemember, tableQuery:
		return 2
	case tableStages, "gb_remember_run_stage_outputs", "gb_graph_in_ctx_counters":
		return 3
	}
	return 1
}

func skippedReasons(m dbexport.Manifest) string {
	var parts []string
	for _, s := range m.Skipped {
		parts = append(parts, s.What+": "+s.Reason)
	}
	if len(parts) == 0 {
		return "the source has no SQLite database"
	}
	return strings.Join(parts, "; ")
}

//---------------------------------------------------------------------------------------------
// Datasets

// datasets finds (or creates) the dataset of the user for each dataset of the archive that was not deleted, and records the IDs.
func (im *importer) datasets(ctx context.Context) error {
	tb, ok := im.r.Table(tableDatasets)
	if !ok {
		return nil
	}
	res := im.res.table(tableDatasets)
	res.Rows = tb.Rows
	return im.r.Rows(tableDatasets, func(row dbexport.Row) error {
		old, ok := asInt(row["id"])
		name, _ := row["name"].(string)
		if !ok || name == "" {
			res.Skipped++
			return nil
		}
		if deleted, _ := asInt(row["deleted"]); deleted != 0 || row["deleted"] == true {
			res.Skipped++
			return nil
		}
		id, err := relational.EnsureDataset(ctx, im.t.Relational, im.o.UserID, name)
		if err != nil {
			return fmt.Errorf("dbimport: dataset %q: %w", name, err)
		}
		im.setID(tableDatasets, old, id)
		im.res.Datasets[name] = id
		res.Merged++
		return nil
	})
}

//---------------------------------------------------------------------------------------------
// Rows

func (im *importer) setID(table string, old, new int64) {
	if im.ids[table] == nil {
		im.ids[table] = map[int64]int64{}
	}
	im.ids[table][old] = new
}

func (im *importer) mapID(table string, old int64) (int64, bool) {
	id, ok := im.ids[table][old]
	return id, ok
}

func (im *importer) markSame(table string, old int64) {
	if im.same[table] == nil {
		im.same[table] = map[int64]bool{}
	}
	im.same[table][old] = true
}

// table imports the rows of one table of the archive.
func (im *importer) table(ctx context.Context, tx *sql.Tx, tb dbexport.Table) error {
	res := im.res.table(tb.Name)
	if tb.Name != tableDatasets {
		res.Rows = tb.Rows
	}
	switch {
	case notImported[tb.Name]:
		if tb.Name != tableDatasets && tb.Rows > 0 {
			res.Skipped = tb.Rows
			im.res.warnf("table %s (%d rows) is not imported", tb.Name, tb.Rows)
		}
		return nil
	case !imported[tb.Name]:
		if tb.Rows > 0 {
			res.Skipped = tb.Rows
			im.res.warnf("table %s (%d rows) is not known to this version: not imported", tb.Name, tb.Rows)
		}
		return nil
	case runTable(tb.Name) && im.o.RootRunID == 0:
		res.Skipped = tb.Rows
		return nil
	}

	cols, err := im.targetColumns(ctx, tx, tb.Name)
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		if tb.Rows > 0 {
			res.Skipped = tb.Rows
			im.res.warnf("the target has no table %s: its %d rows are not imported", tb.Name, tb.Rows)
		}
		return nil
	}
	autoID := len(tb.PrimaryKey) == 1 && tb.PrimaryKey[0] == "id" && cols["id"] == kInt
	fks := map[string]string{} // column -> the table it references (single-column keys only)
	for _, fk := range tb.ForeignKeys {
		if len(fk.Columns) == 1 {
			fks[fk.Columns[0]] = fk.RefTable
		}
	}

	return im.r.Rows(tb.Name, func(row dbexport.Row) error {
		vals := make(map[string]any, len(row))
		for k, v := range row {
			vals[k] = v
		}
		old, _ := asInt(vals["id"])

		skip, err := im.before(tb.Name, vals, old)
		if err != nil {
			return err
		}
		for col, ref := range fks {
			if skip || vals[col] == nil || ref == tableModels {
				continue
			}
			v, _ := asInt(vals[col])
			id, ok := im.mapID(ref, v)
			if !ok {
				skip = true // it belongs to something that was not imported
				break
			}
			vals[col] = json.Number(strconv.FormatInt(id, 10))
		}
		if skip {
			res.Skipped++
			return nil
		}
		if _, ok := vals["user_id"]; ok {
			vals["user_id"] = im.o.UserID
		}
		if s, ok := vals["session_id"].(string); ok {
			vals["session_id"] = im.session(s)
		}
		existing, skip, err := im.after(ctx, tx, tb.Name, vals, old)
		if err != nil {
			return err
		}
		switch {
		case skip:
			res.Skipped++
			return nil
		case existing > 0:
			im.setID(tb.Name, old, existing)
			im.markSame(tb.Name, old)
			res.Merged++
			return nil
		}

		names := make([]string, 0, len(vals))
		args := make([]any, 0, len(vals))
		ks := make([]kind, 0, len(vals))
		for _, c := range tb.Columns {
			v, present := vals[c.Name]
			k, inTarget := cols[c.Name]
			if !present || !inTarget || (autoID && c.Name == "id") {
				continue
			}
			cv, err := im.convert(v, k)
			if err != nil {
				return fmt.Errorf("column %s: %w", c.Name, err)
			}
			names, args, ks = append(names, c.Name), append(args, cv), append(ks, k)
		}
		id, added, err := im.insert(ctx, tx, tb.Name, names, args, ks, autoID)
		if err != nil {
			return fmt.Errorf("row %d: %w", old, err)
		}
		if !added { // a row that is already there (links and anchors are idempotent)
			res.Skipped++
			return nil
		}
		res.Added++
		if autoID {
			im.setID(tb.Name, old, id)
		}
		return nil
	})
}

// before is the part of the import of a row that comes before its foreign keys are mapped: it says whether to leave the row out.
func (im *importer) before(table string, vals map[string]any, old int64) (skip bool, err error) {
	switch table {
	case tableRuns:
		if vals["parent_id"] == nil {
			// The main agent: the caller's run stands for it, and for the states it has.
			im.roots[old] = true
			im.setID(tableRuns, old, im.o.RootRunID)
			return true, nil
		}
	case tableStates:
		run, _ := asInt(vals["run_id"])
		return im.roots[run], nil
	case tableModels:
		vals["supported"] = json.Number("0") // only the models this program knows are supported (they are set on startup)
	}
	return false, nil
}

// after is the part that comes once the foreign keys, the user and the session are mapped: it fixes the columns that need the maps,
// and finds the stored row an entity or series is merged into (existing > 0).
func (im *importer) after(ctx context.Context, tx *sql.Tx, table string, vals map[string]any, old int64) (existing int64, skip bool, err error) {
	switch table {
	case tableRuns:
		if im.o.Runner != "" {
			vals["runner"] = im.o.Runner
		}
		if vals["status"] == "running" { // the container ended before the agent did
			vals["status"], vals["finished_at"] = "failed", im.exportedAt
			if e, _ := vals["error"].(string); e == "" {
				vals["error"] = "interrupted: the agent was still working when the container ended"
			}
		}
	case tableEntities:
		dsID, _ := asInt(vals["dataset_id"])
		id, err := im.lookup(ctx, tx, "SELECT id FROM gb_entities WHERE dataset_id = %s AND name_key = %s AND type = %s", dsID, vals["name_key"], vals["type"])
		return id, false, err
	case tableSeries:
		dsID, _ := asInt(vals["dataset_id"])
		id, err := im.lookup(ctx, tx, "SELECT id FROM gb_series WHERE dataset_id = %s AND provider = %s AND key = %s AND field = %s", dsID, vals["provider"], vals["key"], vals["field"])
		if id > 0 {
			im.mergedSeries = append(im.mergedSeries, id)
		}
		return id, false, err
	case tableRemember, tableQuery:
		im.fixRun(table, vals)
	case tableStages:
		if s, ok := vals["inference_ids"].(string); ok {
			vals["inference_ids"] = im.inferenceIDs(s)
		}
	case "gb_graph_in_ctx_counters":
		item, _ := asInt(vals["item_id"])
		itemTable := tableEntities
		if vals["kind"] == "edge" {
			itemTable = tableEdges
		}
		id, ok := im.mapID(itemTable, item)
		if !ok {
			return 0, true, nil
		}
		vals["item_id"] = json.Number(strconv.FormatInt(id, 10))
		if vals["run_id"] != nil {
			runsOf := tableRemember
			if vals["pipeline"] == "query" {
				runsOf = tableQuery
			}
			run, _ := asInt(vals["run_id"])
			if id, ok := im.mapID(runsOf, run); ok {
				vals["run_id"] = json.Number(strconv.FormatInt(id, 10))
			} else {
				vals["run_id"] = nil
			}
		}
	case "gb_image_artifacts":
		// The files are in a directory per dataset: <dataset id>/<sha256>.<ext>.
		dsID, _ := asInt(vals["dataset_id"])
		p, _ := vals["path"].(string)
		newPath := p
		if _, rest, ok := strings.Cut(p, "/"); ok {
			newPath = strconv.FormatInt(dsID, 10) + "/" + rest
		}
		if clean := path.Clean(newPath); clean != newPath || strings.HasPrefix(newPath, "/") || strings.HasPrefix(newPath, "../") {
			return 0, true, nil // a path that leaves the image directory
		}
		im.images[p] = newPath
		vals["path"] = newPath
	}
	return 0, false, nil
}

// fixRun maps what the rows of gb_remember_runs and gb_query_runs hold that is not a foreign key: the artifact of a run, the
// inferences of the stages of a query run (gb_query_runs' <stage>_inference_ids), and the session in its parameters.
func (im *importer) fixRun(table string, vals map[string]any) {
	if v, ok := vals["artifact_id"]; ok && v != nil {
		old, _ := asInt(v)
		if id, ok := im.mapID(tableArtifacts, old); ok {
			vals["artifact_id"] = json.Number(strconv.FormatInt(id, 10))
		} else {
			vals["artifact_id"] = nil
		}
	}
	for col, v := range vals {
		s, ok := v.(string)
		if !ok {
			continue
		}
		switch {
		case strings.HasSuffix(col, "_inference_ids"):
			vals[col] = im.inferenceIDs(s)
		case col == "params":
			var m map[string]any
			if json.Unmarshal([]byte(s), &m) != nil || m == nil {
				continue
			}
			changed := false
			for k, v := range m {
				if sv, ok := v.(string); ok {
					if ns := im.session(sv); ns != sv {
						m[k], changed = ns, true
					}
				}
			}
			if changed {
				b, _ := json.Marshal(m)
				vals[col] = string(b)
			}
		}
	}
}

// inferenceIDs maps a JSON list of inference IDs of the archive to the IDs in the target; the ones that were not imported are dropped.
func (im *importer) inferenceIDs(s string) string {
	var list []int64
	if json.Unmarshal([]byte(s), &list) != nil {
		return s
	}
	out := make([]int64, 0, len(list))
	for _, old := range list {
		if id, ok := im.mapID(tableInfs, old); ok {
			out = append(out, id)
		}
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// session renames a session ID that ends with the ID of an agent run of the archive to the ID of the run in the target (what
// agent_research.SessionID makes of it).
func (im *importer) session(s string) string {
	prefix := im.o.SessionPrefix
	if prefix == "" || !strings.HasPrefix(s, prefix) || im.o.RootRunID == 0 {
		return s
	}
	rest := s[len(prefix):]
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || strconv.FormatInt(n, 10) != rest {
		return s
	}
	if id, ok := im.mapID(tableRuns, n); ok {
		return prefix + strconv.FormatInt(id, 10)
	}
	// Not a run that was imported: its number means another run in the target, so it must not stay as it is.
	return prefix + "imported-" + rest
}

// lookup returns the ID of the row that the query finds (0 if none); the query has one %s for each of args, as a placeholder.
func (im *importer) lookup(ctx context.Context, tx *sql.Tx, query string, dsID int64, args ...any) (int64, error) {
	ph := make([]any, 0, 1+len(args))
	vals := []any{dsID}
	ph = append(ph, im.ph(1, kText))
	for i, a := range args {
		s, _ := a.(string)
		ph = append(ph, im.ph(i+2, kText))
		vals = append(vals, s)
	}
	var id int64
	err := tx.QueryRowContext(ctx, fmt.Sprintf(query, ph...), vals...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

//---------------------------------------------------------------------------------------------
// SQL

// kind is what a column holds, as far as the conversion of a value is concerned.
type kind int

const (
	kText kind = iota
	kInt
	kFloat
	kBool
	kTime
	kJSON
	kBytes
)

// kindOf is the kind of a column of a declared type (SQLite: "INTEGER", "DATETIME", ...; PostgreSQL: its data_type, "bigint", "timestamp
// with time zone", "jsonb", ...).
func kindOf(typ string) kind {
	t := strings.ToLower(typ)
	switch {
	case strings.Contains(t, "json"):
		return kJSON
	case strings.Contains(t, "bool"):
		return kBool
	case strings.Contains(t, "timestamp") || strings.Contains(t, "datetime") || t == "date":
		return kTime
	case strings.Contains(t, "int"):
		return kInt
	case strings.Contains(t, "double") || strings.Contains(t, "real") || strings.Contains(t, "float") || strings.Contains(t, "numeric"):
		return kFloat
	case strings.Contains(t, "bytea") || strings.Contains(t, "blob"):
		return kBytes
	}
	return kText
}

// targetColumns returns the kinds of the columns of the target's table (empty if there is no such table).
func (im *importer) targetColumns(ctx context.Context, tx *sql.Tx, table string) (map[string]kind, error) {
	if c, ok := im.cols[table]; ok {
		return c, nil
	}
	out := map[string]kind{}
	if im.pg {
		rows, err := tx.QueryContext(ctx, `SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1`, table)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var name, typ string
			if err := rows.Scan(&name, &typ); err != nil {
				return nil, err
			}
			out[name] = kindOf(typ)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	} else {
		rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+quote(table)+`)`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				cid, notNull, pk int
				name, typ        string
				dflt             sql.NullString
			)
			if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
				return nil, err
			}
			out[name] = kindOf(typ)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	im.cols[table] = out
	return out, nil
}

func quote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// ph is the placeholder of the n-th argument (from 1).
func (im *importer) ph(n int, k kind) string {
	if !im.pg {
		return "?"
	}
	if k == kJSON {
		return "$" + strconv.Itoa(n) + "::jsonb"
	}
	return "$" + strconv.Itoa(n)
}

// insert adds a row; a row that conflicts with a stored one (a unique key) is not added. With withID it returns the ID the row got.
func (im *importer) insert(ctx context.Context, tx *sql.Tx, table string, cols []string, args []any, kinds []kind, withID bool) (id int64, added bool, err error) {
	q := make([]string, len(cols))
	quoted := make([]string, len(cols))
	for i, c := range cols {
		q[i], quoted[i] = im.ph(i+1, kinds[i]), quote(c)
	}
	stmt := "INSERT INTO " + quote(table) + " (" + strings.Join(quoted, ", ") + ") VALUES (" + strings.Join(q, ", ") + ") ON CONFLICT DO NOTHING"
	if withID {
		err = tx.QueryRowContext(ctx, stmt+" RETURNING id", args...).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return id, err == nil, err
	}
	res, err := tx.ExecContext(ctx, stmt, args...)
	if err != nil {
		return 0, false, err
	}
	n, err := res.RowsAffected()
	return 0, n > 0, err
}

// convert turns a value of the archive into one the driver gives to a column of kind k.
func (im *importer) convert(v any, k kind) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case json.Number:
		switch k {
		case kInt:
			if n, err := x.Int64(); err == nil {
				return n, nil
			}
			f, err := x.Float64()
			return int64(f), err
		case kFloat:
			return x.Float64()
		case kBool:
			f, err := x.Float64()
			return f != 0, err
		}
		return x.String(), nil
	case bool:
		switch k {
		case kBool:
			return x, nil
		case kInt:
			if x {
				return int64(1), nil
			}
			return int64(0), nil
		}
		return strconv.FormatBool(x), nil
	case string:
		switch k {
		case kInt:
			return strconv.ParseInt(x, 10, 64)
		case kFloat:
			return strconv.ParseFloat(x, 64)
		case kBool:
			return strconv.ParseBool(x)
		case kBytes:
			return []byte(x), nil
		case kTime:
			if !im.pg {
				return x, nil // SQLite keeps the RFC 3339 text
			}
			t, err := time.Parse(time.RFC3339Nano, x)
			return t, err
		}
		return x, nil
	case map[string]any:
		if enc, ok := x["$base64"].(string); ok {
			b, err := base64.StdEncoding.DecodeString(enc)
			if err != nil {
				return nil, err
			}
			if k == kBytes {
				return b, nil
			}
			return string(b), nil
		}
	}
	b, err := json.Marshal(v)
	return string(b), err
}

func asInt(v any) (int64, bool) {
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n, true
		}
		f, err := x.Float64()
		return int64(f), err == nil
	case float64:
		return int64(x), true
	case int64:
		return x, true
	case int:
		return int64(x), true
	}
	return 0, false
}

//---------------------------------------------------------------------------------------------
// After the commit

// refreshSeries recomputes the summary of the series that points were added to (the points of the archive are in the series the target
// had; its count, its first and last time and its statistics were those of its own points).
func (im *importer) refreshSeries(ctx context.Context) {
	for _, id := range im.mergedSeries {
		if err := im.refreshOne(ctx, id); err != nil {
			im.res.warnf("the summary of series %d was not refreshed: %v", id, err)
		}
	}
}

func (im *importer) refreshOne(ctx context.Context, id int64) error {
	tx, err := graph.Begin(ctx, im.t.Relational)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	points, err := tx.ListSeriesPoints(ctx, id)
	if err != nil {
		return err
	}
	stats := memory_timeseries.Summarize(points)
	if stats == nil {
		return nil
	}
	js, err := json.Marshal(stats)
	if err != nil {
		return err
	}
	if err := tx.UpdateSeriesSummary(ctx, id, &stats.FirstTS, &stats.LastTS, stats.Count, string(js), relational.Now()); err != nil {
		return err
	}
	return tx.Commit()
}

// vectors copies the vectors of the archive to the vector store, to the vector datasets of the datasets they were matched to.
func (im *importer) vectors(ctx context.Context) {
	m := &im.r.Manifest
	var total int64
	for _, vd := range m.Vectors {
		total += vd.Count
	}
	if total == 0 {
		return
	}
	skipAll := func(why string) {
		im.res.VectorsSkipped += total
		im.res.warnf("the %d vectors of the archive are not imported: %s", total, why)
	}
	switch {
	case im.t.Vector == nil:
		skipAll("the target has no vector store")
		return
	case m.EmbeddingDimensions != im.t.EmbeddingDimensions:
		skipAll(fmt.Sprintf("the archive's embeddings have %d dimensions and the target stores %d", m.EmbeddingDimensions, im.t.EmbeddingDimensions))
		return
	}
	for _, vd := range m.Vectors {
		if vd.DatasetID == nil {
			im.res.VectorsSkipped += vd.Count
			im.res.warnf("vector dataset %q is not one of a gligabao dataset: not imported", vd.Name)
			continue
		}
		newDS, ok := im.mapID(tableDatasets, *vd.DatasetID)
		if !ok {
			im.res.VectorsSkipped += vd.Count
			continue
		}
		//------------------
		// MEMORY DATASET
		target := memory_dataset.VectorDataset(newDS, memory_dataset.VectorIndex(vd.Index))
		//------------------
		if err := im.copyVectors(ctx, vd, target); err != nil {
			im.res.warnf("vector dataset %q was not imported completely: %v", vd.Name, err)
		}
	}
}

const vectorBatch = 200

func (im *importer) copyVectors(ctx context.Context, vd dbexport.VectorDataset, target string) error {
	var batch []vector.Vector
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := im.t.Vector.Upsert(ctx, target, batch)
		if err == nil {
			im.res.Vectors += int64(len(batch))
		} else {
			im.res.VectorsSkipped += int64(len(batch))
		}
		batch = batch[:0]
		return err
	}
	err := im.r.Vectors(vd.Name, func(v vector.Vector) error {
		nv, same, ok := im.mapVector(v)
		switch {
		case same:
			im.res.VectorsMerged++
		case !ok:
			im.res.VectorsSkipped++
		default:
			batch = append(batch, nv)
			if len(batch) >= vectorBatch {
				return flush()
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return flush()
}

// mapVector gives a vector the ID and the metadata of the row it was mapped to. same is true for the vector of an entity that was
// merged into a stored one; ok is false for a vector whose row was not imported.
func (im *importer) mapVector(v vector.Vector) (out vector.Vector, same, ok bool) {
	cut := strings.LastIndex(v.ID, "-")
	if cut < 0 {
		return v, false, false
	}
	kindName, num := v.ID[:cut], v.ID[cut+1:]
	old, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return v, false, false
	}
	var table string
	switch kindName {
	case memory.VectorKindEntity:
		table = tableEntities
	case memory.VectorKindEdge:
		table = tableEdges
	case memory.VectorKindChunk, memory.VectorKindSummary:
		table = tableArtifacts
	default:
		return v, false, false
	}
	if im.same[table][old] {
		return v, true, false
	}
	id, found := im.mapID(table, old)
	if !found {
		return v, false, false
	}
	out = vector.Vector{ID: kindName + "-" + strconv.FormatInt(id, 10), Values: v.Values}
	if v.Metadata != nil {
		out.Metadata = make(map[string]any, len(v.Metadata))
		for k, val := range v.Metadata {
			out.Metadata[k] = val
		}
		for key, ref := range map[string]string{"entity_id": tableEntities, "edge_id": tableEdges, "artifact_id": tableArtifacts, "parent_id": tableArtifacts, "chunk_id": tableArtifacts} {
			val, has := out.Metadata[key]
			if !has {
				continue
			}
			n, isNum := asInt(val)
			if mapped, found := im.mapID(ref, n); isNum && found {
				out.Metadata[key] = mapped
			} else {
				delete(out.Metadata, key)
			}
		}
	}
	return out, false, true
}

// copyImages writes the image files of the images that were imported to the image directory, under the paths the rows now have.
func (im *importer) copyImages() {
	if len(im.images) == 0 {
		return
	}
	if im.t.ImageDir == "" {
		im.res.warnf("the %d image files of the archive are not copied: the target has no image directory", len(im.images))
		return
	}
	err := im.r.Images(func(p string, content io.Reader) error {
		newPath, ok := im.images[p]
		if !ok {
			return nil
		}
		full := filepath.Join(im.t.ImageDir, filepath.FromSlash(newPath))
		if _, err := os.Stat(full); err == nil {
			return nil // the same image of the same dataset is stored already
		}
		if err := writeFileAtomic(full, content); err != nil {
			im.res.warnf("image file %s was not copied: %v", newPath, err)
			return nil
		}
		im.res.Images++
		return nil
	})
	if err != nil {
		im.res.warnf("the image files were not copied completely: %v", err)
	}
}
