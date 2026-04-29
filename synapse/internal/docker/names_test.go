package docker

import (
	"strings"
	"testing"
)

func TestGenerateDeploymentName(t *testing.T) {
	for i := 0; i < 100; i++ {
		name := GenerateDeploymentName()
		parts := strings.Split(name, "-")
		if len(parts) != 3 {
			t.Errorf("expected 3 dash-parts, got %d in %q", len(parts), name)
			continue
		}
		if len(parts[2]) != 4 {
			t.Errorf("number suffix should be 4 digits, got %q", parts[2])
		}
		for _, c := range parts[2] {
			if c < '0' || c > '9' {
				t.Errorf("suffix %q has non-digit", parts[2])
				break
			}
		}
	}
}

func TestGenerateDeploymentNameUniquish(t *testing.T) {
	seen := make(map[string]bool)
	const N = 1000
	for i := 0; i < N; i++ {
		seen[GenerateDeploymentName()] = true
	}
	// We expect very few collisions out of 1000 — be lenient (>99% unique).
	if len(seen) < N*99/100 {
		t.Errorf("name generator produced %d unique out of %d — too many collisions", len(seen), N)
	}
}

func TestRandomHexLength(t *testing.T) {
	for _, n := range []int{1, 16, 32, 64} {
		out, err := RandomHex(n)
		if err != nil {
			t.Fatalf("RandomHex(%d): %v", n, err)
		}
		if len(out) != n*2 {
			t.Errorf("RandomHex(%d) = %d chars; want %d", n, len(out), n*2)
		}
	}
}
