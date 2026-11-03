/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  insert: vi.fn(async (_s: unknown) => true),
  execute: vi.fn(async (_s: unknown) => true),
}));

vi.mock("../../features/explorer/snippetInsert", async (importOriginal) => {
  const original = await importOriginal<typeof import("../../features/explorer/snippetInsert")>();
  return { ...original, insertSnippet: mocks.insert, executeSnippet: mocks.execute };
});

import { useJumpCommands, type JumpCommand } from "../../app/jump/useJumpCommands";
import type { SnippetDto } from "../../ipc/types";

function snippet(id: string, steps: SnippetDto["steps"]): SnippetDto {
  return { id, groupId: null, name: `片段${id}`, body: "echo hi", sort: 0, createdAt: 1, updatedAt: 1, description: "", steps };
}

let mounted: MountedView | undefined;

function commands(snippets: SnippetDto[]): JumpCommand[] {
  let out: JumpCommand[] = [];
  function Probe() {
    out = useJumpCommands({ assets: [], snippets, onShowHosts: () => undefined });
    return null;
  }
  mounted = mountWithClient(createElement(Probe));
  return out;
}

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  mocks.insert.mockClear();
  mocks.execute.mockClear();
});

describe("jump palette snippets", () => {
  it("inserts a single-command snippet", () => {
    const cmd = commands([snippet("a", [])]).find((c) => c.id === "snippet-a");
    expect(cmd?.label).toBe("插入片段 片段a");
    cmd?.run();
    expect(mocks.insert).toHaveBeenCalledTimes(1);
    expect(mocks.execute).not.toHaveBeenCalled();
  });

  it("runs a multi-step snippet through the step-by-step executor instead of inserting it", () => {
    const steps = [{ name: "一", command: "ls", confirm: true }, { name: "二", command: "pwd", confirm: false }] as SnippetDto["steps"];
    const cmd = commands([snippet("m", steps)]).find((c) => c.id === "snippet-m");
    expect(cmd?.label).toBe("执行片段 片段m");
    cmd?.run();
    expect(mocks.execute).toHaveBeenCalledTimes(1);
    expect(mocks.insert).not.toHaveBeenCalled();
  });
});
