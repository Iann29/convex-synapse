package synapsetest

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dockerprov "github.com/Iann29/synapse/internal/docker"
	"github.com/Iann29/synapse/internal/provisioner"
)

// randDeploymentName returns a unique name for concurrent tests.
func randDeploymentName() string {
	return "rand-" + randHex(4) + "-1234"
}

// One worker, one job: Enqueue → worker dequeues → Provision called → done.
func TestProvisioner_EnqueueAndExecute(t *testing.T) {
	h := Setup(t)

	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "PQ Co")
	project := createProject(t, h, owner.AccessToken, team.Slug, "App")
	depID := h.SeedDeployment(project.ID, "pq-cat-1234", "dev",
		"provisioning", false, owner.ID, 4100, "k")

	if err := provisioner.Enqueue(context.Background(), h.DB, depID, false); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// The harness already started a worker. Poll for the job's Provision
	// call to land on FakeDocker.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.Docker.Provisioned) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(h.Docker.Provisioned) == 0 {
		t.Fatalf("Docker.Provision was never called")
	}
	if h.Docker.Provisioned[0].Name != "pq-cat-1234" {
		t.Errorf("Provision spec name: got %q want pq-cat-1234", h.Docker.Provisioned[0].Name)
	}

	// Verify deployment row flipped to running and job to done.
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		_ = h.DB.QueryRow(context.Background(),
			`SELECT status FROM deployments WHERE id = $1`, depID).Scan(&status)
		if status == "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var depStatus, jobStatus string
	_ = h.DB.QueryRow(context.Background(),
		`SELECT status FROM deployments WHERE id = $1`, depID).Scan(&depStatus)
	_ = h.DB.QueryRow(context.Background(),
		`SELECT status FROM provisioning_jobs WHERE deployment_id = $1`, depID).Scan(&jobStatus)
	if depStatus != "running" {
		t.Errorf("deployment status: got %q want running", depStatus)
	}
	if jobStatus != "done" {
		t.Errorf("job status: got %q want done", jobStatus)
	}
}

// Provision returns an error: deployment row marked failed, job marked failed
// with the error message captured.
func TestProvisioner_FailureMarksRowAndJob(t *testing.T) {
	h := Setup(t)

	h.Docker.ProvisionFn = func(_ context.Context, _ dockerprov.DeploymentSpec) (*dockerprov.DeploymentInfo, error) {
		return nil, errBoom
	}

	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Fail Co")
	project := createProject(t, h, owner.AccessToken, team.Slug, "App")
	depID := h.SeedDeployment(project.ID, "fl-fox-1234", "dev",
		"provisioning", false, owner.ID, 4101, "k")

	if err := provisioner.Enqueue(context.Background(), h.DB, depID, false); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Wait for both rows to settle.
	deadline := time.Now().Add(3 * time.Second)
	var depStatus, jobStatus string
	for time.Now().Before(deadline) {
		_ = h.DB.QueryRow(context.Background(),
			`SELECT status FROM deployments WHERE id = $1`, depID).Scan(&depStatus)
		_ = h.DB.QueryRow(context.Background(),
			`SELECT status FROM provisioning_jobs WHERE deployment_id = $1`, depID).Scan(&jobStatus)
		if depStatus == "failed" && jobStatus == "failed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if depStatus != "failed" {
		t.Errorf("deployment status: got %q want failed", depStatus)
	}
	if jobStatus != "failed" {
		t.Errorf("job status: got %q want failed", jobStatus)
	}

	var jobErr string
	_ = h.DB.QueryRow(context.Background(),
		`SELECT COALESCE(error, '') FROM provisioning_jobs WHERE deployment_id = $1`, depID).Scan(&jobErr)
	if jobErr == "" {
		t.Errorf("expected job.error to be populated")
	}
}

// Two workers concurrently enqueued with N jobs — exactly N Provision calls
// total, no doubles. Validates SELECT FOR UPDATE SKIP LOCKED.
func TestProvisioner_NoDoubleClaims(t *testing.T) {
	h := Setup(t)

	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Race Co")
	project := createProject(t, h, owner.AccessToken, team.Slug, "App")

	// Slow Provision so concurrent claims have time to overlap. Counter
	// tracks distinct deployments that hit the docker fake.
	var provisionedIDs sync.Map
	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32
	h.Docker.ProvisionFn = func(_ context.Context, spec dockerprov.DeploymentSpec) (*dockerprov.DeploymentInfo, error) {
		cur := concurrent.Add(1)
		defer concurrent.Add(-1)
		for {
			m := maxConcurrent.Load()
			if cur <= m || maxConcurrent.CompareAndSwap(m, cur) {
				break
			}
		}
		provisionedIDs.Store(spec.Name, true)
		time.Sleep(80 * time.Millisecond)
		return &dockerprov.DeploymentInfo{
			ContainerID:   "fake-" + spec.Name,
			HostPort:      spec.HostPort,
			DeploymentURL: "http://test",
		}, nil
	}

	// Spawn an extra worker so we have 2 racing the queue.
	extraCtx, extraCancel := context.WithCancel(context.Background())
	defer extraCancel()
	extra := &provisioner.Worker{
		DB:     h.DB,
		Docker: h.Docker,
		Config: provisioner.Config{
			PollInterval: 30 * time.Millisecond,
			JobTimeout:   30 * time.Second,
			NodeID:       "extra-worker",
		},
		Logger: slog.Default(),
	}
	go extra.Run(extraCtx)

	const N = 6
	for i := 0; i < N; i++ {
		// Distinct names + ports so allocator constraints don't collide.
		depID := h.SeedDeployment(project.ID, randDeploymentName(), "dev",
			"provisioning", false, owner.ID, 4200+i, "k")
		if err := provisioner.Enqueue(context.Background(), h.DB, depID, false); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	// Wait for everything to drain.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var pending int
		_ = h.DB.QueryRow(context.Background(),
			`SELECT count(*) FROM provisioning_jobs WHERE status IN ('pending','claimed')`).Scan(&pending)
		if pending == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	count := 0
	provisionedIDs.Range(func(_, _ any) bool { count++; return true })
	if count != N {
		t.Errorf("expected %d distinct provisioned deployments, got %d", N, count)
	}

	// Each Provision was called for a unique deployment — Provisioned
	// shouldn't have duplicate names.
	seen := map[string]int{}
	for _, p := range h.Docker.Provisioned {
		seen[p.Name]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("Provision called %d times for %q", n, name)
		}
	}
}

// requeueStale: a job left in 'claimed' with a very old claimed_at gets
// reset to 'pending' on worker startup.
func TestProvisioner_RecoveryRequeuesStaleJobs(t *testing.T) {
	h := Setup(t)

	owner := h.RegisterRandomUser()
	team := createTeam(t, h, owner.AccessToken, "Stale Co")
	project := createProject(t, h, owner.AccessToken, team.Slug, "App")
	depID := h.SeedDeployment(project.ID, "stale-bear-9999", "dev",
		"provisioning", false, owner.ID, 4300, "k")

	// Insert a 'claimed' job with claimed_at far in the past.
	if _, err := h.DB.Exec(context.Background(), `
		INSERT INTO provisioning_jobs (deployment_id, kind, status, claimed_by, claimed_at, healthcheck_via_network)
		VALUES ($1, 'provision', 'claimed', 'dead-node', now() - interval '1 hour', false)
	`, depID); err != nil {
		t.Fatalf("seed stale claim: %v", err)
	}

	// Start a fresh worker — its requeueStale step should reset the row
	// to 'pending' then immediately re-claim it.
	freshCtx, freshCancel := context.WithCancel(context.Background())
	defer freshCancel()
	fresh := &provisioner.Worker{
		DB:     h.DB,
		Docker: h.Docker,
		Config: provisioner.Config{
			PollInterval: 30 * time.Millisecond,
			JobTimeout:   30 * time.Second,
			NodeID:       "fresh",
		},
		Logger: slog.Default(),
	}
	go fresh.Run(freshCtx)

	// Eventually the deployment should reach 'running'.
	deadline := time.Now().Add(3 * time.Second)
	var depStatus string
	for time.Now().Before(deadline) {
		_ = h.DB.QueryRow(context.Background(),
			`SELECT status FROM deployments WHERE id = $1`, depID).Scan(&depStatus)
		if depStatus == "running" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if depStatus != "running" {
		t.Errorf("deployment status: got %q want running (recovery should have re-pended + re-claimed)", depStatus)
	}
}
