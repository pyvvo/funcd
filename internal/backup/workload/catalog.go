package workload

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	// The cgo-free SQLite driver reads a DuckLake checkpoint (ADR-0222 Decision 5).
	_ "modernc.org/sqlite"

	"github.com/pyvvo/funcd/api/fault"
)

// fileQuery lists each file of a DuckLake file table (%s) with its table's and schema's paths, and whether each is
// relative.
const fileQuery = `SELECT f.path, f.path_is_relative, t.path, t.path_is_relative, s.path, s.path_is_relative
	FROM %s f JOIN ducklake_table t ON t.table_id = f.table_id JOIN ducklake_schema s ON s.schema_id = t.schema_id`

// CatalogFiles lists the objects a DuckLake checkpoint names (Decision 5): each ducklake_data_file and
// ducklake_delete_file path, resolved against its table's, its schema's and the data path, as a key relative to the
// data path, the catalog's prefix; sorted and distinct. It reads a read-only temporary copy of db. A path outside the
// data path is fault.Invalid: a restore into another prefix could not follow it.
func CatalogFiles(db []byte) ([]string, error) {
	const op = "workload.CatalogFiles"
	dir, err := os.MkdirTemp("", "funcd-catalog-")
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "copy the checkpoint")
	}
	defer func() { _ = os.RemoveAll(dir) }()
	file := filepath.Join(dir, "catalog.db")
	if err := os.WriteFile(file, db, 0o400); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "copy the checkpoint")
	}
	conn, err := sql.Open("sqlite", "file:"+file+"?mode=ro&immutable=1")
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "open the checkpoint")
	}
	defer func() { _ = conn.Close() }()
	var dataPath string
	if err := conn.QueryRow(`SELECT value FROM ducklake_metadata WHERE key = 'data_path'`).Scan(&dataPath); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "read the data path")
	}
	seen := map[string]bool{}
	for _, table := range []string{"ducklake_data_file", "ducklake_delete_file"} {
		if err := files(conn, fmt.Sprintf(fileQuery, table), dataPath, seen); err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	slices.Sort(out)
	return out, nil
}

func files(conn *sql.DB, query, dataPath string, seen map[string]bool) error {
	const op = "workload.CatalogFiles"
	rows, err := conn.Query(query)
	if err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "list the files")
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var file, table, schema string
		var fileRel, tableRel, schemaRel sql.NullBool
		if err := rows.Scan(&file, &fileRel, &table, &tableRel, &schema, &schemaRel); err != nil {
			return fault.Wrapf(err, fault.Invalid, op, "read a file row")
		}
		abs := file
		if fileRel.Bool {
			if tableRel.Bool {
				if schemaRel.Bool {
					schema = dataPath + schema
				}
				table = schema + table
			}
			abs = table + file
		}
		rel, ok := strings.CutPrefix(abs, dataPath)
		if !ok || rel == "" {
			return fault.Invalidf(op, "file %q is outside the data path %q", abs, dataPath)
		}
		seen[rel] = true
	}
	if err := rows.Err(); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fault.Wrapf(err, fault.Invalid, op, "list the files")
	}
	return nil
}
