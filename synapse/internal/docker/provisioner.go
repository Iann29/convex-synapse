package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
)

// DeploymentSpec describes everything the provisioner needs to create one
// Convex backend container.
type DeploymentSpec struct {
	Name           string            // friendly name, used for container suffix and INSTANCE_NAME
	InstanceSecret string            // hex-encoded secret
	HostPort       int               // host port mapped to the container's 3210
	EnvVars        map[string]string // additional env, applied last (overrides defaults)
	// HealthcheckViaNetwork picks the URL the provisioner polls while waiting
	// for the backend to become healthy. See config.HealthcheckViaNetwork.
	HealthcheckViaNetwork bool
}

// DeploymentInfo is what the provisioner returns once a container is up.
type DeploymentInfo struct {
	ContainerID   string
	HostPort      int
	DeploymentURL string
}

// containerName returns the docker container name for a deployment.
// Prefixed so an operator listing `docker ps` can filter Synapse-managed
// containers without confusing them with anything else on the host.
func containerName(deploymentName string) string {
	return "convex-" + deploymentName
}

// volumeName isolates each deployment's data dir.
func volumeName(deploymentName string) string {
	return "synapse-data-" + deploymentName
}

// EnsureImage pulls the backend image if it is not already present locally.
// Pulling at provision time would add seconds to every create_deployment;
// callers should call this once at startup OR best-effort on first use.
func (c *Client) EnsureImage(ctx context.Context) error {
	images, err := c.api.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return fmt.Errorf("list images: %w", err)
	}
	for _, img := range images {
		for _, tag := range img.RepoTags {
			if tag == c.BackendImage {
				return nil
			}
		}
	}
	c.logger.Info("pulling backend image", "image", c.BackendImage)
	rc, err := c.api.ImagePull(ctx, c.BackendImage, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", c.BackendImage, err)
	}
	defer rc.Close()
	// Drain the response so the pull actually completes.
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("drain pull stream: %w", err)
	}
	return nil
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

	containerPort := nat.Port("3210/tcp")
	hostBinding := nat.PortBinding{HostIP: "0.0.0.0", HostPort: strconv.Itoa(spec.HostPort)}

	cloudOrigin := fmt.Sprintf("http://127.0.0.1:%d", spec.HostPort)
	env := []string{
		"INSTANCE_NAME=" + spec.Name,
		"INSTANCE_SECRET=" + spec.InstanceSecret,
		"CONVEX_CLOUD_ORIGIN=" + cloudOrigin,
		"CONVEX_SITE_ORIGIN=" + cloudOrigin,
	}
	for k, v := range spec.EnvVars {
		env = append(env, k+"="+v)
	}

	cfg := &container.Config{
		Image: c.BackendImage,
		Env:   env,
		Labels: map[string]string{
			"synapse.managed":    "true",
			"synapse.deployment": spec.Name,
		},
		ExposedPorts: nat.PortSet{containerPort: struct{}{}},
	}
	hostCfg := &container.HostConfig{
		PortBindings: nat.PortMap{containerPort: []nat.PortBinding{hostBinding}},
		RestartPolicy: container.RestartPolicy{
			Name:              container.RestartPolicyUnlessStopped,
			MaximumRetryCount: 0,
		},
		Mounts: nil, // volume below via Binds is simpler & older-Docker-friendly
		Binds:  []string{volumeName(spec.Name) + ":/convex/data"},
	}
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			c.Network: {},
		},
	}

	resp, err := c.api.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, containerName(spec.Name))
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
		healthURL = fmt.Sprintf("http://%s:3210", containerName(spec.Name))
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

// Destroy stops and removes the deployment's container and its data volume.
// Idempotent — missing container or volume is treated as success.
func (c *Client) Destroy(ctx context.Context, deploymentName string) error {
	name := containerName(deploymentName)
	timeout := 10
	_ = c.api.ContainerStop(ctx, name, container.StopOptions{Timeout: &timeout})
	if err := c.api.ContainerRemove(ctx, name, container.RemoveOptions{Force: true, RemoveVolumes: false}); err != nil {
		// 404 is fine, anything else is real.
		if !isNotFound(err) {
			return fmt.Errorf("remove container %s: %w", name, err)
		}
	}
	if err := c.api.VolumeRemove(ctx, volumeName(deploymentName), true); err != nil && !isNotFound(err) {
		return fmt.Errorf("remove volume: %w", err)
	}
	return nil
}

// Status reports the docker-side status of a deployment.
// Returns ("", nil) if the container does not exist.
func (c *Client) Status(ctx context.Context, deploymentName string) (string, error) {
	insp, err := c.api.ContainerInspect(ctx, containerName(deploymentName))
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
