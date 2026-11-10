import { beforeEach, describe, expect, it, vi } from "vitest";

const call = vi.hoisted(() => vi.fn());
vi.mock("../ipc/commands", () => ({ call }));

import { aiCommandApi } from "../ipc/aiCommandApi";

beforeEach(() => call.mockReset());

describe("AI command generation IPC", () => {
  it("sends the request fields to ai_command_generate and returns the result", async () => {
    const result = { command: "ls -la" };
    call.mockResolvedValueOnce(result);
    const request = { prompt: "list files", cwd: "/tmp", shell: "bash" };
    await expect(aiCommandApi.generate(request)).resolves.toBe(result);
    expect(call).toHaveBeenCalledWith("ai_command_generate", request);
  });
});
