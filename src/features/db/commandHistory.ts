// CommandHistory is an in-memory shell-style history: older() and newer() walk
// the entries, and walking past the newest entry restores the unsent draft.
export class CommandHistory {
  private entries: string[] = [];
  private index = 0;
  private draft = "";

  constructor(private readonly limit = 100) {}

  push(command: string): void {
    if (command && this.entries.at(-1) !== command) {
      this.entries.push(command);
      if (this.entries.length > this.limit) this.entries.shift();
    }
    this.index = this.entries.length;
    this.draft = "";
  }

  // older returns the previous entry, or null when there is none. current is
  // the input text, kept as the draft when leaving it.
  older(current: string): string | null {
    if (this.index === 0) return null;
    if (this.index === this.entries.length) this.draft = current;
    this.index--;
    return this.entries[this.index];
  }

  newer(): string | null {
    if (this.index >= this.entries.length) return null;
    this.index++;
    return this.index === this.entries.length ? this.draft : this.entries[this.index];
  }
}
