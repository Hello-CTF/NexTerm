// How often another device may take the PTY size from this view within the window before the view stops sending
// its size on its own.
export const SIZE_OVERRIDE_LIMIT = 3;
export const SIZE_OVERRIDE_WINDOW_MS = 30_000;

// SizeOverrideGuard ends a resize ping-pong between devices showing the same terminal. It counts the times another
// device took the size while this view owned it; after SIZE_OVERRIDE_LIMIT of them within SIZE_OVERRIDE_WINDOW_MS the
// view stops sending its size, even when the backend hands the size back, until the user takes it on purpose
// (reset). Like a client that stops reconnecting after a few failures, each device ends the loop on its own.
export class SizeOverrideGuard {
  private overrides: number[] = [];
  private owned = false;
  private engaged = false;

  constructor(private readonly now: () => number = Date.now) {}

  // note records the published size owner: owned when it is this view, elsewhere when another device owns it (not
  // nobody, briefly, between owners). It reports whether the view must stop sending its size.
  note(owned: boolean, elsewhere: boolean): boolean {
    if (this.owned && !owned && elsewhere && !this.engaged) {
      const at = this.now();
      this.overrides = this.overrides.filter((time) => at - time < SIZE_OVERRIDE_WINDOW_MS);
      this.overrides.push(at);
      if (this.overrides.length >= SIZE_OVERRIDE_LIMIT) this.engaged = true;
    }
    if (owned || elsewhere) this.owned = owned;
    return this.engaged;
  }

  suppressed(): boolean {
    return this.engaged;
  }

  // reset runs when the user takes the size on purpose: the view sends its size again and the count starts over.
  reset(): void {
    this.overrides = [];
    this.engaged = false;
  }
}
