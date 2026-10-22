export function createTransferTaskId(): string {
  return globalThis.crypto.randomUUID();
}
