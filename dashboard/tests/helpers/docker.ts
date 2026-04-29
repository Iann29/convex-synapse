// Docker cleanup helpers. After deployment tests we want to make sure no
// orphan Convex backend containers/volumes are left behind regardless of
// whether the test passed or threw.

import { execFileSync } from "node:child_process";

function run(cmd: string, args: string[]): string {
  try {
    return execFileSync(cmd, args, { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }).trim();
  } catch {
    return "";
  }
}

export function pruneSynapseContainers(): void {
  const ids = run("docker", [
    "ps", "-aq",
    "--filter", "label=synapse.managed=true",
  ]).split("\n").filter(Boolean);
  if (ids.length) {
    run("docker", ["rm", "-f", ...ids]);
  }
  const volumes = run("docker", [
    "volume", "ls", "-q",
    "--filter", "name=synapse-data-",
  ]).split("\n").filter(Boolean);
  if (volumes.length) {
    run("docker", ["volume", "rm", ...volumes]);
  }
}

export function listSynapseContainerNames(): string[] {
  return run("docker", [
    "ps",
    "--filter", "label=synapse.managed=true",
    "--format", "{{.Names}}",
  ]).split("\n").filter(Boolean);
}
