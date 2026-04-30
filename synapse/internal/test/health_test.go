package synapsetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Iann29/synapse/internal/health"
)

// fakeRestarter satisfies health.Restarter for the auto-restart tests.
type fakeRestarter struct {
	fn func(ctx context.Context, name string) error
}

func (f *fakeRestarter) Restart(ctx context.Context, name string) error {
	return f.fn(ctx, name)
}

// RestartReplica defaults to forwarding to the legacy Restart so existing
// single-replica auto-restart tests don't have to special-case the new
// signature. Tests that exercise the HA-aware path can wrap fn themselves.
func (f *fakeRestarter) RestartReplica(ctx context.Context, name string, _ int) error {
	return f.fn(ctx, name)
}

// Worker.sweep against real postgres + the harness's FakeDocker. Verifies
// that a "running" row whose container is gone gets reconciled to "stopped".
func TestHealthWorker_ReconcilesGoneContainer(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Health Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")
	id := h.SeedDeployment(proj.ID, "stale-cat-1234", "dev", "running", false, owner.ID, 4000, "k")

	// Docker reports the container has vanished.
	h.Docker.StatusFn = func(_ context.Context, _ string) (string, error) {
		return "", nil
	}

	w := &health.Worker{
		DB:     h.DB,
		Docker: h.Docker,
		Config: health.Config{Interval: time.Hour, StatusTimeout: 2 * time.Second},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go w.Run(ctx)

	// Sweep runs immediately on start. Poll for the status change.
	deadline := time.Now().Add(3 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		s, err := health.LookupRow(context.Background(), h.DB, id)
		if err != nil {
			t.Fatalf("lookup: %v", err)
		}
		last = s
		if s == "stopped" {
			cancel()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected status=stopped, still %q", last)
}

func TestHealthWorker_ReconcilesExited(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Health Co 2")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")
	id := h.SeedDeployment(proj.ID, "ex-fox-1234", "dev", "running", false, owner.ID, 4001, "k")

	h.Docker.StatusFn = func(_ context.Context, _ string) (string, error) {
		return "exited", nil
	}

	w := &health.Worker{
		DB:     h.DB,
		Docker: h.Docker,
		Config: health.Config{Interval: time.Hour, StatusTimeout: 2 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go w.Run(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, _ := health.LookupRow(context.Background(), h.DB, id)
		if s == "stopped" {
			cancel()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("expected exited→stopped reconciliation")
}

// AutoRestart on: "exited" container gets restarted, row flips back to running.
func TestHealthWorker_AutoRestartsStopped(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "AR Co")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")
	id := h.SeedDeployment(proj.ID, "ar-cat-1234", "dev", "running", false, owner.ID, 4010, "k")

	// Step 1: docker reports exited → worker should mark stopped, then
	// restart, then mark running.
	statusCalls := 0
	h.Docker.StatusFn = func(_ context.Context, _ string) (string, error) {
		statusCalls++
		if statusCalls == 1 {
			return "exited", nil
		}
		return "running", nil
	}

	restartCalls := 0
	restarter := &fakeRestarter{
		fn: func(_ context.Context, _ string) error {
			restartCalls++
			return nil
		},
	}

	w := &health.Worker{
		DB:        h.DB,
		Docker:    h.Docker,
		Restarter: restarter,
		Config:    health.Config{Interval: time.Hour, StatusTimeout: 2 * time.Second, AutoRestart: true},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	w.Run(ctx)

	if restartCalls != 1 {
		t.Errorf("expected 1 restart call, got %d", restartCalls)
	}
	s, _ := health.LookupRow(context.Background(), h.DB, id)
	if s != "running" {
		t.Errorf("expected status=running after restart, got %q", s)
	}
}

// AutoRestart on: container not found → restart fails, row promoted to failed.
func TestHealthWorker_RestartFailedContainerNotFound(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "AR Co 2")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")
	id := h.SeedDeployment(proj.ID, "ar-fox-1234", "dev", "running", false, owner.ID, 4011, "k")

	h.Docker.StatusFn = func(_ context.Context, _ string) (string, error) {
		return "exited", nil
	}
	restarter := &fakeRestarter{
		fn: func(_ context.Context, _ string) error {
			return errors.New("container not found")
		},
	}

	w := &health.Worker{
		DB:        h.DB,
		Docker:    h.Docker,
		Restarter: restarter,
		Config:    health.Config{Interval: time.Hour, StatusTimeout: 2 * time.Second, AutoRestart: true},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	w.Run(ctx)

	s, _ := health.LookupRow(context.Background(), h.DB, id)
	if s != "failed" {
		t.Errorf("expected status=failed after restart-on-missing-container, got %q", s)
	}
}

// "running" → no change; row stays running.
func TestHealthWorker_LeavesRunningAlone(t *testing.T) {
	h := Setup(t)
	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Health Co 3")
	proj := createProject(t, h, owner.AccessToken, team.Slug, "App")
	id := h.SeedDeployment(proj.ID, "ok-owl-1234", "dev", "running", false, owner.ID, 4002, "k")

	h.Docker.StatusFn = func(_ context.Context, _ string) (string, error) {
		return "running", nil
	}

	w := &health.Worker{
		DB:     h.DB,
		Docker: h.Docker,
		Config: health.Config{Interval: time.Hour, StatusTimeout: 2 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	w.Run(ctx) // blocks until ctx done; immediate sweep happens then idle

	s, err := health.LookupRow(context.Background(), h.DB, id)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if s != "running" {
		t.Fatalf("expected status=running, got %q", s)
	}
}
