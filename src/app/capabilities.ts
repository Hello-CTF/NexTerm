let mountUnavailableReason_: string | null = null;

export function mountUnavailableReason(): string | null {
  return mountUnavailableReason_;
}

export function setMountUnavailableReason(reason: string | null | undefined): void {
  mountUnavailableReason_ = reason ?? null;
}
