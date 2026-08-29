import { useCallback, useEffect, useRef, useState } from 'react';
import type { FileViewerFile } from '../types';
import { fetchFileAsBlobUrl, fileNameFromPath, isImageFile, readFile } from '../utils/file';

export interface OpenFileOptions {
  /** Display name; derived from the path when omitted. */
  name?: string;
  /** Known size (file browser has it from the listing) — avoids a blank badge while loading. */
  size?: number;
  /** 1-based line range to highlight and scroll to. */
  line?: number;
  endLine?: number;
}

// Owns the viewer's load lifecycle: text via read-file, images via
// serve-file blobs, stale-response guarding when the user clicks another
// file mid-flight, and object-URL revocation. Every viewer surface shares
// it so behaviour can't drift between them.
export function useFileViewer(jobId?: string) {
  const [file, setFile] = useState<FileViewerFile | null>(null);
  const requestedPathRef = useRef<string>('');
  const imageUrlRef = useRef<string | null>(null);

  const revokeImageUrl = useCallback(() => {
    if (imageUrlRef.current) {
      URL.revokeObjectURL(imageUrlRef.current);
      imageUrlRef.current = null;
    }
  }, []);

  useEffect(() => revokeImageUrl, [revokeImageUrl]);

  const close = useCallback(() => {
    requestedPathRef.current = '';
    revokeImageUrl();
    setFile(null);
  }, [revokeImageUrl]);

  const open = useCallback(async (path: string, options: OpenFileOptions = {}) => {
    const { name = fileNameFromPath(path), size = 0, line, endLine } = options;
    const isImage = isImageFile(name);
    requestedPathRef.current = path;
    revokeImageUrl();
    setFile({
      path,
      name,
      content: '',
      size,
      truncated: false,
      binary: false,
      loading: !isImage,
      isImage,
      imageUrl: null,
      line,
      endLine,
    });

    // Stale guard: only the most recently requested path may commit state.
    const commit = (patch: Partial<FileViewerFile>) => {
      if (requestedPathRef.current !== path) return false;
      setFile((prev) => (prev && prev.path === path ? { ...prev, ...patch } : prev));
      return true;
    };

    if (isImage) {
      try {
        const url = await fetchFileAsBlobUrl(path);
        if (!commit({ imageUrl: url })) {
          URL.revokeObjectURL(url);
          return;
        }
        imageUrlRef.current = url;
      } catch (error) {
        commit({ loading: false, isImage: false, error: error instanceof Error ? error.message : String(error) });
      }
      return;
    }

    try {
      const data = await readFile(path, jobId);
      commit({
        content: data.content,
        size: data.size || size,
        truncated: data.truncated,
        binary: data.binary,
        loading: false,
      });
    } catch (error) {
      commit({ loading: false, error: error instanceof Error ? error.message : String(error) });
    }
  }, [jobId, revokeImageUrl]);

  return { file, open, close };
}
