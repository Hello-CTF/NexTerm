// Frontend mirror of store.AssetEnvironment: an asset's environment is derived
// from its tags — a canonical name (production/prod, staging/stage,
// development/dev, testing/test) or any `env:`-prefixed value. Matching uses
// the Go-consistent helpers so a frontend classification agrees with the
// backend's; the backend remains the final authorization boundary.

import { goToLowerString, goTrimSpace } from "./goFold";

const ENVIRONMENT_TAG_PREFIX = "env:";

function canonicalEnvironment(value: string, prefixed: boolean): string {
  switch (value) {
    case "production":
    case "prod":
      return "production";
    case "staging":
    case "stage":
      return "staging";
    case "development":
    case "dev":
      return "development";
    case "testing":
    case "test":
      return "testing";
    default:
      return prefixed ? value : "";
  }
}

// assetEnvironment mirrors store.AssetEnvironment exactly: tags split on ASCII
// and full-width commas, each trimmed with Go's White_Space set and lowercased
// with Go's simple mapping; a canonical "production" wins immediately,
// otherwise the first non-empty candidate wins.
export function assetEnvironment(tags: string): string {
  let environment = "";
  for (const part of tags.replaceAll("，", ",").split(",")) {
    let normalized = goToLowerString(goTrimSpace(part));
    if (normalized === "") continue;
    const prefixed = normalized.startsWith(ENVIRONMENT_TAG_PREFIX);
    if (prefixed) {
      normalized = goTrimSpace(normalized.slice(ENVIRONMENT_TAG_PREFIX.length));
    }
    const candidate = canonicalEnvironment(normalized, prefixed);
    if (candidate === "") continue;
    if (candidate === "production") return candidate;
    if (environment === "") environment = candidate;
  }
  return environment;
}
