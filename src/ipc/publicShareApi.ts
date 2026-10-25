export type PublicSharePermission = "read" | "read_write";

export interface PublicShareReady {
  sessionId: string;
  permission: PublicSharePermission;
  expiresAt: number;
}

export interface PublicShareFailure {
  code: string;
  message: string;
}

export interface PublicShareCloseInfo {
  code: number;
  reason: string;
  wasClean: boolean;
}

export interface PublicShareHandlers {
  onReady: (ready: PublicShareReady) => void;
  onOutput: (bytes: Uint8Array) => void;
  onError: (failure: PublicShareFailure) => void;
  onClose: (info: PublicShareCloseInfo) => void;
}
