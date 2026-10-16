package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/convidera/impact-graph/internal/graph"
)

// versionLine is "impact <version> (<commit>, <go version>, <os>/<arch>)". A release build carries the version from
// -ldflags; `go install ...@vX.Y.Z` carries it in the build info; any other build is "dev".
func versionLine() string {
	version := graph.EngineVersion
	commit := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		if version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				commit = s.Value[:7]
			}
		}
	}
	parts := []string{}
	if commit != "" {
		parts = append(parts, commit)
	}
	parts = append(parts, runtime.Version(), runtime.GOOS+"/"+runtime.GOARCH)
	return fmt.Sprintf("impact %s (%s)", version, strings.Join(parts, ", "))
}
