# installer/install/caddy.sh
# shellcheck shell=bash
#
# Caddy reverse-proxy detection + configuration. Three modes:
#
#   caddy_host           — Caddy is already running on the host as a
#                          systemd service. We append a managed block
#                          to /etc/caddy/Caddyfile (BEGIN/END markers
#                          for idempotency) and reload.
#   nginx_external       — Operator runs nginx. We can't auto-edit
#                          their config; we print a config snippet
#                          they can paste manually and exit with a
#                          documented "manual step required" status.
#   caddy_compose        — Fresh host with no reverse proxy. We
#                          enable the optional `caddy` profile in
#                          docker-compose.yml and use the standalone
#                          Caddyfile template.
#
# Composes detect:: helpers from chunk 1 and uses ui::* for
# operator-facing output.
#
# Tests inject CADDY_RELOAD=fake-reload-cmd / CADDY_FILE=/tmp/...
# to make the reload + write paths deterministic.

# ---- managed-block primitive ---------------------------------------

# caddy::_block_markers <tag> → echoes "begin\n<TAB>end" on two lines.
# Stable strings so awk-based stripping is exact-match (no regex
# escape needed). The tag is part of the marker so multiple managed
# blocks (synapse + future ones) can coexist.
caddy::_block_markers() {
    local tag="$1"
    printf '# BEGIN %s (managed by synapse setup.sh — do not edit)\n' "$tag"
    printf '# END %s (managed by synapse setup.sh)\n' "$tag"
}

# caddy::upsert_block <file> <tag>
# Reads block content from stdin. Strips any pre-existing block with
# the same tag (matched by exact BEGIN/END lines), then appends the
# new block at the bottom. Atomic via mktemp+mv. Re-running with the
# same input is a no-op semantically (the BEGIN/END markers identify
# the managed region and the rest of the file is preserved verbatim).
caddy::upsert_block() {
    local file="$1" tag="$2"
    local begin end
    begin="$(printf '# BEGIN %s (managed by synapse setup.sh — do not edit)' "$tag")"
    end="$(printf   '# END %s (managed by synapse setup.sh)' "$tag")"
    local content
    content="$(cat)"
    local tmp
    tmp="$(mktemp "${file}.XXXXXX")" || return 2
    if [[ -f "$file" ]]; then
        # Strip any existing block with the same tag.
        awk -v b="$begin" -v e="$end" '
            $0 == b { skip = 1; next }
            $0 == e { skip = 0; next }
            !skip   { print }
        ' "$file" >"$tmp"
    else
        : >"$tmp"
    fi
    {
        printf '\n%s\n' "$begin"
        printf '%s\n' "$content"
        printf '%s\n' "$end"
    } >>"$tmp"
    if [[ -f "$file" ]]; then
        chmod --reference="$file" "$tmp" 2>/dev/null || chmod 0644 "$tmp"
    else
        chmod 0644 "$tmp"
    fi
    mv -f "$tmp" "$file"
}

# caddy::remove_block <file> <tag>
# Strips the managed block with the given tag. Used by uninstall.
caddy::remove_block() {
    local file="$1" tag="$2"
    [[ -f "$file" ]] || return 0
    local begin end
    begin="$(printf '# BEGIN %s (managed by synapse setup.sh — do not edit)' "$tag")"
    end="$(printf   '# END %s (managed by synapse setup.sh)' "$tag")"
    local tmp
    tmp="$(mktemp "${file}.XXXXXX")" || return 2
    awk -v b="$begin" -v e="$end" '
        $0 == b { skip = 1; next }
        $0 == e { skip = 0; next }
        !skip   { print }
    ' "$file" >"$tmp"
    chmod --reference="$file" "$tmp" 2>/dev/null || chmod 0644 "$tmp"
    mv -f "$tmp" "$file"
}

# ---- mode detection ------------------------------------------------

# caddy::detect_mode → echoes one of caddy_host / nginx_external /
# caddy_compose. Always exits 0; the caller branches on the string.
caddy::detect_mode() {
    if detect::has_caddy; then
        # If caddy is on PATH and a unit is enabled, treat as host.
        # Tests can override via CADDY_FORCE_MODE for path coverage.
        if [[ -n "${CADDY_FORCE_MODE:-}" ]]; then
            echo "$CADDY_FORCE_MODE"
            return 0
        fi
        echo "caddy_host"
        return 0
    fi
    if detect::has_nginx; then
        echo "nginx_external"
        return 0
    fi
    echo "caddy_compose"
}

# ---- template rendering --------------------------------------------

# caddy::_render <template> → echoes the rendered template to stdout.
# Substitutes {{KEY}} placeholders from exported env vars (DOMAIN,
# DASHBOARD_PORT, SYNAPSE_PORT, ACME_EMAIL). Same substitution logic
# as secrets::render_env_tmpl, factored slightly differently so we
# can pipe straight into upsert_block / a file.
caddy::_render() {
    local tmpl="$1"
    [[ -r "$tmpl" ]] || { echo "caddy::_render: $tmpl unreadable" >&2; return 2; }
    local content
    content="$(cat "$tmpl")"
    local placeholders
    placeholders="$(grep -oE '\{\{[A-Z_][A-Z0-9_]*\}\}' "$tmpl" | sort -u)"
    local ph key val esc
    while IFS= read -r ph; do
        [[ -z "$ph" ]] && continue
        key="${ph#\{\{}"; key="${key%\}\}}"
        val="${!key:-}"
        esc="$(printf '%s' "$val" | sed -e 's/[\&|]/\\&/g')"
        content="$(printf '%s' "$content" | sed "s|${ph}|${esc}|g")"
    done <<<"$placeholders"
    printf '%s' "$content"
}

# ---- mode actions ---------------------------------------------------

# caddy::install_host_block <caddy_file> <fragment_template>
# caddy_host mode entry point. Renders the fragment template,
# upserts it into <caddy_file>, then reloads Caddy (or runs
# CADDY_RELOAD if set, for tests).
caddy::install_host_block() {
    local caddy_file="${1:-/etc/caddy/Caddyfile}"
    local tmpl="${2:-$INSTALLER_TEMPLATES/caddy.fragment}"
    local rendered
    rendered="$(caddy::_render "$tmpl")" || return 2
    if ! caddy::upsert_block "$caddy_file" "synapse" <<<"$rendered"; then
        echo "caddy::install_host_block: upsert failed" >&2
        return 2
    fi
    local reload_cmd="${CADDY_RELOAD:-systemctl reload caddy}"
    # shellcheck disable=SC2086  # intentional word-split for the
    # configurable command string.
    $reload_cmd
}

# caddy::print_nginx_snippet <nginx_template>
# nginx_external mode. Prints a config block the operator can paste
# into their nginx server { } context. We don't auto-edit nginx
# configs because the surface is too varied.
caddy::print_nginx_snippet() {
    cat <<EOF
# === Synapse — paste into your existing nginx server block ===
location /v1/   { proxy_pass http://127.0.0.1:${SYNAPSE_PORT:-8080}/v1/;   proxy_http_version 1.1; proxy_set_header Host \$host; }
location /d/    { proxy_pass http://127.0.0.1:${SYNAPSE_PORT:-8080}/d/;    proxy_http_version 1.1; proxy_set_header Host \$host; }
location /health { proxy_pass http://127.0.0.1:${SYNAPSE_PORT:-8080}/health; }
location /      { proxy_pass http://127.0.0.1:${DASHBOARD_PORT:-6790}/;  proxy_http_version 1.1; proxy_set_header Host \$host; }
# Then: sudo nginx -t && sudo systemctl reload nginx
EOF
}

# caddy::write_standalone <out_file> <standalone_template>
# caddy_compose mode. Renders the standalone Caddyfile to a path
# the docker-compose `caddy` service will mount. Refuses to overwrite
# an existing file unless CADDY_FORCE_OVERWRITE=1 is set.
caddy::write_standalone() {
    local out="${1:?out path required}"
    local tmpl="${2:-$INSTALLER_TEMPLATES/caddy.standalone}"
    if [[ -e "$out" && "${CADDY_FORCE_OVERWRITE:-0}" != "1" ]]; then
        echo "caddy::write_standalone: $out exists; pass CADDY_FORCE_OVERWRITE=1 to replace" >&2
        return 1
    fi
    local rendered
    rendered="$(caddy::_render "$tmpl")" || return 2
    local tmp
    tmp="$(mktemp "${out}.XXXXXX")" || return 2
    printf '%s' "$rendered" >"$tmp"
    chmod 0644 "$tmp"
    mv -f "$tmp" "$out"
}
