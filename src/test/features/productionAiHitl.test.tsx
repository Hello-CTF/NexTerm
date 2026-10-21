/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import {
  confirmationInput,
  requiredEnvironmentNamesFromEvent,
} from "../../features/ai/aiWire";

describe("requiredEnvironmentNamesFromEvent", () => {
  it("reads the typed environment names from a production confirmRequired event", () => {
    expect(requiredEnvironmentNamesFromEvent({ requiredEnvironmentNames: ["production"] })).toEqual(["production"]);
    expect(requiredEnvironmentNamesFromEvent({ requiredEnvironmentNames: ["live", "production"] })).toEqual([
      "live",
      "production",
    ]);
  });

  it("returns undefined when the field is absent (legacy or non-production challenge)", () => {
    expect(requiredEnvironmentNamesFromEvent({})).toBeUndefined();
    expect(requiredEnvironmentNamesFromEvent({ requiredEnvironmentNames: undefined })).toBeUndefined();
  });

  it("fails closed to undefined on malformed fields", () => {
    expect(requiredEnvironmentNamesFromEvent({ requiredEnvironmentNames: [] })).toBeUndefined();
    expect(requiredEnvironmentNamesFromEvent({ requiredEnvironmentNames: "production" })).toBeUndefined();
    expect(requiredEnvironmentNamesFromEvent({ requiredEnvironmentNames: [42] })).toBeUndefined();
    expect(requiredEnvironmentNamesFromEvent({ requiredEnvironmentNames: ["production", "  "] })).toBeUndefined();
    expect(requiredEnvironmentNamesFromEvent({ requiredEnvironmentNames: [null] })).toBeUndefined();
  });
});

describe("confirmationInput environment names", () => {
  const request = { jobId: "job-1", callId: "call-1", nonce: "nonce-1" };

  it("sends no environmentNames key for the legacy fieldless challenge", () => {
    expect(confirmationInput(request, "allow")).toEqual({
      jobId: "job-1",
      callId: "call-1",
      nonce: "nonce-1",
      decision: "allow",
    });
    expect(confirmationInput(request, "deny", [])).toEqual({
      jobId: "job-1",
      callId: "call-1",
      nonce: "nonce-1",
      decision: "deny",
    });
  });

  it("sends only the user's actual typed names, for every decision", () => {
    expect(confirmationInput(request, "allow", ["production"])).toEqual({
      jobId: "job-1",
      callId: "call-1",
      nonce: "nonce-1",
      decision: "allow",
      environmentNames: ["production"],
    });
    expect(confirmationInput(request, "deny", ["live", "production"]).environmentNames).toEqual([
      "live",
      "production",
    ]);
  });
});
