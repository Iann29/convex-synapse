// adopt-domains-from-caddy: a one-shot CLI that ingests an existing
// operator-maintained Caddyfile, extracts hostname → backend mappings,
// and registers them as Synapse `deployment_domains`.
//
// Killer feature for migrating an existing VPS with hand-maintained
// Caddy blocks into Synapse with one command. Pure CLI — when the
// flag is set, run() returns early without booting the HTTP server.
//
// Scope is narrow on purpose: the parser only handles the common
// "<host> { ... reverse_proxy <upstream>:<port> ... }" shape. We
// intentionally do NOT pull a third-party Caddyfile parser (the
// upstream Caddy parser drags in enormous deps and we only need to
// inspect a handful of directives).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// adoptDomainsFlags holds all the user-tunable knobs for the
// adopt-domains-from-caddy subcommand.
type adoptDomainsFlags struct {
	Caddyfile   string
	APIURL      string
	Token       string
	DryRun      bool
	DefaultRole string
	// Manual hostname → deployment-name overrides parsed from
	// repeated --map=foo.example.com=deploymentname flags.
	Overrides map[string]string
}

// caddyBlock is a single top-level Caddyfile block. We retain just
// enough to drive inference: the address (which we narrow to a
// hostname, dropping ports/schemes/snippets) and the list of
// reverse_proxy upstreams found inside.
type caddyBlock struct {
	// Hostname is the canonical lowercased hostname this block
	// matches, or empty if the address was a port-only listener
	// (e.g. ":8080") or a snippet header (e.g. "(name)").
	Hostname string
	// IsSnippet records whether the address looked like "(name)" so
	// the caller can skip it without confusing it for an unparsed
	// hostname.
	IsSnippet bool
	// Upstreams is the list of "<host>:<port>" tuples found in
	// reverse_proxy directives anywhere in the block, in source
	// order. We keep all of them — the role-inference heuristic
	// uses the LAST one, since `handle @api_v1 { reverse_proxy ... }`
	// blocks typically come BEFORE the catch-all reverse_proxy.
	Upstreams []caddyUpstream
	// Line is the 1-based source line where the address opened, used
	// in error messages.
	Line int
}

type caddyUpstream struct {
	Host string
	Port int
}

// caddyParseError is returned by parseCaddyfile when a block is
// malformed enough that we can't safely emit a hostname tuple. The
// caller surfaces these to the operator but keeps going so a single
// bad block doesn't block the whole import.
type caddyParseError struct {
	Line int
	Msg  string
}

func (e *caddyParseError) Error() string {
	return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
}

// plannedDomain is one row in the import plan. The CLI builds a list,
// prints it, and (in non-dry-run) POSTs each entry.
type plannedDomain struct {
	Hostname       string
	DeploymentName string
	Role           string
	// Reason carries the operator-facing explanation for why a row
	// could not be planned (deployment not found, ambiguous role,
	// etc.). Empty on success.
	Reason string
	// Source is the upstream we keyed off of (informational, shown
	// in dry-run output).
	Source string
}

// adoptDomainsRun is the entry point invoked from main when the
// --adopt-domains-from-caddy flag is set. Returns a non-nil error
// only on usage / unrecoverable IO problems; per-row failures are
// reported in the summary table and tallied into a non-zero exit
// status iff one or more rows failed in live mode.
func adoptDomainsRun(flags adoptDomainsFlags, stdout io.Writer) error {
	if flags.Caddyfile == "" {
		return errors.New("--caddyfile is required")
	}
	if flags.APIURL == "" {
		flags.APIURL = "http://localhost:8080"
	}
	flags.APIURL = strings.TrimRight(flags.APIURL, "/")
	if flags.DefaultRole == "" {
		flags.DefaultRole = "api"
	}
	switch flags.DefaultRole {
	case "api", "dashboard":
	default:
		return fmt.Errorf("--default-role must be \"api\" or \"dashboard\" (got %q)", flags.DefaultRole)
	}
	if !flags.DryRun && flags.Token == "" {
		return errors.New("--token is required for live mode (use --dry-run to skip)")
	}

	src, err := os.ReadFile(flags.Caddyfile)
	if err != nil {
		return fmt.Errorf("read caddyfile: %w", err)
	}

	blocks, parseErrs := parseCaddyfile(src)
	for _, pe := range parseErrs {
		fmt.Fprintf(stdout, "warn: skipping malformed block: %s\n", pe.Error())
	}

	plan := buildPlan(blocks, flags.DefaultRole, flags.Overrides)
	printPlan(stdout, plan)

	if flags.DryRun {
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "(dry-run) no API calls made.")
		return nil
	}

	// Live mode — POST each ready row. Rows with non-empty Reason
	// are skipped; the operator must rerun with --map=<host>=<name>
	// to fix them.
	client := &http.Client{Timeout: 30 * time.Second}
	results := postPlan(context.Background(), client, flags.APIURL, flags.Token, plan)
	printResults(stdout, results)

	failed := 0
	for _, r := range results {
		if !r.OK {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d/%d domains failed to register", failed, len(results))
	}
	return nil
}

// ---------- parser ----------

// parseCaddyfile walks the bytes of a Caddyfile and returns the list
// of blocks it could identify. The grammar handled is intentionally
// narrow:
//
//   - Top-level lines that end with "{" open a block. The text before
//     "{" is the "address". An address that starts with "(" is a
//     snippet definition; we record the block but flag IsSnippet so
//     the caller skips it.
//   - "}" on its own (or as the final char on a line) closes the
//     current block.
//   - Inside a block, "reverse_proxy <upstream>" extracts one
//     upstream per call. Upstreams may carry the "h2c://" prefix or
//     similar — we strip everything before the last "://" and keep
//     "host:port".
//   - Nested blocks (handle, @matcher) are tracked so we don't close
//     the outer block early, but their contents share the same
//     upstream collection list (we only care about reverse_proxy
//     destinations, not which handle they're inside).
//   - Blank lines and "#"-prefixed comments are skipped.
//
// Returns the list of parsed blocks plus any non-fatal parse errors
// (one-per-block-with-trouble) so the caller can surface them as
// warnings.
func parseCaddyfile(src []byte) ([]caddyBlock, []*caddyParseError) {
	var (
		blocks []caddyBlock
		errs   []*caddyParseError
	)

	// stack[len-1] is the block currently being parsed. depth>1 means
	// we're inside a nested directive block (handle, @matcher, etc.)
	// — we keep parsing reverse_proxy lines into the SAME caddyBlock
	// at the bottom of the stack since that's the operator's view of
	// "the block for hostname X".
	type frame struct {
		topLevelIdx int // index into blocks; -1 if we're nested without an open top-level block
	}
	var stack []frame

	lines := splitLines(src)
	for i, raw := range lines {
		line := stripComment(raw)
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// A "}" can be alone or trail other content. Handle the alone
		// case first (the common case for closing braces).
		if line == "}" {
			if len(stack) == 0 {
				errs = append(errs, &caddyParseError{Line: i + 1, Msg: "unexpected '}'"})
				continue
			}
			stack = stack[:len(stack)-1]
			continue
		}

		// Block-opening line: ends with "{". Address is everything
		// before the trailing "{". An empty address is invalid.
		if strings.HasSuffix(line, "{") {
			addr := strings.TrimSpace(strings.TrimSuffix(line, "{"))
			if addr == "" {
				errs = append(errs, &caddyParseError{Line: i + 1, Msg: "block opened with empty address"})
				stack = append(stack, frame{topLevelIdx: -1})
				continue
			}
			// Already inside a block? This is a NESTED directive
			// (handle, @matcher, etc.) — push a frame that points
			// back at the same top-level idx so reverse_proxy lines
			// keep landing in the right place.
			if len(stack) > 0 {
				stack = append(stack, frame{topLevelIdx: stack[len(stack)-1].topLevelIdx})
				continue
			}

			// Top-level block.
			block := caddyBlock{Line: i + 1}
			block.Hostname, block.IsSnippet = classifyAddress(addr)
			blocks = append(blocks, block)
			stack = append(stack, frame{topLevelIdx: len(blocks) - 1})
			continue
		}

		// Inside a block (or its nested directives). The only thing
		// we care about is `reverse_proxy <upstream>`.
		if len(stack) == 0 {
			// A bare directive at the top level (no surrounding
			// block) is a Caddy global option line — ignore it.
			continue
		}
		topIdx := stack[len(stack)-1].topLevelIdx
		if topIdx < 0 {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "reverse_proxy" {
			// reverse_proxy may have multiple upstreams. The first
			// one is enough for inference; collect them all so the
			// dry-run plan can show "we picked X out of [X,Y,Z]" if
			// we ever want to. Stop at the first arg that opens a
			// nested block ("{") — those are options, not upstreams.
			for _, arg := range fields[1:] {
				if arg == "{" {
					break
				}
				ups, ok := parseUpstream(arg)
				if !ok {
					continue
				}
				blocks[topIdx].Upstreams = append(blocks[topIdx].Upstreams, ups)
			}
		}
	}

	if len(stack) > 0 {
		errs = append(errs, &caddyParseError{Line: len(lines), Msg: "unclosed block at end of file"})
	}
	return blocks, errs
}

// splitLines is byte-safe (handles bare \r and CRLF) and avoids
// pulling bufio for a small file.
func splitLines(src []byte) []string {
	// Normalise line endings then split. Caddyfile is tiny — the
	// allocation cost is fine.
	s := string(bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n")))
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

// stripComment removes everything after a '#' that is not inside a
// quoted string. Caddyfiles support both inline (` foo # bar`) and
// full-line (`# bar`) comments. We don't try to parse quoted strings
// fully — a hash inside an upstream URL would be exotic — so the
// dumb "first unescaped #" rule is enough.
func stripComment(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '#' {
			// Treat "#" anywhere as a line comment. Caddyfile
			// addresses + reverse_proxy upstreams don't legitimately
			// contain '#'.
			return s[:i]
		}
	}
	return s
}

// classifyAddress inspects the text before "{" and returns the
// hostname (lowercased) plus a snippet flag. Anything that doesn't
// look like a single hostname is returned with empty hostname so
// the caller skips it:
//
//   - "(name)"            → snippet definition
//   - ":8080" / ":443"    → port-only listener
//   - "https://foo.com"   → schema-prefixed (we drop the scheme)
//   - "foo.com, bar.com"  → multi-host, take first; nudge operator
//     to split if they want both registered.
//   - "foo.com:443"       → strip the port suffix
//   - "*.foo.com"         → wildcard; skip (Synapse stores
//     deployment-specific subdomains, not wildcards)
func classifyAddress(addr string) (hostname string, snippet bool) {
	addr = strings.TrimSpace(addr)
	if strings.HasPrefix(addr, "(") {
		return "", true
	}
	// Multi-host: take first.
	if comma := strings.IndexByte(addr, ','); comma >= 0 {
		addr = strings.TrimSpace(addr[:comma])
	}
	// Drop scheme.
	if i := strings.Index(addr, "://"); i >= 0 {
		addr = addr[i+3:]
	}
	// Port-only listener.
	if strings.HasPrefix(addr, ":") {
		return "", false
	}
	// Strip trailing port. We do this by chopping on the LAST ":"
	// that precedes a numeric tail.
	if colon := strings.LastIndexByte(addr, ':'); colon >= 0 {
		tail := addr[colon+1:]
		if _, err := strconv.Atoi(tail); err == nil {
			addr = addr[:colon]
		}
	}
	// Wildcard subdomain — out of scope for per-deployment domains.
	if strings.HasPrefix(addr, "*.") || addr == "*" {
		return "", false
	}
	addr = strings.ToLower(addr)
	if !looksLikeHostname(addr) {
		return "", false
	}
	return addr, false
}

// looksLikeHostname is a cheap sanity check — at least one dot, no
// whitespace, no slash. We do NOT try to mirror the strict regex in
// internal/api/domains.go (the API will validate again). The point
// here is to filter out parser garbage like "log_level INFO".
func looksLikeHostname(s string) bool {
	if s == "" {
		return false
	}
	if strings.ContainsAny(s, " \t/?#") {
		return false
	}
	if strings.Count(s, ".") < 1 {
		return false
	}
	return true
}

// parseUpstream extracts ("host", port) from a reverse_proxy arg.
// Strips schemes and trailing slashes. Returns ok=false if the arg
// isn't a host:port shape we can use.
func parseUpstream(arg string) (caddyUpstream, bool) {
	s := strings.TrimSpace(arg)
	if s == "" {
		return caddyUpstream{}, false
	}
	// Strip scheme — Caddy supports h2c://, http://, https://, etc.
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	// Strip trailing path / slash.
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	colon := strings.LastIndexByte(s, ':')
	if colon < 0 {
		// No port. Could be a Caddy upstream by name — we can't use
		// it for port-based role inference, so surface the host only
		// (port=0).
		return caddyUpstream{Host: s, Port: 0}, true
	}
	host := s[:colon]
	port, err := strconv.Atoi(s[colon+1:])
	if err != nil {
		return caddyUpstream{}, false
	}
	return caddyUpstream{Host: host, Port: port}, true
}

// ---------- inference ----------

// buildPlan walks the parsed blocks and produces one plannedDomain
// per usable hostname. Inference rules:
//
//  1. Skip snippet blocks and address-less blocks.
//  2. Deployment name: explicit override wins. Otherwise, take the
//     SECOND label of the hostname (for "api.fechasul.com.br" →
//     "fechasul"). If the hostname has fewer than 3 labels we can't
//     infer; the row is emitted with a Reason explaining why.
//  3. Role:
//     - hostname starts with "dashboard." → dashboard
//     - hostname starts with "api." → api
//     - else: look at the LAST reverse_proxy upstream port. Convex
//     backend host-mapped ports live in the 322X-323X range; the
//     dashboard ports live in the 679X-680X range. Outside both,
//     fall back to the operator's --default-role.
func buildPlan(blocks []caddyBlock, defaultRole string, overrides map[string]string) []plannedDomain {
	var out []plannedDomain
	for _, b := range blocks {
		if b.IsSnippet || b.Hostname == "" {
			continue
		}
		row := plannedDomain{Hostname: b.Hostname}

		// Pick a representative upstream for the source/role hint.
		// Prefer the LAST reverse_proxy seen — Caddyfiles usually put
		// the catch-all at the end after path-matched handles.
		if len(b.Upstreams) > 0 {
			u := b.Upstreams[len(b.Upstreams)-1]
			row.Source = fmt.Sprintf("%s:%d", u.Host, u.Port)
		}

		// Role inference.
		row.Role = inferRole(b.Hostname, b.Upstreams, defaultRole)

		// Deployment-name inference.
		if overrides != nil {
			if name, ok := overrides[b.Hostname]; ok && name != "" {
				row.DeploymentName = name
				out = append(out, row)
				continue
			}
		}
		name, ok := inferDeploymentName(b.Hostname)
		if !ok {
			row.Reason = fmt.Sprintf(
				"could not auto-detect deployment for %q; pass --map=%s=<deployment-name> to override",
				b.Hostname, b.Hostname)
			out = append(out, row)
			continue
		}
		row.DeploymentName = name
		out = append(out, row)
	}

	// Stable order for predictable test output.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Hostname < out[j].Hostname })
	return out
}

// inferRole picks "api" vs "dashboard" using hostname hints first
// (fast + unambiguous when the operator names them well), then
// falls back to a port-range heuristic, and finally to defaultRole.
func inferRole(hostname string, upstreams []caddyUpstream, defaultRole string) string {
	first := firstLabel(hostname)
	switch first {
	case "dashboard", "dash":
		return "dashboard"
	case "api", "backend", "convex":
		return "api"
	}
	// Port-range heuristic on the LAST upstream (matches the
	// Source field). 3210/322X-323X is Convex backend; 6791/679X-
	// 680X is the dashboard sidecar.
	if len(upstreams) > 0 {
		p := upstreams[len(upstreams)-1].Port
		switch {
		case p == 3210 || (p >= 3220 && p <= 3239):
			return "api"
		case p == 6791 || (p >= 6790 && p <= 6809):
			return "dashboard"
		}
	}
	return defaultRole
}

// firstLabel returns the leftmost DNS label of the hostname.
func firstLabel(host string) string {
	i := strings.IndexByte(host, '.')
	if i < 0 {
		return host
	}
	return host[:i]
}

// inferDeploymentName takes the SECOND label (zero-indexed [1]) of
// the hostname. "api.fechasul.com.br" → "fechasul". Returns ok=false
// when the hostname has fewer than 3 labels (e.g. "fechasul.com" —
// the operator would need to use --map there).
func inferDeploymentName(host string) (string, bool) {
	parts := strings.Split(host, ".")
	if len(parts) < 3 {
		return "", false
	}
	candidate := strings.ToLower(parts[1])
	// Synapse deployment names are slug-shaped (lowercase letters,
	// digits, hyphens). The full validation lives on the backend; a
	// simple non-empty + alnum/hyphen check here filters obvious
	// junk like a numeric IP.
	if candidate == "" {
		return "", false
	}
	for _, c := range candidate {
		if !(c == '-' || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return "", false
		}
	}
	return candidate, true
}

// ---------- output ----------

// printPlan writes the plan as a tabular summary. The expected output
// for a 2-row plan looks like:
//
//	HOSTNAME                       DEPLOYMENT  ROLE       SOURCE          NOTE
//	api.fechasul.com.br            fechasul    api        127.0.0.1:3222  ok
//	dashboard.fechasul.com.br      fechasul    dashboard  127.0.0.1:6797  ok
func printPlan(w io.Writer, plan []plannedDomain) {
	if len(plan) == 0 {
		fmt.Fprintln(w, "no usable hostname blocks found in caddyfile")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOSTNAME\tDEPLOYMENT\tROLE\tSOURCE\tNOTE")
	for _, row := range plan {
		dep := row.DeploymentName
		note := "ok"
		if row.Reason != "" {
			dep = "?"
			note = row.Reason
		}
		src := row.Source
		if src == "" {
			src = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			row.Hostname, dep, row.Role, src, note)
	}
	_ = tw.Flush()
}

// ---------- live mode ----------

// postResult records what happened when we tried to register one row.
type postResult struct {
	Hostname string
	OK       bool
	Status   int
	// Message is either the API's message or our own short
	// description of a network error.
	Message string
}

// postPlan POSTs each "ready" row in the plan to
// /v1/deployments/<name>/domains. Rows with a non-empty Reason
// (typically: failed deployment-name auto-detect) are skipped with a
// surfaced postResult so the operator's table shows them.
func postPlan(ctx context.Context, client *http.Client, apiURL, token string, plan []plannedDomain) []postResult {
	out := make([]postResult, 0, len(plan))
	for _, row := range plan {
		if row.Reason != "" {
			out = append(out, postResult{
				Hostname: row.Hostname,
				OK:       false,
				Message:  "skipped: " + row.Reason,
			})
			continue
		}
		body, _ := json.Marshal(map[string]string{
			"domain": row.Hostname,
			"role":   row.Role,
		})
		url := fmt.Sprintf("%s/v1/deployments/%s/domains", apiURL, row.DeploymentName)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			out = append(out, postResult{Hostname: row.Hostname, Message: err.Error()})
			continue
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			out = append(out, postResult{Hostname: row.Hostname, Message: err.Error()})
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		ok := resp.StatusCode >= 200 && resp.StatusCode < 300
		msg := strings.TrimSpace(string(respBody))
		if ok {
			msg = "registered"
		} else if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		out = append(out, postResult{
			Hostname: row.Hostname,
			OK:       ok,
			Status:   resp.StatusCode,
			Message:  truncateMsg(msg, 120),
		})
	}
	return out
}

func truncateMsg(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// printResults dumps the live-mode outcome table.
func printResults(w io.Writer, results []postResult) {
	if len(results) == 0 {
		return
	}
	fmt.Fprintln(w, "")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOSTNAME\tSTATUS\tRESULT")
	ok, fail := 0, 0
	for _, r := range results {
		st := "-"
		if r.Status > 0 {
			st = strconv.Itoa(r.Status)
		}
		mark := "OK"
		if !r.OK {
			mark = "FAIL"
			fail++
		} else {
			ok++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s: %s\n", r.Hostname, st, mark, r.Message)
	}
	_ = tw.Flush()
	fmt.Fprintf(w, "\n%d ok, %d failed\n", ok, fail)
}
