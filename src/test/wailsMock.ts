import { vi } from "vitest";

const bindingNames: Record<number, string> = {
  2349242617: "main.Service.Call",
  3490772380: "main.Service.ConsumeDeepLinks",
};

export function createWailsMock() {
  const byName = vi.fn();
  const byID = vi.fn((id: number, ...args: unknown[]) => {
    const name = bindingNames[id];
    if (!name) throw new Error(`unknown Wails binding ID: ${id}`);
    return byName(name, ...args);
  });
  return {
    Call: { ByName: byName, ByID: byID },
    Events: { On: vi.fn() },
    Dialogs: {
      Info: vi.fn(),
      Warning: vi.fn(),
      Error: vi.fn(),
      Question: vi.fn(),
      OpenFile: vi.fn(),
      SaveFile: vi.fn(),
    },
    Window: {
      Minimise: vi.fn(),
      ToggleMaximise: vi.fn(),
      Close: vi.fn(),
    },
  };
}

export type WailsMock = ReturnType<typeof createWailsMock>;
