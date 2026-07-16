export interface BootRuntime {
  stage: (name: string, detail?: string) => void;
  fail: (label: string, detail: unknown) => void;
  complete: () => void;
  clearAppCache: () => void;
  diagnosticId: string;
}

declare global {
  interface Window {
    __quartetBoot?: BootRuntime;
  }
}

export function markBootStage(name: string, detail?: string): void {
  window.__quartetBoot?.stage(name, detail);
}

export function reportBootFailure(label: string, detail: unknown): void {
  window.__quartetBoot?.fail(label, detail);
}

export function completeBoot(): void {
  window.__quartetBoot?.complete();
}
