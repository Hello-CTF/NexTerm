// The privacy engine is built into the application, so packages carry no native
// runtime. Updaters from 1.4.6-rc.3 and earlier still require an ai-runtime
// directory in the Linux deb payload and, on Windows, either onnxruntime.dll or a
// runtime.json that declares the runtime unavailable after the installer ran.
// Packages they can update to therefore ship ai-runtime/runtime.json with only an
// "unavailable" reason, which those updaters accept without a library.
import fs from "node:fs";
import path from "node:path";
import { assertion } from "./build-assertions.mjs";

export const LEGACY_RUNTIME_MARKER = `${JSON.stringify({ unavailable: "the privacy engine is built into the application" })}\n`;

export function writeLegacyRuntimeMarker(directory) {
  fs.mkdirSync(directory, { recursive: true });
  fs.writeFileSync(path.join(directory, "runtime.json"), LEGACY_RUNTIME_MARKER);
}

export function legacyRuntimeMarkerAssertions(directory) {
  const id = "legacy-runtime-marker";
  try {
    const entries = fs.existsSync(directory) ? fs.readdirSync(directory) : [];
    if (entries.length !== 1 || entries[0] !== "runtime.json") throw new Error(`ai-runtime must hold only runtime.json; found: ${entries.join(", ") || "nothing"}`);
    const file = path.join(directory, "runtime.json");
    if (!fs.lstatSync(file).isFile()) throw new Error("ai-runtime/runtime.json is not a regular file");
    const manifest = JSON.parse(fs.readFileSync(file, "utf8"));
    if (typeof manifest.unavailable !== "string" || !manifest.unavailable) throw new Error("ai-runtime/runtime.json must declare an unavailable reason");
    return [assertion(id, true, "ai-runtime/runtime.json declares the runtime unavailable, so updaters from 1.4.6-rc.3 and earlier accept the package")];
  } catch (error) {
    return [assertion(id, false, error.message)];
  }
}
