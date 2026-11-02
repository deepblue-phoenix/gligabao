package dbexport

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// queryer is what the introspection needs of a database or a transaction.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// introspect describes the tables of gligabao in the SQLite database, parents first (see sortTables). Rows is not set.
func introspect(ctx context.Context, q queryer) ([]Table, error) {
	tables, err := introspectSQLite(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("dbexport: read the schema: %w", err)
	}
	return sortTables(tables), nil
}

// quote quotes an identifier (the names come from the catalog, but they are not trusted to be plain).
func quote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// sortTables orders the tables so that each comes after the tables its foreign keys reference (not counting itself or tables that
// are not in the list). Among those that are ready the order is by name. A cycle is broken by taking the rest by name.
func sortTables(tables []Table) []Table {
	byName := make(map[string]*Table, len(tables))
	for i := range tables {
		byName[tables[i].Name] = &tables[i]
	}
	waiting := map[string]map[string]bool{}
	for _, t := range tables {
		deps := map[string]bool{}
		for _, fk := range t.ForeignKeys {
			if _, ok := byName[fk.RefTable]; ok && fk.RefTable != t.Name {
				deps[fk.RefTable] = true
			}
		}
		waiting[t.Name] = deps
	}
	var out []Table
	for len(waiting) > 0 {
		var ready []string
		for name, deps := range waiting {
			if len(deps) == 0 {
				ready = append(ready, name)
			}
		}
		sort.Strings(ready)
		if len(ready) == 0 { // a cycle: take one table, which frees the ones that wait for it
			for name := range waiting {
				ready = append(ready, name)
			}
			sort.Strings(ready)
			ready = ready[:1]
		}
		for _, name := range ready {
			out = append(out, *byName[name])
			delete(waiting, name)
			for _, deps := range waiting {
				delete(deps, name)
			}
		}
	}
	return out
}

//---------------------------------------------------------------------------------------------

func introspectSQLite(ctx context.Context, q queryer) ([]Table, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, sql FROM sqlite_master WHERE type = 'table' AND name LIKE 'gb\_%' ESCAPE '\' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var names, virtual []string
	for rows.Next() {
		var name string
		var ddl sql.NullString
		if err := rows.Scan(&name, &ddl); err != nil {
			rows.Close()
			return nil, err
		}
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(ddl.String)), "CREATE VIRTUAL") {
			virtual = append(virtual, name)
			continue
		}
		names = append(names, name)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The index of a virtual table (the R*Tree of the geo entities) is rebuilt by the database that loads the rows, and it keeps its data
	// in shadow tables named after it (<name>_node, ...): neither is part of the data.
	var tables []Table
next:
	for _, name := range names {
		for _, v := range virtual {
			if strings.HasPrefix(name, v+"_") {
				continue next
			}
		}
		t := Table{Name: name}
		if t.Columns, t.PrimaryKey, err = sqliteColumns(ctx, q, name); err != nil {
			return nil, err
		}
		tables = append(tables, t)
	}
	for i := range tables {
		if tables[i].ForeignKeys, err = sqliteForeignKeys(ctx, q, tables[i].Name, tables); err != nil {
			return nil, err
		}
	}
	return tables, nil
}

func sqliteColumns(ctx context.Context, q queryer, table string) (cols []Column, pk []string, err error) {
	rows, err := q.QueryContext(ctx, `PRAGMA table_info(`+quote(table)+`)`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	type pkCol struct {
		pos  int
		name string
	}
	var pks []pkCol
	for rows.Next() {
		var (
			cid, notNull, pos int
			name, typ         string
			dflt              sql.NullString
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pos); err != nil {
			return nil, nil, err
		}
		cols = append(cols, Column{Name: name, Type: typ})
		if pos > 0 {
			pks = append(pks, pkCol{pos, name})
		}
	}
	sort.Slice(pks, func(i, j int) bool { return pks[i].pos < pks[j].pos })
	for _, p := range pks {
		pk = append(pk, p.name)
	}
	return cols, pk, rows.Err()
}

func sqliteForeignKeys(ctx context.Context, q queryer, table string, all []Table) ([]ForeignKey, error) {
	rows, err := q.QueryContext(ctx, `PRAGMA foreign_key_list(`+quote(table)+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int]*ForeignKey{}
	var order []int
	for rows.Next() {
		var (
			id, seq                 int
			ref, from               string
			to                      sql.NullString
			onUpdate, onDelete, mtc string
		)
		if err := rows.Scan(&id, &seq, &ref, &from, &to, &onUpdate, &onDelete, &mtc); err != nil {
			return nil, err
		}
		fk := byID[id]
		if fk == nil {
			fk = &ForeignKey{RefTable: ref}
			byID[id] = fk
			order = append(order, id)
		}
		fk.Columns = append(fk.Columns, from)
		if to.Valid {
			fk.RefColumns = append(fk.RefColumns, to.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []ForeignKey
	for _, id := range order {
		fk := byID[id]
		if len(fk.RefColumns) != len(fk.Columns) { // "REFERENCES t": the primary key of t
			fk.RefColumns = nil
			for _, t := range all {
				if t.Name == fk.RefTable {
					fk.RefColumns = t.PrimaryKey
				}
			}
		}
		out = append(out, *fk)
	}
	return out, nil
}
