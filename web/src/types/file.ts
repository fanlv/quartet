// One viewer state shape for every surface. `isImage` / `isPdf` are resolved
// by the loader so view components stay free of file-extension rules.
export interface FileViewerFile {
  path: string;
  name: string;
  content: string;
  size: number;
  truncated: boolean;
  binary: boolean;
  loading: boolean;
  isImage: boolean;
  isPdf: boolean;
  imageUrl?: string | null;
  /** Inline serve-file URL rendered by the browser's PDF viewer. */
  pdfUrl?: string | null;
  error?: string;
  line?: number;
  endLine?: number;
}
