package cklauncher

import "fmt"

// Injected at build time via -ldflags; defaults apply in dev builds.
var (
	Version   = "0.2.15"
	GitCommit = "dev"
	BuiltAt   = ""
)

// FullVersion returns SemVer with build metadata commit suffix when available.
func FullVersion() string {
	if GitCommit == "" || GitCommit == "dev" {
		return Version
	}
	return fmt.Sprintf("%s+%s", Version, GitCommit)
}
