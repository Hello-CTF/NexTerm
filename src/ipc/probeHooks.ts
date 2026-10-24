// Counting hooks for the debug-capture performance probe. They stay null (a single null check per call)
// unless a capture is running, and only ever see command or event names, never arguments or payloads.
type NameSink = (name: string) => void;

let ipcSink: NameSink | null = null;
let eventSink: NameSink | null = null;

export function setProbeSinks(ipc: NameSink | null, event: NameSink | null): void {
  ipcSink = ipc;
  eventSink = event;
}

export function noteIpcCall(name: string): void {
  ipcSink?.(name);
}

export function noteEvent(name: string): void {
  eventSink?.(name);
}
