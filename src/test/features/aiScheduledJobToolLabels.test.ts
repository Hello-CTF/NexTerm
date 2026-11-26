import { describe, expect, it } from "vitest";
import { toolLabel } from "../../features/ai/panel/aiConfirmLabels";

describe("scheduled job tool labels", () => {
  it("names the scheduled job tools in words instead of raw tool names", () => {
    expect(toolLabel("schedule_job")).toBe("创建定时任务");
    expect(toolLabel("list_scheduled_jobs")).toBe("查看定时任务");
    expect(toolLabel("cancel_scheduled_job")).toBe("删除定时任务");
  });
});
