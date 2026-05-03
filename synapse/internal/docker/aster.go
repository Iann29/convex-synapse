package docker

import (
	"context"
	"errors"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
)

// AsterBrokerImage is the default image tag the provisioner uses when
// `spec.AsterImage` is empty. Operators can override per-deployment via
// the spec field, or globally via the synapse process config (TODO:
// wire SYNAPSE_ASTER_BROKER_IMAGE through Config in the next slice).
const AsterBrokerImage = "aster-brokerd:0.3"

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
