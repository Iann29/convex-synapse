package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
)

// DeploymentSpec describes everything the provisioner needs to create one
// Convex backend container.
//
// Single-replica (the v0.4-and-earlier shape): leave ReplicaIndex zero,
// HAReplica false, Storage nil. Container name stays "convex-{Name}",
// volume "synapse-data-{Name}", env vars are the existing four plus any
// EnvVars overrides — exactly what existed before v0.5.
//
// HA-enabled (v0.5+): set HAReplica=true and ReplicaIndex per replica;
// the container name picks up a "-{idx}" suffix. Set Storage to point at
// the per-deployment Postgres database + S3 buckets that both replicas
// share — the volume mount is dropped (state lives in Postgres + S3).
type DeploymentSpec struct {
	Name           string            // friendly name, used for container suffix and INSTANCE_NAME
	InstanceSecret string            // hex-encoded secret
	HostPort       int               // host port mapped to the container's 3210
	EnvVars        map[string]string // additional env, applied last (overrides defaults)
	// HealthcheckViaNetwork picks the URL the provisioner polls while waiting
	// for the backend to become healthy. See config.HealthcheckViaNetwork.
	HealthcheckViaNetwork bool

	// ReplicaIndex is the position of this replica within the deployment
	// (0, 1, …). Ignored unless HAReplica=true.
	ReplicaIndex int

	// HAReplica turns on the "-{ReplicaIndex}" suffix on container and
	// volume names. Single-replica deployments leave this false so that
	// existing containers, dashboards, and tests don't see the rename.
	HAReplica bool

	// Storage, if non-nil, switches the backend to Postgres + S3. The
	// container gets POSTGRES_URL plus the AWS_/S3_STORAGE_* env vars
	// the upstream image expects, and the SQLite data volume mount is
	// dropped. Nil = SQLite + local volume (existing behavior).
	Storage *StorageEnv
}

// StorageEnv carries the per-deployment Postgres + S3 configuration.
// Plaintext only — encryption-at-rest happens in internal/crypto before
// these values are persisted; they're decrypted here for use as env
// vars on the container we're about to create.
type StorageEnv struct {
	// Postgres URL the backend writes its tables to. Includes credentials.
	PostgresURL string
	// Set true when the Postgres URL uses http (e.g. local dev with no TLS).
	// Maps to the upstream backend's DO_NOT_REQUIRE_SSL=1 env var.
	DoNotRequireSSL bool

	// S3 (or S3-compatible like MinIO) connection material. The backend
	// writes file/module/search/export/snapshot blobs to these buckets.
	S3Endpoint      string
	S3Region        string
	S3AccessKey     string
	S3SecretKey     string
	BucketFiles     string
	BucketModules   string
	BucketSearch    string
	BucketExports   string
	BucketSnapshots string
}

// DeploymentInfo is what the provisioner returns once a container is up.
type DeploymentInfo struct {
	ContainerID   string
	HostPort      int
	DeploymentURL string
}

// SnapshotMigrationSpec describes the Convex backup/restore hop used when a
// SQLite single-replica deployment is upgraded to HA. The docker client runs
// the official Convex CLI in a throwaway container on the Synapse network so
// it can reach both the old single-replica backend and the new HA replicas by
// container DNS name.
type SnapshotMigrationSpec struct {
	DeploymentName string
	SourceURL      string
	SourceAdminKey string
	TargetURLs     []string
	TargetAdminKey string
}

// ContainerName is the deterministic Docker container name for a given
// deployment + replica. Exported so the proxy resolver can build the
// in-network address (`http://convex-{name}-{idx}:3210`) without
// duplicating the format string.
func ContainerName(deploymentName string, replicaIndex int, ha bool) string {
	if !ha {
		return "convex-" + deploymentName
	}
	return fmt.Sprintf("convex-%s-%d", deploymentName, replicaIndex)
}

// VolumeName returns the docker volume that holds a replica's SQLite
// data. HA deployments backed by Postgres + S3 should not have a
// volume mount at all — callers check `spec.Storage == nil` before
// calling this.
func VolumeName(deploymentName string, replicaIndex int, ha bool) string {
	if !ha {
		return "synapse-data-" + deploymentName
	}
	return fmt.Sprintf("synapse-data-%s-%d", deploymentName, replicaIndex)
}

// containerName / volumeName remain as the internal short forms for the
// pre-v0.5 single-replica path. They delegate to the exported helpers so
// any future change to the naming scheme has a single source of truth.
func containerName(deploymentName string) string {
	return ContainerName(deploymentName, 0, false)
}

func volumeName(deploymentName string) string {
	return VolumeName(deploymentName, 0, false)
}

// EnsureImage pulls the backend image if it is not already present locally.
// Pulling at provision time would add seconds to every create_deployment;
// callers should call this once at startup OR best-effort on first use.
func (c *Client) EnsureImage(ctx context.Context) error {
	return c.ensureImage(ctx, c.BackendImage)
}

func (c *Client) ensureImage(ctx context.Context, imageName string) error {
	images, err := c.api.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return fmt.Errorf("list images: %w", err)
	}
	for _, img := range images {
		for _, tag := range img.RepoTags {
			if tag == imageName {
				return nil
			}
		}
	}
	c.logger.Info("pulling docker image", "image", imageName)
	rc, err := c.api.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", imageName, err)
	}
	defer rc.Close()
	// Drain the response so the pull actually completes.
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("drain pull stream: %w", err)
	}
	return nil
}

// MigrateSnapshot exports a backup from the old single-replica backend and
// imports it into one of the freshly-provisioned HA replicas. Synapse's own
// image is distroless and intentionally has no node/npm, so the CLI runs in a
// short-lived Node container attached to the same Docker network as the
// backends. The archive stays inside that container and is deleted with it.
func (c *Client) MigrateSnapshot(ctx context.Context, spec SnapshotMigrationSpec) error {
	if spec.SourceURL == "" || spec.SourceAdminKey == "" || spec.TargetAdminKey == "" || len(spec.TargetURLs) == 0 {
		return errors.New("snapshot migration: missing source or target credentials")
	}

	const cliImage = "node:22-alpine"
	if err := c.ensureImage(ctx, cliImage); err != nil {
		return err
	}

	targets := make([]string, 0, len(spec.TargetURLs))
	for _, target := range spec.TargetURLs {
		if strings.TrimSpace(target) != "" {
			targets = append(targets, strings.TrimSpace(target))
		}
	}
	if len(targets) == 0 {
		return errors.New("snapshot migration: no target URLs")
	}

	script := `
set -eu
mkdir -p /backup
CONVEX_SELF_HOSTED_URL="$SOURCE_CONVEX_SELF_HOSTED_URL" \
CONVEX_SELF_HOSTED_ADMIN_KEY="$SOURCE_CONVEX_SELF_HOSTED_ADMIN_KEY" \
  npx --yes convex@latest export --path /backup
archive="$(find /backup -maxdepth 1 -type f -name '*.zip' | sort | tail -n 1)"
if [ -z "$archive" ]; then
  echo "convex export completed without producing a zip archive" >&2
  exit 1
fi
import_ok=0
while IFS= read -r target; do
  [ -n "$target" ] || continue
  if CONVEX_SELF_HOSTED_URL="$target" \
     CONVEX_SELF_HOSTED_ADMIN_KEY="$TARGET_CONVEX_SELF_HOSTED_ADMIN_KEY" \
       npx --yes convex@latest import --replace "$archive"; then
    import_ok=1
    break
  fi
done <<'TARGET_URLS_EOF'
` + strings.Join(targets, "\n") + `
TARGET_URLS_EOF
if [ "$import_ok" != "1" ]; then
  echo "convex import failed for every HA target replica" >&2
  exit 1
fi
`

	containerName := fmt.Sprintf("synapse-upgrade-%s-%d", spec.DeploymentName, time.Now().UnixNano())
	resp, err := c.api.ContainerCreate(ctx, &container.Config{
		Image: cliImage,
		Cmd:   []string{"sh", "-lc", script},
		Env: []string{
			"SOURCE_CONVEX_SELF_HOSTED_URL=" + spec.SourceURL,
			"SOURCE_CONVEX_SELF_HOSTED_ADMIN_KEY=" + spec.SourceAdminKey,
			"TARGET_CONVEX_SELF_HOSTED_ADMIN_KEY=" + spec.TargetAdminKey,
			"CONVEX_DISABLE_ANALYTICS=1",
		},
		Labels: map[string]string{
			"synapse.managed":    "true",
			"synapse.task":       "upgrade_to_ha",
			"synapse.deployment": spec.DeploymentName,
		},
	}, &container.HostConfig{}, &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			c.Network: {},
		},
	}, nil, containerName)
	if err != nil {
		return fmt.Errorf("create migration container: %w", err)
	}
	defer func() {
		_ = c.api.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
	}()

	if err := c.api.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start migration container: %w", err)
	}

	statusCh, errCh := c.api.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("wait migration container: %w", err)
		}
	case status := <-statusCh:
		if status.StatusCode != 0 {
			return fmt.Errorf("snapshot migration failed: %s", c.migrationLogs(ctx, resp.ID, spec))
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func (c *Client) migrationLogs(ctx context.Context, containerID string, spec SnapshotMigrationSpec) string {
	rc, err := c.api.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       "80",
	})
	if err != nil {
		return "could not read migration logs: " + err.Error()
	}
	defer rc.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, rc)
	out := buf.String()
	out = strings.ReplaceAll(out, spec.SourceAdminKey, "[redacted]")
	out = strings.ReplaceAll(out, spec.TargetAdminKey, "[redacted]")
	if len(out) > 4000 {
		out = out[len(out)-4000:]
	}
	return strings.TrimSpace(out)
}

// Provision creates and starts a Convex backend container per spec.
// On failure, it best-effort removes any partially-created container so the
// caller can retry without leaking resources.
func (c *Client) Provision(ctx context.Context, spec DeploymentSpec) (*DeploymentInfo, error) {
	if spec.Name == "" || spec.InstanceSecret == "" || spec.HostPort == 0 {
		return nil, errors.New("provision: name, instance secret, and host port required")
	}

	// Best-effort: pull image if missing. Skip on error so a misconfigured
	// network doesn't block deployments that already have the image cached.
	if err := c.EnsureImage(ctx); err != nil {
		c.logger.Warn("ensure image failed, will try to create anyway", "err", err)
	}

	cName := ContainerName(spec.Name, spec.ReplicaIndex, spec.HAReplica)

	containerPort := nat.Port("3210/tcp")
	hostBinding := nat.PortBinding{HostIP: "0.0.0.0", HostPort: strconv.Itoa(spec.HostPort)}

	cloudOrigin := fmt.Sprintf("http://127.0.0.1:%d", spec.HostPort)
	env := []string{
		"INSTANCE_NAME=" + spec.Name,
		"INSTANCE_SECRET=" + spec.InstanceSecret,
		"CONVEX_CLOUD_ORIGIN=" + cloudOrigin,
		"CONVEX_SITE_ORIGIN=" + cloudOrigin,
	}

	// HA storage: append the env vars the upstream backend reads when
	// run in Postgres + S3 mode. The presence of POSTGRES_URL alone
	// switches the backend off SQLite (see self-hosted/docker/
	// docker-compose.yml of get-convex/convex-backend).
	if spec.Storage != nil {
		s := spec.Storage
		env = append(env,
			"POSTGRES_URL="+s.PostgresURL,
			"S3_ENDPOINT_URL="+s.S3Endpoint,
			"AWS_REGION="+s.S3Region,
			"AWS_ACCESS_KEY_ID="+s.S3AccessKey,
			"AWS_SECRET_ACCESS_KEY="+s.S3SecretKey,
			"S3_STORAGE_FILES_BUCKET="+s.BucketFiles,
			"S3_STORAGE_MODULES_BUCKET="+s.BucketModules,
			"S3_STORAGE_SEARCH_BUCKET="+s.BucketSearch,
			"S3_STORAGE_EXPORTS_BUCKET="+s.BucketExports,
			"S3_STORAGE_SNAPSHOT_IMPORTS_BUCKET="+s.BucketSnapshots,
		)
		if s.DoNotRequireSSL {
			env = append(env, "DO_NOT_REQUIRE_SSL=true")
		}
	}

	for k, v := range spec.EnvVars {
		env = append(env, k+"="+v)
	}

	labels := map[string]string{
		"synapse.managed":    "true",
		"synapse.deployment": spec.Name,
	}
	if spec.HAReplica {
		labels["synapse.replica_index"] = strconv.Itoa(spec.ReplicaIndex)
	}

	cfg := &container.Config{
		Image:        c.BackendImage,
		Env:          env,
		Labels:       labels,
		ExposedPorts: nat.PortSet{containerPort: struct{}{}},
	}
	hostCfg := &container.HostConfig{
		PortBindings: nat.PortMap{containerPort: []nat.PortBinding{hostBinding}},
		RestartPolicy: container.RestartPolicy{
			Name:              container.RestartPolicyUnlessStopped,
			MaximumRetryCount: 0,
		},
	}
	// SQLite path → mount a per-replica data volume. Postgres + S3 path
	// (Storage != nil) keeps everything in shared storage, so no volume
	// mount; the container is fully ephemeral.
	if spec.Storage == nil {
		hostCfg.Binds = []string{
			VolumeName(spec.Name, spec.ReplicaIndex, spec.HAReplica) + ":/convex/data",
		}
	}
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			c.Network: {},
		},
	}

	resp, err := c.api.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, cName)
	if err != nil {
		return nil, fmt.Errorf("create container: %w", err)
	}

	if err := c.api.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = c.api.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("start container: %w", err)
	}

	info := &DeploymentInfo{
		ContainerID:   resp.ID,
		HostPort:      spec.HostPort,
		DeploymentURL: cloudOrigin,
	}

	// The DB-stored DeploymentURL is what API consumers see — always the
	// host-port mapping. The internal URL is what THIS process polls.
	healthURL := cloudOrigin
	if spec.HealthcheckViaNetwork {
		healthURL = fmt.Sprintf("http://%s:3210", cName)
	}
	if err := c.waitHealthy(ctx, healthURL, 60*time.Second); err != nil {
		// Leave the container running — operator can inspect it. Caller can
		// flip status to "failed" while keeping it around for diagnosis.
		c.logger.Warn("deployment did not become healthy in time",
			"name", spec.Name, "err", err)
	}

	return info, nil
}

// waitHealthy polls the deployment's HTTP endpoint until it returns any
// response (not a connection refused / EOF). The Convex backend exposes
// /version which is a quick smoke check.
func (c *Client) waitHealthy(ctx context.Context, baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	httpClient := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := httpClient.Get(baseURL + "/version")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(750 * time.Millisecond):
		}
	}
	return errors.New("deployment did not become healthy before timeout")
}

// Destroy stops and removes the deployment's container and its data
// volume. Idempotent — missing container or volume is treated as
// success. Single-replica path (no -idx suffix); see DestroyReplica
// for HA deployments.
func (c *Client) Destroy(ctx context.Context, deploymentName string) error {
	return c.destroy(ctx, containerName(deploymentName), volumeName(deploymentName), true)
}

// Stop halts the legacy single-replica container without removing it or its
// SQLite volume. upgrade_to_ha uses this after the snapshot import succeeds:
// the HA replicas become the live backends while the old container remains on
// disk for operator inspection or manual rollback.
func (c *Client) Stop(ctx context.Context, deploymentName string) error {
	timeout := 10
	if err := c.api.ContainerStop(ctx, containerName(deploymentName), container.StopOptions{Timeout: &timeout}); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("stop container %s: %w", containerName(deploymentName), err)
	}
	return nil
}

// DestroyReplica removes a single replica's container + (optional) volume.
// HA deployments backed by Postgres + S3 should pass keepVolume=true
// because there is no per-replica volume to clean up — Synapse never
// created one. SQLite-backed HA deployments keep a per-replica volume
// and pass keepVolume=false to clear it on delete.
func (c *Client) DestroyReplica(ctx context.Context, deploymentName string, replicaIndex int, keepVolume bool) error {
	cName := ContainerName(deploymentName, replicaIndex, true)
	vName := ""
	if !keepVolume {
		vName = VolumeName(deploymentName, replicaIndex, true)
	}
	return c.destroy(ctx, cName, vName, !keepVolume)
}

func (c *Client) destroy(ctx context.Context, cName, vName string, removeVolume bool) error {
	timeout := 10
	_ = c.api.ContainerStop(ctx, cName, container.StopOptions{Timeout: &timeout})
	if err := c.api.ContainerRemove(ctx, cName, container.RemoveOptions{Force: true, RemoveVolumes: false}); err != nil {
		if !isNotFound(err) {
			return fmt.Errorf("remove container %s: %w", cName, err)
		}
	}
	if removeVolume && vName != "" {
		if err := c.api.VolumeRemove(ctx, vName, true); err != nil && !isNotFound(err) {
			return fmt.Errorf("remove volume: %w", err)
		}
	}
	return nil
}

// Restart starts a stopped Convex backend container without touching the
// data volume. Useful for recovery flows where the container exited or was
// killed (OOM, host reboot) but the row is still in the DB.
//
// Returns "container not found" as a sentinel `errNotFound` so the caller
// can decide whether to re-provision from scratch.
func (c *Client) Restart(ctx context.Context, deploymentName string) error {
	return c.restart(ctx, containerName(deploymentName))
}

// Recreate stops + removes the existing container (keeping the data
// volume so SQLite contents survive) and re-Provisions with the
// supplied spec. Used by the custom-domains flow to apply a refreshed
// CORS_ALLOWED_ORIGINS env without resetting backend state.
//
// Single-replica path only (spec.HAReplica must be false). HA
// deployments would need per-replica orchestration that the v1.1
// custom-domains flow deliberately ducks.
//
// On the wire this is ~15s of downtime per deployment (stop + start +
// healthcheck). Callers should advertise that to the operator before
// triggering — see DomainsHandler.deploymentRestartTriggered.
func (c *Client) Recreate(ctx context.Context, spec DeploymentSpec) (*DeploymentInfo, error) {
	if spec.HAReplica {
		return nil, fmt.Errorf("recreate: HA replicas not supported in this path")
	}
	cName := containerName(spec.Name)
	// Same shape as destroy() but always keep the volume.
	timeout := 10
	_ = c.api.ContainerStop(ctx, cName, container.StopOptions{Timeout: &timeout})
	if err := c.api.ContainerRemove(ctx, cName, container.RemoveOptions{Force: true, RemoveVolumes: false}); err != nil {
		if !isNotFound(err) {
			return nil, fmt.Errorf("remove container %s: %w", cName, err)
		}
	}
	return c.Provision(ctx, spec)
}

// RecreateReplica is the HA-aware variant of Recreate. It targets one
// replica container by index and leaves any volume behind; normal HA
// deployments keep state in Postgres + S3, and preserving a volume is the
// least surprising fallback for any legacy HA row without Storage.
func (c *Client) RecreateReplica(ctx context.Context, spec DeploymentSpec) (*DeploymentInfo, error) {
	if !spec.HAReplica {
		return nil, fmt.Errorf("recreate replica: HAReplica must be true")
	}
	cName := ContainerName(spec.Name, spec.ReplicaIndex, true)
	timeout := 10
	_ = c.api.ContainerStop(ctx, cName, container.StopOptions{Timeout: &timeout})
	if err := c.api.ContainerRemove(ctx, cName, container.RemoveOptions{Force: true, RemoveVolumes: false}); err != nil {
		if !isNotFound(err) {
			return nil, fmt.Errorf("remove container %s: %w", cName, err)
		}
	}
	return c.Provision(ctx, spec)
}

// RestartReplica is the HA-aware variant of Restart, addressing one
// replica by index.
func (c *Client) RestartReplica(ctx context.Context, deploymentName string, replicaIndex int) error {
	return c.restart(ctx, ContainerName(deploymentName, replicaIndex, true))
}

func (c *Client) restart(ctx context.Context, cName string) error {
	if err := c.api.ContainerStart(ctx, cName, container.StartOptions{}); err != nil {
		if isNotFound(err) {
			return ErrContainerNotFound
		}
		return fmt.Errorf("start container %s: %w", cName, err)
	}
	return nil
}

// ErrContainerNotFound is returned by Restart when the container has been
// removed (e.g. the operator manually `docker rm`'d it). Callers should
// treat this as a signal to re-provision rather than retry the restart.
var ErrContainerNotFound = errNotFound{}

type errNotFound struct{}

func (errNotFound) Error() string { return "container not found" }

// Status reports the docker-side status of a deployment's primary
// container (single-replica path). Returns ("", nil) if the container
// does not exist.
func (c *Client) Status(ctx context.Context, deploymentName string) (string, error) {
	return c.status(ctx, containerName(deploymentName))
}

// StatusReplica is the HA-aware variant of Status, addressing one
// replica by index.
func (c *Client) StatusReplica(ctx context.Context, deploymentName string, replicaIndex int) (string, error) {
	return c.status(ctx, ContainerName(deploymentName, replicaIndex, true))
}

func (c *Client) status(ctx context.Context, cName string) (string, error) {
	insp, err := c.api.ContainerInspect(ctx, cName)
	if isNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return insp.State.Status, nil
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	type notFound interface{ NotFound() bool }
	if nf, ok := err.(notFound); ok && nf.NotFound() {
		return true
	}
	// Fall back to the legacy error string check.
	msg := err.Error()
	return contains(msg, "No such container") ||
		contains(msg, "No such volume") ||
		contains(msg, "no such image")
}

func contains(s, substr string) bool {
	return len(substr) == 0 || (len(s) >= len(substr) && indexOf(s, substr) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
