package docker

import "testing"

// TestContainerName_BackCompat: a single-replica deployment keeps the
// pre-v0.5 container name. Critical for back-compat with existing
// containers, dashboards, the proxy resolver, Playwright fixtures,
// `docker ps --filter label=synapse.managed=true` snapshots, and any
// operator scripts that grep for `convex-<name>`.
func TestContainerName_BackCompat(t *testing.T) {
	got := ContainerName("happy-cat-1234", 0, false)
	if got != "convex-happy-cat-1234" {
		t.Errorf("non-HA: got %q want convex-happy-cat-1234", got)
	}

	// Even with a non-zero replica index, single-replica mode keeps the
	// legacy name. (We don't expect callers to pass replica_index>0 in
	// non-HA mode, but if someone does we shouldn't quietly mangle the
	// name into something like `convex-happy-cat-1234-7`.)
	got = ContainerName("happy-cat-1234", 7, false)
	if got != "convex-happy-cat-1234" {
		t.Errorf("non-HA with idx: got %q want convex-happy-cat-1234", got)
	}
}

// TestContainerName_HASuffix: HA mode picks up `-{idx}` so two replicas
// of the same deployment can coexist on one Docker daemon.
func TestContainerName_HASuffix(t *testing.T) {
	cases := []struct {
		name string
		idx  int
		want string
	}{
		{"happy-cat-1234", 0, "convex-happy-cat-1234-0"},
		{"happy-cat-1234", 1, "convex-happy-cat-1234-1"},
		{"my-app", 9, "convex-my-app-9"},
	}
	for _, tc := range cases {
		got := ContainerName(tc.name, tc.idx, true)
		if got != tc.want {
			t.Errorf("ContainerName(%q, %d, true): got %q want %q", tc.name, tc.idx, got, tc.want)
		}
	}
}

// TestVolumeName mirrors the container-name conventions: legacy names
// for single-replica, suffixed for HA. Critical because docker volumes
// outlive containers — renaming them breaks SQLite-backed restarts.
func TestVolumeName(t *testing.T) {
	if got := VolumeName("happy-cat-1234", 0, false); got != "synapse-data-happy-cat-1234" {
		t.Errorf("non-HA volume: got %q", got)
	}
	if got := VolumeName("happy-cat-1234", 1, true); got != "synapse-data-happy-cat-1234-1" {
		t.Errorf("HA volume: got %q", got)
	}
}
