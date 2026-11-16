package config

import "testing"

// saveVars saves the current build variables and returns a restore function.
func saveVars() func() {
	v, c, d, ch := version, commit, commitDate, channel
	return func() { version, commit, commitDate, channel = v, c, d, ch }
}

// testCommit40 is a valid 40-character lowercase hex commit SHA.
const testCommit40 = "aabbccdd11223344556677889900112233445566"

func TestBuildDev(t *testing.T) {
	defer saveVars()()
	version, commit, commitDate, channel = "", "", "", ""

	b := Build()
	if b.Version != "dev" {
		t.Errorf("Version = %q, want dev", b.Version)
	}
	if b.Commit != "" {
		t.Errorf("Commit = %q, want empty", b.Commit)
	}
	if b.Channel != "dev" {
		t.Errorf("Channel = %q, want dev", b.Channel)
	}
	if !b.IsDev() {
		t.Error("IsDev() = false, want true")
	}
	if b.Short() != "dev" {
		t.Errorf("Short() = %q, want dev", b.Short())
	}
}

func TestBuildInvalidCommit(t *testing.T) {
	defer saveVars()()
	// Uppercase letters are not valid hex in a commit.
	version, commit, commitDate, channel = "v1.0.0", "AABBCCDD1122334455667788990011223344556677", "", "release"

	b := Build()
	if !b.IsDev() {
		t.Errorf("invalid commit should produce dev build, got %+v", b)
	}
}

func TestBuildValidCommit(t *testing.T) {
	defer saveVars()()
	version, commit, commitDate, channel = "v1.2.3", testCommit40, "2024-01-01T00:00:00Z", "release"

	b := Build()
	if b.Version != "v1.2.3" {
		t.Errorf("Version = %q, want v1.2.3", b.Version)
	}
	if b.Commit != testCommit40 {
		t.Errorf("Commit = %q", b.Commit)
	}
	if b.Channel != "release" {
		t.Errorf("Channel = %q, want release", b.Channel)
	}
	if b.CommitDate != "2024-01-01T00:00:00Z" {
		t.Errorf("CommitDate = %q", b.CommitDate)
	}
	if b.Short() != testCommit40[:12] {
		t.Errorf("Short() = %q, want %q", b.Short(), testCommit40[:12])
	}
	if b.IsDev() {
		t.Error("IsDev() = true, want false")
	}
}

func TestBuildEmptyVersion(t *testing.T) {
	defer saveVars()()
	version, commit, commitDate, channel = "", testCommit40, "", "main"

	b := Build()
	if b.Version != testCommit40[:12] {
		t.Errorf("empty version → first 12 of commit, got %q", b.Version)
	}
}

func TestBuildBadChannel(t *testing.T) {
	defer saveVars()()
	version, commit, commitDate, channel = "v1.0.0", testCommit40, "", "staging"

	b := Build()
	if b.Channel != "dev" {
		t.Errorf("bad channel should become dev, got %q", b.Channel)
	}
}

func TestBuildChannelMain(t *testing.T) {
	defer saveVars()()
	version, commit, commitDate, channel = "v1.0.0", testCommit40, "", "main"

	b := Build()
	if b.Channel != "main" {
		t.Errorf("Channel = %q, want main", b.Channel)
	}
}

func TestSetBuildForTest(t *testing.T) {
	fake := BuildInfo{
		Version:    "v9.9.9",
		Commit:     "1234567890abcdef1234567890abcdef12345678",
		CommitDate: "2025-01-01T00:00:00Z",
		Channel:    "main",
	}
	restore := SetBuildForTest(fake)

	b := Build()
	if b.Commit != fake.Commit {
		restore()
		t.Fatalf("Commit = %q, want %q", b.Commit, fake.Commit)
	}
	if b.Version != fake.Version {
		restore()
		t.Fatalf("Version = %q, want %q", b.Version, fake.Version)
	}
	if b.Channel != fake.Channel {
		restore()
		t.Fatalf("Channel = %q, want %q", b.Channel, fake.Channel)
	}

	restore()

	b2 := Build()
	if !b2.IsDev() {
		t.Fatalf("after restore, IsDev = false: %+v", b2)
	}
}
