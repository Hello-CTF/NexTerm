import { describe, expect, it } from "vitest";
import {
  autoWriteAllowed,
  formatEvidence,
  handleAtHostCwd,
  isValidHandleName,
  mentionToken,
  presenceView,
  runStateView,
} from "../features/ai/agentState";
import type { AgentEvidence } from "../features/ai/agentTypes";

const evidence: AgentEvidence = {
  kind: "output",
  observedAt: 1_760_000_000_000,
  summary: "输出锚点匹配",
};

describe("agentState run states", () => {
  it("idle renders as its own state and never as done", () => {
    const view = runStateView("idle", null);
    expect(view.label).toBe("空闲");
    expect(view.key).toBe("idle");
    expect(view.label).not.toBe("已完成");
  });

  it("done without evidence degrades to unknown", () => {
    const view = runStateView("done", null);
    expect(view.unknown).toBe(true);
    expect(view.label).toContain("未知");
    expect(view.label).not.toBe("已完成");
  });

  it("done with evidence renders as completed and keeps the evidence", () => {
    const view = runStateView("done", evidence);
    expect(view.unknown).toBe(false);
    expect(view.label).toBe("已完成");
    expect(view.evidence).toBe(evidence);
  });

  it("error and canceled without evidence also degrade to unknown", () => {
    for (const state of ["error", "canceled"] as const) {
      const view = runStateView(state, null);
      expect(view.unknown).toBe(true);
      expect(view.label).toContain("未知");
    }
  });

  it("running and queued render without evidence claims", () => {
    expect(runStateView("running", null).label).toBe("运行中");
    expect(runStateView("queued", null).label).toBe("排队中");
  });
});

describe("agentState presence", () => {
  it("gone requires authoritative evidence", () => {
    expect(presenceView("gone", null).unknown).toBe(true);
    const view = presenceView("gone", evidence);
    expect(view.unknown).toBe(false);
    expect(view.label).toBe("已消失");
  });

  it("replaced and exited require evidence", () => {
    expect(presenceView("replaced", null).unknown).toBe(true);
    expect(presenceView("exited", null).unknown).toBe(true);
    expect(presenceView("replaced", evidence).label).toBe("已被替换");
    expect(presenceView("exited", evidence).label).toBe("已退出");
  });

  it("unverifiable stays visible as unverifiable, never collapsing into gone", () => {
    const withEvidence = presenceView("unverifiable", evidence);
    const withoutEvidence = presenceView("unverifiable", null);
    expect(withEvidence.label).toBe("无法验证");
    expect(withoutEvidence.label).toBe("无法验证（无证据）");
    expect(withoutEvidence.label).not.toContain("已消失");
    expect(withoutEvidence.unknown).toBe(false);
  });

  it("nonpersistent is modelled separately", () => {
    expect(presenceView("nonpersistent", null).label).toBe("非持久目标");
  });
});

describe("agentState auto-write guard", () => {
  it("only attached and detached handles allow automatic writes", () => {
    expect(autoWriteAllowed("attached")).toBe(true);
    expect(autoWriteAllowed("detached")).toBe(true);
    for (const presence of [
      "disconnected",
      "gone",
      "replaced",
      "exited",
      "unverifiable",
      "nonpersistent",
    ] as const) {
      expect(autoWriteAllowed(presence)).toBe(false);
    }
  });
});

describe("agentState handle formatting", () => {
  it("formats handle@host:cwd and @handle mention tokens", () => {
    const ref = { handle: "build", host: "web-01", cwd: "/srv/app" };
    expect(handleAtHostCwd(ref)).toBe("build@web-01:/srv/app");
    expect(mentionToken(ref)).toBe("@build");
  });

  it("renders honestly unknown host or cwd without fabricating identity", () => {
    expect(handleAtHostCwd({ handle: "build", host: "", cwd: "" })).toBe("build");
    expect(handleAtHostCwd({ handle: "build", host: "web-01", cwd: "" })).toBe("build@web-01");
    expect(handleAtHostCwd({ handle: "build", host: "", cwd: "/srv/app" })).toBe("build");
  });

  it("validates handle names against mention-unsafe characters", () => {
    expect(isValidHandleName("build")).toBe(true);
    expect(isValidHandleName("build-2_x")).toBe(true);
    expect(isValidHandleName("2fast")).toBe(true);
    expect(isValidHandleName("")).toBe(false);
    expect(isValidHandleName("-leading")).toBe(false);
    expect(isValidHandleName("has space")).toBe(false);
    expect(isValidHandleName("at@sign")).toBe(false);
    expect(isValidHandleName("colon:name")).toBe(false);
  });

  it("formats evidence with its summary", () => {
    expect(formatEvidence(evidence)).toContain("输出锚点匹配");
  });
});
