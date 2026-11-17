package config

import "os"

// Build-info variables are injected at link time via -X flags when the binary
// is assembled by the CI pipeline or update.sh.
// An empty or non-hex commit means the binary was built outside the pipeline.
var (
	version    string
	commit     string
	commitDate string
	channel    string
)

// Values of BuildInfo.Env.
const (
	EnvProduction  = "production"
	EnvDevelopment = "development"
)

// envVar names the variable the installer sets to production in server.env of the server.
const envVar = "EMTDS_ENV"

// BuildInfo carries the version metadata embedded in the binary at build time and the
// environment the process runs in.
type BuildInfo struct {
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	CommitDate string `json:"commit_date"`
	Channel    string `json:"channel"`
	Env        string `json:"env"`
}

// Environment returns EnvProduction when EMTDS_ENV is exactly "production" and EnvDevelopment
// otherwise, so a local stack or a test never looks like the production server.
func Environment() string {
	if os.Getenv(envVar) == EnvProduction {
		return EnvProduction
	}
	return EnvDevelopment
}

// Build returns the build metadata for the running binary.
//
// Rules applied:
//   - commit must be exactly 40 lowercase hex digits; otherwise the build is
//     treated as a development build: Version "dev", Commit "", Channel "dev".
//   - version empty with a valid commit → first 12 characters of commit.
//   - channel outside "release", "main", "dev" → "dev".
//   - env is Environment() of the process, read on every call.
func Build() BuildInfo {
	c := commit
	if !isValidBuildCommit(c) {
		return BuildInfo{Version: "dev", Commit: "", CommitDate: commitDate, Channel: "dev", Env: Environment()}
	}
	ch := channel
	if ch != "release" && ch != "main" && ch != "dev" {
		ch = "dev"
	}
	v := version
	if v == "" {
		v = c[:12]
	}
	return BuildInfo{Version: v, Commit: c, CommitDate: commitDate, Channel: ch, Env: Environment()}
}

// isValidBuildCommit reports whether s is a 40-character lowercase hex string.
func isValidBuildCommit(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < 40; i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// Short returns the first 12 characters of Commit, or "dev" when Commit is
// shorter than 12 characters (including empty).
func (b BuildInfo) Short() string {
	if len(b.Commit) < 12 {
		return "dev"
	}
	return b.Commit[:12]
}

// IsDev reports whether the build is a development build
// (Version is "dev" or Commit is empty).
func (b BuildInfo) IsDev() bool {
	return b.Version == "dev" || b.Commit == ""
}

// SetBuildForTest replaces the package-level build variables with the fields
// from b and returns a function that restores the original values.
// Intended for use in tests from other packages only.
func SetBuildForTest(b BuildInfo) (restore func()) {
	orig := [4]string{version, commit, commitDate, channel}
	version, commit, commitDate, channel = b.Version, b.Commit, b.CommitDate, b.Channel
	return func() {
		version, commit, commitDate, channel = orig[0], orig[1], orig[2], orig[3]
	}
}
