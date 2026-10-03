export const TERMINAL_GRID_MAX = 1024;

export interface TerminalGrid {
  cols: number;
  rows: number;
}

export interface TerminalViewport {
  widthPx: number;
  heightPx: number;
}

export interface TerminalCellMetrics {
  widthPx: number;
  heightPx: number;
}

export interface TerminalGridRuntimeAdapter {
  resize: (tabId: string, grid: TerminalGrid) => Promise<void>;
  flush?: (tabId: string) => Promise<void>;
}

export interface TerminalGridSnapshot {
  tabId: string;
  desired: TerminalGrid | null;
  committed: TerminalGrid | null;
  visible: boolean;
  canResize: boolean;
  inFlight: boolean;
  pending: boolean;
  generation: number;
  observedRevision: number;
  closed: boolean;
}

interface GridRequest {
  tabId: string;
  grid: TerminalGrid;
  generation: number;
  force: boolean;
  final: boolean;
}

function sameGrid(a: TerminalGrid | null, b: TerminalGrid | null): boolean {
  return a === b || (!!a && !!b && a.cols === b.cols && a.rows === b.rows);
}

function fitAxis(pixels: number, cell: number): number {
  const ratio = pixels / cell;
  if (ratio < 1) return 1;
  if (!Number.isFinite(ratio) || ratio >= TERMINAL_GRID_MAX) return TERMINAL_GRID_MAX;
  return Math.floor(ratio);
}

export function validTerminalGrid(grid: TerminalGrid): boolean {
  return (
    Number.isInteger(grid.cols) &&
    Number.isInteger(grid.rows) &&
    grid.cols > 0 &&
    grid.rows > 0 &&
    grid.cols <= TERMINAL_GRID_MAX &&
    grid.rows <= TERMINAL_GRID_MAX
  );
}

export function gridForViewport(
  viewport: TerminalViewport,
  metrics: TerminalCellMetrics,
): TerminalGrid | null {
  if (
    viewport.widthPx <= 0 ||
    viewport.heightPx <= 0 ||
    !Number.isFinite(viewport.widthPx) ||
    !Number.isFinite(viewport.heightPx)
  ) {
    return null;
  }
  if (
    metrics.widthPx <= 0 ||
    metrics.heightPx <= 0 ||
    !Number.isFinite(metrics.widthPx) ||
    !Number.isFinite(metrics.heightPx)
  ) {
    return null;
  }
  return {
    cols: fitAxis(viewport.widthPx, metrics.widthPx),
    rows: fitAxis(viewport.heightPx, metrics.heightPx),
  };
}

export class TerminalGridCoordinator {
  private tabId = "";
  private desired: TerminalGrid | null = null;
  private committed: TerminalGrid | null = null;
  private visible: boolean;
  private canResize: boolean;
  private generation = 0;
  private observedRevision = 0;
  private observedGrid: TerminalGrid | null = null;
  private inFlight: GridRequest | null = null;
  private pending: GridRequest | null = null;
  private forceOnNextMeasurement = false;
  private closed = false;

  constructor(
    private readonly runtime: TerminalGridRuntimeAdapter,
    private readonly applyLocal: (grid: TerminalGrid) => void,
    options: { visible: boolean; canResize: boolean },
  ) {
    this.visible = options.visible;
    this.canResize = options.canResize;
  }

  desiredGrid(): TerminalGrid | null {
    return this.desired;
  }

  snapshot(): TerminalGridSnapshot {
    return {
      tabId: this.tabId,
      desired: this.desired,
      committed: this.committed,
      visible: this.visible,
      canResize: this.canResize,
      inFlight: this.inFlight !== null,
      pending: this.pending !== null,
      generation: this.generation,
      observedRevision: this.observedRevision,
      closed: this.closed,
    };
  }

  update(viewport: TerminalViewport, metrics: TerminalCellMetrics): TerminalGrid | null {
    if (this.closed || !this.visible) return null;
    const grid = gridForViewport(viewport, metrics);
    if (!grid) return null;
    const changed = !sameGrid(this.desired, grid);
    this.desired = grid;
    const force = this.forceOnNextMeasurement;
    this.forceOnNextMeasurement = false;
    if (this.canResize) {
      this.applyLocal(grid);
      if (changed || force || !sameGrid(this.committed, grid)) this.schedule(force, false);
    }
    return grid;
  }

  setVisible(visible: boolean): void {
    if (this.closed || this.visible === visible) return;
    this.visible = visible;
    this.pending = null;
    if (visible) this.forceOnNextMeasurement = true;
  }

  setCanResize(canResize: boolean): void {
    if (this.closed || this.canResize === canResize) return;
    this.canResize = canResize;
    this.pending = null;
    if (canResize && this.visible && this.desired) {
      this.applyLocal(this.desired);
      this.schedule(true, false);
    }
  }

  attach(tabId: string, initialGrid: TerminalGrid | null, synchronize: boolean): void {
    if (this.closed || !tabId) return;
    this.generation += 1;
    this.tabId = tabId;
    this.committed = initialGrid;
    this.pending = null;
    if (synchronize && this.visible && this.canResize && this.desired) {
      this.schedule(true, false);
    }
  }

  observe(revision: number, grid: TerminalGrid): boolean {
    if (this.closed || this.canResize || revision <= 0 || !validTerminalGrid(grid)) return false;
    if (
      revision < this.observedRevision ||
      (revision === this.observedRevision &&
        (this.committed !== null || !sameGrid(this.observedGrid, grid)))
    ) {
      return false;
    }
    this.observedRevision = revision;
    this.observedGrid = grid;
    this.committed = grid;
    this.applyLocal(grid);
    return true;
  }

  flush(): void {
    if (!this.closed && this.visible && this.canResize && this.desired) {
      this.schedule(true, true);
    }
  }

  close(): void {
    this.closed = true;
    this.generation += 1;
    this.tabId = "";
    this.pending = null;
    this.committed = null;
  }

  private schedule(force: boolean, final: boolean): void {
    if (this.closed || !this.visible || !this.canResize || !this.tabId || !this.desired) return;
    const pending = this.pending;
    this.pending = {
      tabId: this.tabId,
      grid: this.desired,
      generation: this.generation,
      force: force || pending?.force === true,
      final: final || pending?.final === true,
    };
    this.pump();
  }

  private pump(): void {
    if (this.inFlight || !this.pending || this.closed) return;
    const request = this.pending;
    this.pending = null;
    if (
      request.generation !== this.generation ||
      request.tabId !== this.tabId ||
      !this.visible ||
      !this.canResize
    ) {
      return;
    }
    if (!request.force && sameGrid(this.committed, request.grid)) return;
    this.inFlight = request;
    void this.run(request);
  }

  private async run(request: GridRequest): Promise<void> {
    try {
      await this.runtime.resize(request.tabId, request.grid);
      if (request.final && this.runtime.flush) await this.runtime.flush(request.tabId);
      if (
        request.generation === this.generation &&
        request.tabId === this.tabId &&
        this.visible &&
        this.canResize &&
        !this.closed
      ) {
        this.committed = request.grid;
      }
    } catch {
      // 后续测量、重连或显式 flush 会按最新目标重试。
    } finally {
      if (this.inFlight === request) {
        this.inFlight = null;
        this.pump();
      }
    }
  }
}
