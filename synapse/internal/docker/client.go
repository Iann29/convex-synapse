// Package docker wraps the Docker Engine API and exposes high-level
// provisioning primitives used by the deployments handler.
//
// Synapse uses Docker as its sole orchestrator in v0. Each deployment is a
// single container created from the configured Convex backend image, attached
// to the synapse-network bridge, and exposed via host-port mapping.
package docker

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

// Client wraps the Docker SDK client with our convention-defaults
// (network name, image, etc.). One instance is shared across handlers.
type Client struct {
	api          *client.Client
	logger       *slog.Logger
	BackendImage string
	Network      string
}

// NewClient connects to the Docker daemon at host (e.g. unix:///var/run/docker.sock
// or tcp://...) and ensures the configured network exists.
func NewClient(host, backendImage, networkName string, logger *slog.Logger) (*Client, error) {
	api, err := client.NewClientWithOpts(
		client.WithHost(host),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}

	c := &Client{api: api, logger: logger, BackendImage: backendImage, Network: networkName}
	if err := c.ensureNetwork(context.Background()); err != nil {
		return nil, err
	}
	return c, nil
}

// ensureNetwork creates the synapse-network bridge if it does not already exist.
// Idempotent — safe to call on every server start.
func (c *Client) ensureNetwork(ctx context.Context) error {
	nets, err := c.api.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return fmt.Errorf("list networks: %w", err)
	}
	for _, n := range nets {
		if n.Name == c.Network {
			return nil
		}
	}
	_, err = c.api.NetworkCreate(ctx, c.Network, network.CreateOptions{
		Driver: "bridge",
		Labels: map[string]string{"synapse.managed": "true"},
	})
	if err != nil {
		return fmt.Errorf("create network %q: %w", c.Network, err)
	}
	c.logger.Info("docker network created", "name", c.Network)
	return nil
}

// Ping checks daemon connectivity. Used by /health.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.api.Ping(ctx)
	return err
}
