// A request started under an older browser session must not be sent with a newer cookie.
let sessionEpoch = 0;

export function currentAuthSessionEpoch(): number {
  return sessionEpoch;
}

export function advanceAuthSessionEpoch(): void {
  sessionEpoch += 1;
}
