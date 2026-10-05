export const SERVER_TOKEN_HEADER = "X-NexTerm-Sync-Token";

const WS_AUTH_PROTOCOL = "nexterm";

let serverToken: string | null = null;
let prompting: Promise<string | null> | null = null;
let prompterOverride: (() => Promise<string | null>) | null = null;

export function getServerToken(): string | null {
  return serverToken;
}

export function setServerToken(token: string | null): void {
  const trimmed = token?.trim() ?? "";
  serverToken = trimmed === "" ? null : trimmed;
}

export function clearServerToken(): void {
  serverToken = null;
}

export function registerServerTokenPrompter(fn: (() => Promise<string | null>) | null): void {
  prompterOverride = fn;
}

export function authHeaders(): Record<string, string> {
  return serverToken ? { [SERVER_TOKEN_HEADER]: serverToken } : {};
}

export function wsAuthProtocols(): string[] {
  return serverToken ? [WS_AUTH_PROTOCOL, serverToken] : [WS_AUTH_PROTOCOL];
}

export async function ensureServerToken(): Promise<string> {
  if (serverToken) return serverToken;
  const entered = await promptOnce();
  if (!entered) {
    throw { code: "forbidden", message: "未提供服务器访问令牌" };
  }
  setServerToken(entered);
  return serverToken as string;
}

function promptOnce(): Promise<string | null> {
  if (!prompting) {
    prompting = (async () => {
      if (prompterOverride) return prompterOverride();
      const { promptText } = await import("../ui/dialogs");
      return promptText(
        "服务器要求访问令牌。请在服务器上运行 nexterm-server token 查看同步令牌，或向管理员索取。",
        "",
        { secret: true },
      );
    })().finally(() => {
      prompting = null;
    });
  }
  return prompting;
}

function withAuth(init: RequestInit): RequestInit {
  return { ...init, headers: { ...(init.headers as Record<string, string> | undefined), ...authHeaders() } };
}

export async function authedFetch(input: string, init: RequestInit = {}): Promise<Response> {
  let response = await fetch(input, withAuth(init));
  if (response.status !== 401) return response;
  clearServerToken();
  let token: string | null = null;
  try {
    token = await ensureServerToken();
  } catch {
    token = null;
  }
  if (!token) return response;
  response = await fetch(input, withAuth(init));
  if (response.status === 401) clearServerToken();
  return response;
}
