export interface WorkspaceInfo {
  id: string;
  title: string;
  description: string;
  workdir: string;
  defaultAgent?: string;
  defaultModel?: string;
  color?: string;
  favorite?: boolean;
  sortOrder?: number;
}

export interface WorkspaceRecord extends WorkspaceInfo {
  version: number;
  favorite: boolean;
  sortOrder: number;
}
