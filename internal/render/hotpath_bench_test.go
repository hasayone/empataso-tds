package render

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// benchDataDir writes a 1000-line list and a pool of three such files under a temp directory.
func benchDataDir(b *testing.B) string {
	dir := b.TempDir()
	var sb strings.Builder
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&sb, "line-%d.example.com\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "list.dat"), []byte(sb.String()), 0o644); err != nil {
		b.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "pool"), 0o755); err != nil {
		b.Fatal(err)
	}
	for _, n := range []string{"a.dat", "b.dat", "c.dat"} {
		if err := os.WriteFile(filepath.Join(dir, "pool", n), []byte(sb.String()), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return dir
}

// BenchmarkRandLine measures [RANDLINE] over a 1000-line file.
func BenchmarkRandLine(b *testing.B) {
	d := MacroDeps{DataDir: benchDataDir(b), Rng: rand.New(rand.NewSource(1))}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if Expand("[RANDLINE-(list.dat)-1]", d) == "" {
			b.Fatal("empty line")
		}
	}
}

// BenchmarkRandDfl measures [RANDDFL] over a directory of three 1000-line files.
func BenchmarkRandDfl(b *testing.B) {
	d := MacroDeps{DataDir: benchDataDir(b), Rng: rand.New(rand.NewSource(1))}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if Expand("[RANDDFL-(pool)-1]", d) == "" {
			b.Fatal("empty line")
		}
	}
}
