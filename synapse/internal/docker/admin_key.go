package docker

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/container"
)

// (extractAdminKey is below; kept here in package-internal scope so its
// behaviour is testable without exporting it.)

// GenerateAdminKey computes an admin key that the Convex backend (running with
// the matching INSTANCE_SECRET) will accept on /api/check_admin_key.
//
// Convex signs admin keys with INSTANCE_SECRET via its keybroker; we cannot
// shortcut this on our side. Instead, we run the official `generate_key`
// binary that ships inside the convex-backend image, off a throwaway
// container. ~150ms cold; warm starts are <50ms because the image is cached.
//
// The binary occasionally panics in `fastant` (TSC overflow on some CPUs),
// surfacing as empty stdout + a Rust panic message on stderr. We retry a
// few times since the panic is transient.
func (c *Client) GenerateAdminKey(ctx context.Context, instanceName, instanceSecret string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		key, err := c.generateAdminKeyOnce(ctx, instanceName, instanceSecret)
		if err == nil && key != "" {
			return key, nil
		}
		lastErr = err
		if err == nil {
			lastErr = fmt.Errorf("generate_key empty output")
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	return "", lastErr
}

func (c *Client) generateAdminKeyOnce(ctx context.Context, instanceName, instanceSecret string) (string, error) {
	resp, err := c.api.ContainerCreate(ctx,
		&container.Config{
			Image:      c.BackendImage,
			Entrypoint: []string{"/convex/generate_key"},
			Cmd:        []string{instanceName, instanceSecret},
			Tty:        false,
			AttachStdout: true,
			AttachStderr: true,
		},
		&container.HostConfig{
			AutoRemove: false, // we ContainerRemove ourselves so output stream survives
		},
		nil, nil, "")
	if err != nil {
		return "", fmt.Errorf("create generate_key container: %w", err)
	}
	id := resp.ID
	defer func() {
		_ = c.api.ContainerRemove(context.Background(), id, container.RemoveOptions{Force: true})
	}()

	if err := c.api.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("start generate_key: %w", err)
	}

	statusCh, errCh := c.api.ContainerWait(ctx, id, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return "", fmt.Errorf("wait generate_key: %w", err)
		}
	case <-statusCh:
	}

	logs, err := c.api.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		return "", fmt.Errorf("read generate_key logs: %w", err)
	}
	defer logs.Close()

	// Docker's combined-stream framing: each chunk has an 8-byte header that
	// we have to strip. The output is short (a single line) so a small read
	// covers it.
	var buf bytes.Buffer
	chunk := make([]byte, 4096)
	for {
		n, readErr := logs.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
		}
		if readErr != nil {
			break
		}
	}
	out := stripDockerStreamHeaders(buf.Bytes())
	// generate_key prints two lines: "Admin key:" then the key itself.
	// We want only the key line.
	key := extractAdminKey(string(out))
	if key == "" {
		return "", fmt.Errorf("generate_key produced empty output (raw: %q)", string(out))
	}
	return key, nil
}

// extractAdminKey pulls the actual admin key out of generate_key's stdout.
// Real output:
//
//	Admin key:
//	{instance_name}|{hex...}
//
// We pick the last non-empty line that contains the "<name>|<hex>" pattern.
func extractAdminKey(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Admin key") {
			continue
		}
		if strings.Contains(line, "|") {
			return line
		}
	}
	return ""
}

// stripDockerStreamHeaders removes the 8-byte multiplex headers Docker
// prepends to each chunk in a non-TTY logs stream:
//
//	[STREAM_TYPE 0 0 0 SIZE_BE_4]
//
// The output is harmless if the stream had no headers (we just walk the
// bytes that look like headers and pass through whatever).
func stripDockerStreamHeaders(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		// A header must have at least 8 bytes left.
		if len(b)-i < 8 {
			out = append(out, b[i:]...)
			break
		}
		stream := b[i]
		// stream is 0/1/2 in real headers; if it's anything else, treat
		// the chunk as raw and bail.
		if stream > 2 {
			out = append(out, b[i:]...)
			break
		}
		size := int(b[i+4])<<24 | int(b[i+5])<<16 | int(b[i+6])<<8 | int(b[i+7])
		i += 8
		end := i + size
		if end > len(b) {
			end = len(b)
		}
		out = append(out, b[i:end]...)
		i = end
	}
	return out
}
