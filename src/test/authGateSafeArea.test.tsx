/** @vitest-environment jsdom */

import { createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mount, type MountedView } from "./features/reactTestUtils";
import { AuthGate } from "../features/auth/AuthGate";
import { useAuth } from "../features/auth/store";

let mounted: MountedView | undefined;

beforeEach(() => {
  useAuth.setState({
    status: null,
    user: null,
    dek: null,
    pendingMfa: null,
    pendingRecoveryKey: null,
    error: null,
    refresh: vi.fn().mockResolvedValue(undefined),
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useAuth.setState({ gate: "ready", pendingRecoveryKey: null });
});

describe("AuthGate safe area", () => {
  for (const branch of ["loading", "setup", "recovery"] as const) {
    it(`uses the protected fullscreen surface for ${branch}`, () => {
      useAuth.setState({
        gate: branch === "recovery" ? "ready" : branch,
        pendingRecoveryKey: branch === "recovery"
          ? { formatted: "AAAA-AAAA", canonical: "aaaaaaaa" }
          : null,
      });
      mounted = mount(createElement(AuthGate));

      const gate = mounted.container.querySelector(".nx-auth-gate");
      expect(gate).not.toBeNull();
      expect(gate?.classList.contains("fixed")).toBe(true);
      expect(gate?.querySelector(":scope > .nx-card")).not.toBeNull();
    });
  }
});
