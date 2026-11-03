// Package migrate moves a Precomputing file to a new version of its policy without starting over.
//
// What a file already holds keeps its meaning: streams, keys, values, rollups and precomputes stay
// as they were and go on counting. A migration may add to them (new streams, rollups,
// precomputes, quotas, raw events, samples, anomalies) and change settings that decide only what
// happens to events from now on or how long detail is kept. Everything new starts empty at the
// migration, which the file records. Anything that would change what is already counted is
// refused, with the reason.
package migrate

import (
	"encoding/json"
	"fmt"
	"strings"

	"precomputing.com/precomputing/compile"
	"precomputing.com/precomputing/internal/sqlite"
	"precomputing.com/precomputing/internal/version"
	"precomputing.com/precomputing/policy"
)

// Table is the record of migrations a file has been through.
const Table = "_precomputing_migrations"

// Refusal is a change the migration cannot make.
type Refusal struct{ Reasons []string }

func (r *Refusal) Error() string {
	if len(r.Reasons) == 1 {
		return "cannot migrate: " + r.Reasons[0]
	}
	return "cannot migrate:\n  " + strings.Join(r.Reasons, "\n  ")
}

// Plan is what a migration does.
type Plan struct {
	Added   []string // what starts empty at the migration, in words
	Changed []string // settings that change from now on, in words
	old     *policy.Policy
	new     *policy.Policy
	drop    []object // the old policy's views and triggers, made again
	create  []object // new tables and indexes, then every view and trigger of the new policy
	meta    [][]any  // _precomputing rows
	objects [][]any  // _precomputing_objects rows
}

type object struct{ kind, name, sql string }

// Prepare works out the migration of the file in db to the policy text src. It changes nothing.
func Prepare(db *sqlite.DB, src string) (*Plan, error) {
	oldText, err := one(db, "SELECT value FROM _precomputing WHERE key = 'policy'")
	if err != nil {
		return nil, fmt.Errorf("not a Precomputing file: %v", err)
	}
	format, _ := one(db, "SELECT value FROM _precomputing WHERE key = 'format'")
	if format != fmt.Sprint(version.Format) {
		return nil, fmt.Errorf("the file is in format %s; this release migrates format %d", format, version.Format)
	}
	oldPol, err := policy.Parse(oldText)
	if err != nil {
		return nil, fmt.Errorf("the policy in the file: %v", err)
	}
	newPol, err := policy.Parse(src)
	if err != nil {
		return nil, err
	}
	p := &Plan{old: oldPol, new: newPol}
	var why []string
	p.compare(&why)

	// The schemas, as SQLite itself records them, of the old policy, the new one and the file.
	oldObjs, err := schemaOf(oldPol)
	if err != nil {
		return nil, err
	}
	newObjs, err := schemaOf(newPol)
	if err != nil {
		return nil, err
	}
	fileObjs, err := objects(db)
	if err != nil {
		return nil, err
	}
	byName := func(list []object) map[string]object {
		m := map[string]object{}
		for _, o := range list {
			m[o.name] = o
		}
		return m
	}
	oldBy, newBy, fileBy := byName(oldObjs), byName(newObjs), byName(fileObjs)
	for _, o := range oldObjs {
		n, ok := newBy[o.name]
		switch {
		case !ok:
			why = append(why, fmt.Sprintf("%s %s would go, and with it what it holds", o.kind, o.name))
		case (o.kind == "table" || o.kind == "index") && n.sql != o.sql:
			why = append(why, fmt.Sprintf("table %s would change its columns; what it holds was counted the old way%s", o.name, sharedState(o.name, oldPol)))
		}
		if f, ok := fileBy[o.name]; (o.kind == "table" || o.kind == "index") && ok && f.sql != o.sql {
			why = append(why, fmt.Sprintf("%s %s in the file is not what the file's own policy makes", o.kind, o.name))
		}
	}
	for _, n := range newObjs {
		if _, ok := oldBy[n.name]; ok {
			continue
		}
		if f, ok := fileBy[n.name]; ok {
			why = append(why, fmt.Sprintf("the file already has a %s named %s, which the new policy would make", f.kind, n.name))
		}
	}
	if len(why) > 0 {
		return nil, &Refusal{why}
	}
	for _, o := range oldObjs {
		if o.kind == "view" || o.kind == "trigger" {
			p.drop = append(p.drop, o)
		}
	}
	for _, kind := range []string{"table", "index", "view", "trigger"} {
		for _, n := range newObjs {
			if n.kind != kind {
				continue
			}
			if _, ok := oldBy[n.name]; ok && (kind == "table" || kind == "index") {
				continue
			}
			p.create = append(p.create, n)
		}
	}
	scratch, err := scratchDB(newPol)
	if err != nil {
		return nil, err
	}
	defer scratch.Close()
	if p.meta, err = rows(scratch, "SELECT key, value FROM _precomputing ORDER BY key"); err != nil {
		return nil, err
	}
	if p.objects, err = rows(scratch, "SELECT name, kind, stream, detail FROM _precomputing_objects ORDER BY rowid"); err != nil {
		return nil, err
	}
	return p, nil
}

// sharedState explains a changed precompute table: precomputes that share a stream, by and per
// share one table, so a new one there may need a column the old ones never filled.
func sharedState(table string, pol *policy.Policy) string {
	if !strings.HasPrefix(table, "_pc") {
		return ""
	}
	var names []string
	for _, pc := range pol.Precomputes {
		name := "_pc_" + pc.Stream
		if pc.Func == "quantile" {
			name = "_pcq_" + pc.Stream + "_" + pc.Field
		}
		if len(pc.By) > 0 {
			name += "_by_" + strings.Join(pc.By, "_")
		}
		if pc.Per != "" {
			name += "_per_" + pc.Per
		}
		if name == table {
			names = append(names, pc.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return fmt.Sprintf(" (it holds the state of %s; a new precompute with the same stream, by and per would add a column to it: give it a by or per of its own)", strings.Join(names, ", "))
}

// Apply runs the migration in one transaction. at is the time to record it under, in seconds.
func (p *Plan) Apply(db *sqlite.DB, at int64) error {
	if err := db.Exec("BEGIN IMMEDIATE"); err != nil {
		return err
	}
	if err := p.apply(db, at); err != nil {
		db.Exec("ROLLBACK")
		return err
	}
	return db.Exec("COMMIT")
}

// failAfter makes apply fail after that many statements, so tests can see a failed migration leave
// the file as it was.
var failAfter = -1

func (p *Plan) apply(db *sqlite.DB, at int64) error {
	n := 0
	exec := func(sql string) error {
		if failAfter >= 0 && n >= failAfter {
			return fmt.Errorf("stopped for a test after %d statements", n)
		}
		n++
		return db.Exec(sql)
	}
	for i := len(p.drop) - 1; i >= 0; i-- {
		o := p.drop[i]
		if err := exec(fmt.Sprintf("DROP %s IF EXISTS %s", strings.ToUpper(o.kind), quote(o.name))); err != nil {
			return err
		}
	}
	for _, o := range p.create {
		if err := exec(o.sql); err != nil {
			return fmt.Errorf("%s %s: %v", o.kind, o.name, err)
		}
	}
	if err := exec("DELETE FROM _precomputing_objects"); err != nil {
		return err
	}
	for _, r := range p.objects {
		if err := run(db, "INSERT INTO _precomputing_objects (name, kind, stream, detail) VALUES (?, ?, ?, ?)", r...); err != nil {
			return err
		}
	}
	for _, r := range p.meta {
		if err := run(db, "INSERT OR REPLACE INTO _precomputing (key, value) VALUES (?, ?)", r...); err != nil {
			return err
		}
	}
	if err := exec("CREATE TABLE IF NOT EXISTS " + Table + " (at INTEGER NOT NULL, compiler TEXT NOT NULL, added TEXT NOT NULL, changed TEXT NOT NULL, old_policy TEXT NOT NULL)"); err != nil {
		return err
	}
	added, _ := json.Marshal(nonNil(p.Added))
	changed, _ := json.Marshal(nonNil(p.Changed))
	return run(db, "INSERT INTO "+Table+" (at, compiler, added, changed, old_policy) VALUES (?, ?, ?, ?, ?)",
		at, "precomputing "+version.Version, string(added), string(changed), p.old.Source)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func quote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// scratchDB is an in-memory database with a policy's compiled schema.
func scratchDB(pol *policy.Policy) (*sqlite.DB, error) {
	out, err := compile.Compile(pol, "")
	if err != nil {
		return nil, err
	}
	db, err := sqlite.Open(":memory:")
	if err != nil {
		return nil, err
	}
	if err := db.Exec(out.Schema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// schemaOf is the tables, indexes, views and triggers a policy makes, in the order it makes them.
func schemaOf(pol *policy.Policy) ([]object, error) {
	db, err := scratchDB(pol)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return objects(db)
}

func objects(db *sqlite.DB) ([]object, error) {
	rs, err := rows(db, "SELECT type, name, sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY rowid")
	if err != nil {
		return nil, err
	}
	out := make([]object, len(rs))
	for i, r := range rs {
		out[i] = object{r[0].(string), r[1].(string), r[2].(string)}
	}
	return out, nil
}

func rows(db *sqlite.DB, sql string, args ...any) ([][]any, error) {
	st, err := db.PrepareOnce(sql)
	if err != nil {
		return nil, err
	}
	defer st.Finalize()
	for i, a := range args {
		if err := st.Bind(i+1, a); err != nil {
			return nil, err
		}
	}
	var out [][]any
	for {
		ok, err := st.Step()
		if err != nil {
			return nil, err
		}
		if !ok {
			return out, nil
		}
		row := make([]any, st.Columns())
		for i := range row {
			row[i] = st.Value(i)
		}
		out = append(out, row)
	}
}

func run(db *sqlite.DB, sql string, args ...any) error {
	_, err := rows(db, sql, args...)
	return err
}

func one(db *sqlite.DB, sql string) (string, error) {
	rs, err := rows(db, sql)
	if err != nil {
		return "", err
	}
	if len(rs) == 0 {
		return "", fmt.Errorf("no row")
	}
	s, _ := rs[0][0].(string)
	return s, nil
}

// History is the migrations a file records, oldest first.
func History(db *sqlite.DB) ([][]any, error) {
	rs, err := rows(db, "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = '"+Table+"'")
	if err != nil || len(rs) == 0 {
		return nil, err
	}
	return rows(db, "SELECT at, compiler, added, changed FROM "+Table+" ORDER BY rowid")
}
