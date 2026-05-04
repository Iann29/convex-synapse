package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
)

// AsterImageTag pins the Aster broker/cell pair Synapse expects.
// Both images come from the same Aster workspace build; keeping one tag
// avoids accidentally pairing a broker and cell built from different IPC.
const AsterImageTag = "0.4"

// AsterBrokerImage is the default image tag the provisioner uses when
// `spec.AsterImage` is empty. Operators can override per-deployment via
// the spec field, or globally via the synapse process config (TODO:
// wire SYNAPSE_ASTER_BROKER_IMAGE through Config in the next slice).
const AsterBrokerImage = "aster-brokerd:" + AsterImageTag

// AsterCellImage is the v8cell counterpart used by InvokeAsterCell.
// Same versioning story as AsterBrokerImage.
const AsterCellImage = "aster-v8cell:" + AsterImageTag

// AsterContainerName returns the deterministic Docker container name
// for an Aster brokerd instance backing a deployment. Mirrors the
// Convex `convex-{name}` pattern so operators can grep one rule for
// both kinds.
func AsterContainerName(deploymentName string) string {
	return "aster-broker-" + deploymentName
}

// AsterVolumeName returns the docker volume that holds the brokerd's
// Unix-domain socket. The socket lives inside the volume so a sibling
// cell container (mounted with the same volume read-write) can connect
// without the broker process exposing a TCP port.
func AsterVolumeName(deploymentName string) string {
	return "synapse-aster-" + deploymentName
}

// provisionAster creates and starts an aster-brokerd container for one
// deployment. It is the kind=aster branch of Provision.
//
// Differences from the Convex path:
//   - No host port, no exposed TCP port. Communication is Unix-domain
//     socket only, mounted via a per-deployment Docker volume.
//   - No SQLite volume / no Postgres+S3 env vars. The broker holds the
//     seal key, not a database connection (yet — Postgres integration
//     is the next slice).
//   - Image is `aster-brokerd:<tag>` (not the Convex backend).
//   - Health check is just "container is running"; the broker prints a
//     ready line on stderr but doesn't expose an HTTP endpoint.
func (c *Client) provisionAster(ctx context.Context, spec DeploymentSpec) (*DeploymentInfo, error) {
	if spec.Name == "" || spec.InstanceSecret == "" {
		return nil, errors.New("provision aster: name and instance secret required")
	}
	if spec.HAReplica {
		return nil, errors.New("provision aster: HA replicas not supported")
	}
	if spec.Storage != nil {
		return nil, errors.New("provision aster: storage env not supported (broker owns DB authority directly)")
	}

	imageRef := spec.AsterImage
	if imageRef == "" {
		imageRef = AsterBrokerImage
	}

	cName := AsterContainerName(spec.Name)
	volName := AsterVolumeName(spec.Name)

	// Ensure the Docker volume exists. Idempotent: a `volume create`
	// for an existing name is a no-op on the daemon side.
	if _, err := c.api.VolumeCreate(ctx, volume.CreateOptions{
		Name: volName,
		Labels: map[string]string{
			"synapse.managed":    "true",
			"synapse.deployment": spec.Name,
			"synapse.kind":       "aster",
		},
	}); err != nil {
		return nil, fmt.Errorf("create aster volume: %w", err)
	}

	// The instance secret doubles as the BLAKE3 seal-key seed for the
	// broker. v0.3 uses CapsuleSealKey::derive_for_tests on it; in
	// production this becomes a separately-rotated KMS-backed key.
	env := []string{
		"ASTER_BROKER_SOCK=/run/aster/broker.sock",
		"ASTER_TENANT=" + spec.Name,
		"ASTER_DEPLOYMENT=" + spec.Name,
		"ASTER_SEAL_SEED=" + spec.InstanceSecret,
		// Empty seeds — the broker boots with no documents yet. Once
		// the Postgres integration lands, this env var goes away and
		// the broker reads MVCC at request time.
		"ASTER_SEED_I64=",
		"ASTER_SNAPSHOT_TS=0",
	}
	for k, v := range spec.EnvVars {
		env = append(env, k+"="+v)
	}

	labels := map[string]string{
		"synapse.managed":    "true",
		"synapse.deployment": spec.Name,
		"synapse.kind":       "aster",
		"synapse.role":       "broker",
	}

	cfg := &container.Config{
		Image:  imageRef,
		Env:    env,
		Labels: labels,
	}
	hostCfg := &container.HostConfig{
		Binds: []string{
			volName + ":/run/aster",
		},
		RestartPolicy: container.RestartPolicy{
			Name:              container.RestartPolicyUnlessStopped,
			MaximumRetryCount: 0,
		},
	}
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			c.Network: {},
		},
	}

	resp, err := c.api.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, cName)
	if err != nil {
		return nil, fmt.Errorf("create aster container: %w", err)
	}
	if err := c.api.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = c.api.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("start aster container: %w", err)
	}

	// kind=aster has no HTTP health endpoint to poll. The cell talks to
	// the broker over the shared volume's UDS, which only becomes
	// reachable once the broker's accept loop starts. We trust Docker's
	// container state machine here; misbehaviour will surface in the
	// first invocation as a connect error and bubble up via metrics
	// (TODO: emit metric when that path lands).

	return &DeploymentInfo{
		ContainerID:   resp.ID,
		HostPort:      0,  // No TCP listener — UDS only.
		DeploymentURL: "", // No HTTP URL — proxy decides what to surface.
	}, nil
}

// DestroyAster removes the brokerd container + its volume for a
// kind=aster deployment. Best-effort: if the container is already
// gone we still try the volume; if both are gone we report success.
func (c *Client) DestroyAster(ctx context.Context, deploymentName string) error {
	cName := AsterContainerName(deploymentName)
	volName := AsterVolumeName(deploymentName)

	if err := c.api.ContainerRemove(ctx, cName, container.RemoveOptions{Force: true}); err != nil {
		// "No such container" is fine — the API returns a 404-shaped
		// error string. Anything else is a real failure.
		if !isNotFound(err) {
			return fmt.Errorf("remove aster container %s: %w", cName, err)
		}
	}
	if err := c.api.VolumeRemove(ctx, volName, true); err != nil && !isNotFound(err) {
		return fmt.Errorf("remove aster volume %s: %w", volName, err)
	}
	return nil
}

// StatusAster reports the docker-side status of the brokerd container
// for a kind=aster deployment. Returns ("", nil) if it doesn't exist.
func (c *Client) StatusAster(ctx context.Context, deploymentName string) (string, error) {
	return c.status(ctx, AsterContainerName(deploymentName))
}

// InvokeAsterRequest is the operator-facing payload for a one-shot
// cell invocation. The cell runs the JS source against the matching
// brokerd's UDS, prints a JSON envelope on stdout, then exits.
//
// Capability boundary: the cell never touches Postgres or any other
// resource — it gets the broker socket path and that's it. The seal
// seed must match the brokerd's (otherwise the capsule the broker
// returns won't decrypt and the cell errors out).
type InvokeAsterRequest struct {
	// DeploymentName ties the cell to its sibling brokerd. The cell
	// mounts the same Docker volume so it can find the UDS file.
	// Required.
	DeploymentName string

	// InstanceSecret is reused as the BLAKE3 seal seed. Must match the
	// brokerd's `ASTER_SEAL_SEED`; mismatched seeds produce a wire-
	// shape error before any user code runs. Required.
	InstanceSecret string

	// JSSource is the literal JavaScript the cell executes. Becomes
	// `ASTER_JS_INLINE` (the v8cell binary supports this since
	// Iann29/aster#16). Required.
	JSSource string

	// SnapshotTS pins the read snapshot. 0 means "use whatever the
	// broker reports" — fine for read-only smoke tests, less fine for
	// OCC. Most callers will leave this 0 and let the broker decide.
	SnapshotTS uint64

	// CellID identifies this invocation in the broker's per-cell
	// rate / lease tracking. Defaults to a generated value.
	CellID string

	// LeaseEpoch monotonically increases per cell to invalidate stale
	// capsules. Defaults to 1 — fine for stateless one-shot reads.
	LeaseEpoch uint64

	// Prewarm is a comma-separated list of document IDs (Aster wire
	// form `<table_hex>/<id_hex>` OR Convex IDv6 — both work since
	// PR #12 in Iann29/aster) the broker should hydrate up front.
	// Empty = no prewarm; the cell's own read traps drive hydration.
	Prewarm []string

	// MaxTraps caps the number of read-trap continuations per cell
	// invocation; runaway loops error out instead of pinning a slot.
	// Defaults to 64.
	MaxTraps int

	// Timeout is the host-side deadline for the cell. After this the
	// container is force-removed and the call returns an error. The
	// underlying `context` is the canonical cancellation channel; the
	// timeout is just a sane default. 0 = no extra timeout (the
	// caller's ctx is the only deadline).
	Timeout time.Duration
}

// InvokeAsterResult is what the cell printed + how it exited. Stdout
// is the raw JSON envelope — `{"output":..,"traps":..,"capsule_hash":..}` —
// not parsed here; the API layer parses on its way out.
type InvokeAsterResult struct {
	Stdout   string
	Stderr   string
	ExitCode int64
}

// InvokeAsterCell runs a single cell invocation against the broker
// sibling for `req.DeploymentName`. It is the kind=aster equivalent
// of "send a request to the Convex backend" — except the cell holds
// no DB credentials, only a sealed capsule the broker hands it over
// the UDS.
//
// Lifecycle:
//
//  1. ContainerCreate the cell with the right env + volume mount.
//  2. ContainerStart, then ContainerWait until it stops.
//  3. ContainerLogs to read stdout / stderr.
//  4. ContainerRemove (force) so a half-failed run doesn't leak.
//
// Concurrency: each call creates a fresh container with a unique
// name, so multiple invocations against the same deployment run in
// parallel. The brokerd is the bottleneck — it serialises requests
// per its own concurrency config (default ASTER_MAX_CONNECTIONS=1024).
func (c *Client) InvokeAsterCell(ctx context.Context, req InvokeAsterRequest) (*InvokeAsterResult, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}

	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	cellName := fmt.Sprintf("aster-cell-%s-%d", req.DeploymentName, time.Now().UnixNano())
	volName := AsterVolumeName(req.DeploymentName)

	cellID := req.CellID
	if cellID == "" {
		cellID = fmt.Sprintf("cell-%s-%d", req.DeploymentName, time.Now().UnixNano())
	}
	leaseEpoch := req.LeaseEpoch
	if leaseEpoch == 0 {
		leaseEpoch = 1
	}
	maxTraps := req.MaxTraps
	if maxTraps <= 0 {
		maxTraps = 64
	}
	env := buildAsterCellEnv(req, cellID, leaseEpoch, maxTraps)

	cfg := &container.Config{
		Image: AsterCellImage,
		Env:   env,
		Tty:   false,
		Labels: map[string]string{
			"synapse.managed":    "true",
			"synapse.deployment": req.DeploymentName,
			"synapse.kind":       "aster",
			"synapse.role":       "cell",
		},
		AttachStdout: true,
		AttachStderr: true,
	}
	hostCfg := &container.HostConfig{
		Binds: []string{
			volName + ":/run/aster",
		},
		// The cell must NOT auto-remove — we ContainerRemove ourselves
		// after collecting logs (mirrors the admin_key flow). Auto-
		// remove is racy with ContainerLogs on short-lived runs.
		AutoRemove: false,
	}
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			c.Network: {},
		},
	}

	resp, err := c.api.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, cellName)
	if err != nil {
		return nil, fmt.Errorf("create aster cell container: %w", err)
	}
	id := resp.ID
	defer func() {
		// Best-effort cleanup — using context.Background so we still
		// remove on a cancelled parent ctx.
		_ = c.api.ContainerRemove(context.Background(), id, container.RemoveOptions{Force: true})
	}()

	if err := c.api.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("start aster cell: %w", err)
	}

	statusCh, errCh := c.api.ContainerWait(ctx, id, container.WaitConditionNotRunning)
	var exitCode int64
	select {
	case waitErr := <-errCh:
		if waitErr != nil {
			return nil, fmt.Errorf("wait aster cell: %w", waitErr)
		}
	case status := <-statusCh:
		exitCode = status.StatusCode
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Collect logs in a SEPARATE call from wait — Docker's combined
	// stream framing makes interleaved reads brittle. Both stdout +
	// stderr come back, multiplexed under the 8-byte header that
	// stripDockerStreamHeaders peels off.
	logsReader, err := c.api.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: false,
	})
	if err != nil {
		return nil, fmt.Errorf("read aster cell stdout: %w", err)
	}
	stdout, err := io.ReadAll(logsReader)
	_ = logsReader.Close()
	if err != nil {
		return nil, fmt.Errorf("drain aster cell stdout: %w", err)
	}
	stdoutClean := stripDockerStreamHeaders(stdout)

	stderrReader, err := c.api.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: false,
		ShowStderr: true,
	})
	if err != nil {
		return nil, fmt.Errorf("read aster cell stderr: %w", err)
	}
	stderr, err := io.ReadAll(stderrReader)
	_ = stderrReader.Close()
	if err != nil {
		return nil, fmt.Errorf("drain aster cell stderr: %w", err)
	}
	stderrClean := stripDockerStreamHeaders(stderr)

	return &InvokeAsterResult{
		Stdout:   string(stdoutClean),
		Stderr:   string(stderrClean),
		ExitCode: exitCode,
	}, nil
}

func buildAsterCellEnv(req InvokeAsterRequest, cellID string, leaseEpoch uint64, maxTraps int) []string {
	prewarm := strings.Join(req.Prewarm, ",")
	return []string{
		"ASTER_BROKER_SOCK=/run/aster/broker.sock",
		"ASTER_TENANT=" + req.DeploymentName,
		"ASTER_DEPLOYMENT=" + req.DeploymentName,
		"ASTER_SEAL_SEED=" + req.InstanceSecret,
		fmt.Sprintf("ASTER_SNAPSHOT_TS=%d", req.SnapshotTS),
		"ASTER_CELL_ID=" + cellID,
		fmt.Sprintf("ASTER_LEASE_EPOCH=%d", leaseEpoch),
		"ASTER_PREWARM=" + prewarm,
		fmt.Sprintf("ASTER_MAX_TRAPS=%d", maxTraps),
		// Clear any file-based image default: ASTER_JS and
		// ASTER_JS_INLINE are mutually exclusive in aster_v8cell.
		"ASTER_JS=",
		// The whole point of this slice — the cell binary now reads
		// the JS source from this env (Iann29/aster#16).
		"ASTER_JS_INLINE=" + req.JSSource,
	}
}

func (req *InvokeAsterRequest) validate() error {
	if req.DeploymentName == "" {
		return errors.New("invoke aster: deployment name required")
	}
	if req.InstanceSecret == "" {
		return errors.New("invoke aster: instance secret required (must match brokerd seal seed)")
	}
	if req.JSSource == "" {
		return errors.New("invoke aster: JSSource required")
	}
	// Hard cap on inline source size. Docker enforces a per-arg env
	// var limit (typically ~128 KiB per env, ~2 MiB total per
	// container) so a giant bundle would surface as a confusing
	// "argument list too long" deep in the SDK. Reject early with a
	// clear message; module-loader (#98) is the answer for big code.
	if len(req.JSSource) > 64*1024 {
		return fmt.Errorf("invoke aster: JSSource too large (%d bytes, max 65536) — use the module loader for full bundles", len(req.JSSource))
	}
	return nil
}
