// One viewer state shape for every surface. `isImage` is resolved by the
// loader so view components stay free of file-extension rules.
export interface FileViewerFile {
  path: string;
  name: string;
  content: string;
  size: number;
  truncated: boolean;
  binary: boolean;
  loading: boolean;
  isImage: boolean;
  imageUrl?: string | null;
  error?: string;
  line?: number;
  endLine?: number;
}
