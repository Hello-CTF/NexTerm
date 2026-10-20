/**
 * Minimal fakes for the xterm packages used by the share terminal components.
 * The route tests assert routing, connection, and rendered text; they do not
 * assert terminal emulation, so the real canvas/WebGL stack (which jsdom only
 * serves through slow fallbacks) is replaced with no-op doubles.
 */

export class FakeTerminal {
  options: { disableStdin?: boolean };

  constructor(options: { disableStdin?: boolean }) {
    this.options = options;
  }

  loadAddon(_addon: unknown) {}
  open(_host: HTMLElement) {}

  onData(_callback: (data: string) => void) {
    return { dispose() {} };
  }

  onSelectionChange(_callback: () => void) {
    return { dispose() {} };
  }

  getSelection() {
    return "";
  }

  write(_bytes: Uint8Array) {}
  reset() {}
  focus() {}
  dispose() {}
}

export class FakeFitAddon {
  fit() {}
}

export class FakeWebglAddon {}

export function fakeSelectionAutoCopy(_config: unknown) {
  return {
    notifySelectionChanged() {},
    dispose() {},
  };
}
