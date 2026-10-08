// Package version holds the release number shared by the CLI, the compiler
// and the files they write.
package version

// Version is the release of this code.
const Version = "0.2.0"

// Format is the version of the SQLite file layout the compiler writes.
// It changes only when a file written by an older release would be read wrongly.
const Format = 1
