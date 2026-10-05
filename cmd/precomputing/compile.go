package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"precomputing.com/precomputing/compile"
)

func compileCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("compile", flag.ContinueOnError)
	fs.SetOutput(stderr)
	distill := fs.Bool("distill", false, "print the distill statements instead of the schema")
	outPath := fs.String("o", "", "write to this file instead of standard output")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "precomputing compile: give exactly one policy file")
		return 2
	}
	path := fs.Arg(0)
	src, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "precomputing: %v\n", err)
		return 1
	}
	out, err := compile.Source(string(src), filepath.Base(path))
	if err != nil {
		fmt.Fprintf(stderr, "%s:%v\n", path, err)
		return 1
	}
	text := out.Schema
	if *distill {
		text = out.Distill
	}
	if *outPath == "" {
		fmt.Fprint(stdout, text)
		return 0
	}
	if err := os.WriteFile(*outPath, []byte(text), 0o644); err != nil {
		fmt.Fprintf(stderr, "precomputing: %v\n", err)
		return 1
	}
	return 0
}
