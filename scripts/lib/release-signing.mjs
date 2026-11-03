// Pure release signing checks; must not import build-context.mjs (it reads the repo and env on import).
import { parseReleaseVersion } from "./semver.mjs";

export function releaseSigningKeyProblem({ kind, releaseVersion, publicKey }) {
  if (kind !== "agent") return null;
  if (!releaseVersion || !parseReleaseVersion(releaseVersion)) return null;
  if (publicKey) return null;
  return "agent release builds require NEXTERM_MINISIGN_PUBLIC_KEY: without it the agent refuses every self-upgrade";
}
