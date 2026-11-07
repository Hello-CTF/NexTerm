// Which parts of the asset form each kind shows, and the defaults a kind brings. The editor reads this table instead
// of repeating kind comparisons, so a new protocol is one row.

export type AssetFormKind =
  | "ssh"
  | "winrm"
  | "local"
  | "docker"
  | "docker-context"
  | "kubernetes"
  | "mysql"
  | "postgres"
  | "redis"
  | "rdp"
  | "vnc";

export type ViaTarget = "ssh" | "ssh-or-device";

export interface AssetKindFields {
  defaultPort: number;
  // The host and port inputs and the port probe.
  network: boolean;
  username: boolean;
  // The auth-method select and the key sections; protocols with only a stored password skip them.
  authMethod: boolean;
  // A password credential can be stored for the asset.
  password: boolean;
  // Connects through another asset; the value says which assets qualify.
  via: ViaTarget | null;
  // The SSH advanced section (terminal mode, jump host, env, ...).
  sshAdvanced: boolean;
  // Protocol-specific option groups.
  remote: "rdp" | "vnc" | null;
  // The optional Wake-on-LAN MAC and broadcast options.
  wake: boolean;
}

const NONE: AssetKindFields = {
  defaultPort: 22,
  network: true,
  username: true,
  authMethod: true,
  password: false,
  via: null,
  sshAdvanced: false,
  remote: null,
  wake: false,
};

const DATABASE: AssetKindFields = { ...NONE, authMethod: false, password: true, via: "ssh" };

export const ASSET_KIND_FIELDS: Record<AssetFormKind, AssetKindFields> = {
  ssh: { ...NONE, sshAdvanced: true, wake: true },
  winrm: { ...NONE, defaultPort: 5985, wake: true },
  local: { ...NONE, network: false, username: false, authMethod: false },
  docker: { ...NONE, sshAdvanced: true, wake: true },
  "docker-context": NONE,
  kubernetes: NONE,
  mysql: { ...DATABASE, defaultPort: 3306 },
  postgres: { ...DATABASE, defaultPort: 5432 },
  redis: { ...DATABASE, defaultPort: 6379 },
  rdp: { ...DATABASE, defaultPort: 3389, via: "ssh-or-device", remote: "rdp", wake: true },
  vnc: { ...DATABASE, defaultPort: 5900, username: false, via: "ssh-or-device", remote: "vnc", wake: true },
};

export function assetKindFields(kind: string): AssetKindFields {
  return ASSET_KIND_FIELDS[kind as AssetFormKind] ?? NONE;
}

export function isRemoteDesktopKind(kind: string): kind is "rdp" | "vnc" {
  return kind === "rdp" || kind === "vnc";
}

// Options the remote desktop form edits. The backend reads viaAssetId, promptPassword, domain, width and height;
// clipboard, clipboardAutoSync, commandAsCtrl and viewOnly are read by the client.
export interface RemoteDesktopOptions {
  domain: string;
  promptPassword: boolean;
  clipboard: boolean;
  // Paste sends the local clipboard first, and a remote copy reaches the local clipboard while the tab is in front.
  clipboardAutoSync: boolean;
  // On a Mac, Command is sent as Ctrl; see defaultCommandAsCtrl.
  commandAsCtrl: boolean;
  viewOnly: boolean;
  resolution: "follow" | "fixed";
  width: number;
  height: number;
}

export const DEFAULT_RESOLUTION = { width: 1920, height: 1080 };

// defaultCommandAsCtrl is the Command-as-Ctrl setting an asset has until it is changed: on for RDP, whose remote
// side is Windows, and off for VNC, whose remote side may be a Mac.
export function defaultCommandAsCtrl(kind: string): boolean {
  return kind === "rdp";
}

function positiveInt(value: unknown): number {
  return typeof value === "number" && Number.isInteger(value) && value > 0 ? value : 0;
}

export function readRemoteDesktopOptions(kind: string, options: Record<string, unknown> | undefined): RemoteDesktopOptions {
  const raw = options ?? {};
  const width = positiveInt(raw.width);
  const height = positiveInt(raw.height);
  const fixed = width > 0 && height > 0;
  return {
    domain: typeof raw.domain === "string" ? raw.domain : "",
    promptPassword: raw.promptPassword === true,
    clipboard: raw.clipboard !== false,
    clipboardAutoSync: raw.clipboardAutoSync !== false,
    commandAsCtrl: typeof raw.commandAsCtrl === "boolean" ? raw.commandAsCtrl : defaultCommandAsCtrl(kind),
    // A VNC session starts read-only only when asked; the default is a normal session.
    viewOnly: raw.viewOnly === true,
    resolution: kind === "rdp" && fixed ? "fixed" : "follow",
    width: fixed ? width : DEFAULT_RESOLUTION.width,
    height: fixed ? height : DEFAULT_RESOLUTION.height,
  };
}

// writeRemoteDesktopOptions applies the form values on top of the asset's existing options and drops the keys of
// the other protocol, so editing a host never loses an option this form does not own.
export function writeRemoteDesktopOptions(
  kind: "rdp" | "vnc",
  base: Record<string, unknown>,
  values: RemoteDesktopOptions,
): Record<string, unknown> {
  const options = { ...base };
  delete options.domain;
  delete options.width;
  delete options.height;
  delete options.promptPassword;
  delete options.clipboard;
  delete options.clipboardAutoSync;
  delete options.commandAsCtrl;
  delete options.viewOnly;
  if (values.promptPassword) options.promptPassword = true;
  if (!values.clipboard) options.clipboard = false;
  if (!values.clipboardAutoSync) options.clipboardAutoSync = false;
  if (values.commandAsCtrl !== defaultCommandAsCtrl(kind)) options.commandAsCtrl = values.commandAsCtrl;
  if (kind === "rdp") {
    if (values.domain.trim()) options.domain = values.domain.trim();
    if (values.resolution === "fixed") {
      options.width = values.width;
      options.height = values.height;
    }
  } else if (values.viewOnly) {
    options.viewOnly = true;
  }
  return options;
}

export function validateResolution(width: number, height: number): string | null {
  if (!Number.isInteger(width) || !Number.isInteger(height)) return "分辨率必须是整数";
  if (width < 640 || height < 480) return "分辨率不能小于 640 x 480";
  if (width > 8192 || height > 8192) return "分辨率不能大于 8192 x 8192";
  return null;
}
