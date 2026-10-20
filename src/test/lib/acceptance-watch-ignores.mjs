// The implementation lives with the build tooling in scripts/lib so the
// production Vite config does not depend on the test tree; this re-export
// keeps the existing test import stable.
export { acceptanceWatchIgnored } from "../../../scripts/lib/vite-watch-ignores.mjs";
