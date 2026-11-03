export interface LocalForwardRequest {
  sessionId: string;
  host: string;
  port: number;
}

export interface TerminalIntegrations {
  // Opens a URL inside the app instead of the system browser.
  openInApp?: (url: string) => void;
  createLocalForward?: (request: LocalForwardRequest) => void | Promise<void>;
}

let registered: TerminalIntegrations = {};

// Features that own an action register it here; the terminal only calls what is present.
// Returns a function that removes exactly what this call registered.
export function registerTerminalIntegrations(partial: TerminalIntegrations): () => void {
  const added = { ...partial };
  registered = { ...registered, ...added };
  return () => {
    const next = { ...registered };
    for (const key of Object.keys(added) as (keyof TerminalIntegrations)[]) {
      if (next[key] === added[key]) delete next[key];
    }
    registered = next;
  };
}

export function terminalIntegrations(): TerminalIntegrations {
  return registered;
}
