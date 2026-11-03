package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"precomputing.com/precomputing/internal/sqlite"
	"precomputing.com/precomputing/migrate"
)

// migrateCmd moves a file to a new version of its policy, keeping everything it holds.
func migrateCmd(args []string) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	pol := fs.String("policy", "", "the new policy")
	dry := fs.Bool("dry-run", false, "say what the migration would do and change nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 || *pol == "" {
		fmt.Fprintln(os.Stderr, "precomputing migrate: give --policy NEW.precompute and one file")
		return 2
	}
	src, err := os.ReadFile(*pol)
	if err != nil {
		return fail("%v", err)
	}
	if _, err := os.Stat(fs.Arg(0)); err != nil {
		return fail("%v", err)
	}
	db, err := sqlite.Open(fs.Arg(0))
	if err != nil {
		return fail("%v", err)
	}
	defer db.Close()
	// Wait a while for a writer to finish its checkpoint; a migration needs the file to itself.
	db.Exec("PRAGMA busy_timeout = 10000")
	plan, err := migrate.Prepare(db, string(src))
	if err != nil {
		var refused *migrate.Refusal
		if errors.As(err, &refused) {
			fmt.Fprintln(os.Stderr, "precomputing: "+err.Error())
			fmt.Fprintln(os.Stderr, "Nothing was changed.")
			return 1
		}
		return fail("%v", err)
	}
	if len(plan.Added) == 0 && len(plan.Changed) == 0 {
		if plan.SameText() {
			fmt.Println("The file already holds this policy; nothing to do.")
			return 0
		}
		plan.Changed = append(plan.Changed, "only the policy's text, such as its comments: nothing is counted differently")
	}
	say := func(head string, list []string) {
		if len(list) > 0 {
			fmt.Println(head)
			for _, s := range list {
				fmt.Println("  " + s)
			}
		}
	}
	say("Starts empty now:", plan.Added)
	say("Changes from now on:", plan.Changed)
	if *dry {
		fmt.Println("Dry run: nothing was changed.")
		return 0
	}
	if err := plan.Apply(db, time.Now().Unix()); err != nil {
		return fail("the migration failed and the file is as it was: %v", err)
	}
	fmt.Printf("Migrated %s. Stop any writer before migrating and start it again after.\n", fs.Arg(0))
	return 0
}

// printMigrations lists the migrations a file records, for inspect.
func printMigrations(db *sqlite.DB) {
	hist, err := migrate.History(db)
	if err != nil || len(hist) == 0 {
		return
	}
	fmt.Println("\nMigrations:")
	for _, r := range hist {
		at, _ := r[0].(int64)
		var added, changed []string
		json.Unmarshal([]byte(r[2].(string)), &added)
		json.Unmarshal([]byte(r[3].(string)), &changed)
		fmt.Printf("\n  %s, by %s\n", time.Unix(at, 0).UTC().Format(time.RFC3339), r[1])
		for _, s := range added {
			fmt.Println("    started: " + s)
		}
		for _, s := range changed {
			fmt.Println("    changed: " + s)
		}
	}
}
