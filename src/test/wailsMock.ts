import { vi } from "vitest";

export function createWailsMock() {
  return {
    Call: { ByName: vi.fn() },
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
