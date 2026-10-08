import { useState, useCallback, useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import './FileBrowser.css';
import { useIsMobile } from '../hooks/useIsMobile';
import { useFileViewer } from '../hooks/useFileViewer';
import { copyToClipboard } from '../utils/clipboard';
import { downloadWorkspaceFile, formatFileSize } from '../utils/file';
import { showToast } from '../utils/toast';
import { FileViewer } from './FileViewer/FileViewer';

interface FileEntry {
  name: string;
  size: number;
  modTime: string;
}

interface DirNode {
  name: string;
  path: string;
  dirs: DirNode[];
  files: FileEntry[];
  loaded: boolean;
  expanded: boolean;
}

interface DirContents {
  dirs: string[];
  files: FileEntry[];
  current: string;
}

function directoryNode(path: string, contents?: DirContents): DirNode {
  return {
    name: path.split('/').filter(Boolean).pop() || '/',
    path,
    dirs: (contents?.dirs || []).map((name) => directoryNode(path === '/' ? '/' + name : path + '/' + name)),
    files: contents?.files || [],
    loaded: !!contents,
    expanded: !!contents,
  };
}

// Deep-update a node in the tree by path, returning a new root (immutable
// update). Module-scoped so the recursive self-reference is statically
// resolvable and React hook deps don't churn on every render.
function updateNode(root: DirNode, targetPath: string, updater: (n: DirNode) => DirNode): DirNode {
  if (root.path === targetPath) return updater(root);
  return {
    ...root,
    dirs: root.dirs.map((d) =>
      targetPath.startsWith(d.path + '/') || targetPath === d.path
        ? updateNode(d, targetPath, updater)
        : d
    ),
  };
}

interface FileBrowserProps {
  rootPath: string;
  jobId?: string;
  onClose: () => void;
  onFileSelect?: (path: string) => void;
  selectedPath?: string;
}

async function fetchDirContents(path: string, signal?: AbortSignal): Promise<DirContents> {
  const params = path ? `?path=${encodeURIComponent(path)}&showFiles=true` : '?showFiles=true';
  const endpoint = `/api/v1/list-dir${params}`;
  const res = await fetch(endpoint, { signal });
  const rawBody = await res.text();
  if (!res.ok) {
    throw new Error(`GET ${endpoint} returned HTTP ${res.status}${res.statusText ? ` ${res.statusText}` : ''}${rawBody ? `\n${rawBody}` : ''}`);
  }
  let data: { code?: number; dirs?: string[]; files?: FileEntry[]; current: string };
  try {
    data = JSON.parse(rawBody);
  } catch (reason) {
    throw new Error(`GET ${endpoint} returned invalid JSON\n${rawBody}`, { cause: reason });
  }
  if (data.code !== 0) throw new Error(`GET ${endpoint} returned code ${String(data.code)}\n${rawBody}`);
  return { dirs: data.dirs || [], files: data.files || [], current: data.current };
}

export function FileBrowser({ rootPath, jobId, onClose, onFileSelect, selectedPath }: FileBrowserProps) {
  const { t } = useTranslation();
  const [root, setRoot] = useState<DirNode | null>(null);
  const [error, setError] = useState('');
  const [locatingPath, setLocatingPath] = useState(selectedPath);
  const selectedFileRef = useRef<HTMLDivElement>(null);
  const loadedRootPath = root?.path;
  const { file: viewingFile, open: openViewer, close: closeViewer } = useFileViewer(jobId);
  const [panelWidth, setPanelWidth] = useState(420);
  const [downloadingPath, setDownloadingPath] = useState('');
  const resizing = useRef(false);
  const resizeCleanup = useRef<(() => void) | null>(null);

  // Clean up resize listeners on unmount to prevent leaks
  useEffect(() => {
    return () => { resizeCleanup.current?.(); };
  }, []);
  const panelRef = useRef<HTMLDivElement>(null);
  const isMobile = useIsMobile();

  // Load root directory on mount
  useEffect(() => {
    const controller = new AbortController();
    setRoot(null);
    setError('');
    const loadRoot = async () => {
      const data = await fetchDirContents(rootPath || '', controller.signal);
      if (!controller.signal.aborted) {
        setRoot(directoryNode(data.current, data));
      }
    };
    void loadRoot().catch((reason: unknown) => {
      if (!controller.signal.aborted) setError(reason instanceof Error ? reason.stack || reason.message : String(reason));
    });
    return () => controller.abort();
  }, [rootPath]);

  // Locate once per selection or root load, so manually collapsing a folder
  // after locating the file does not immediately reopen it.
  useEffect(() => {
    setLocatingPath(selectedPath);
  }, [selectedPath, loadedRootPath]);

  useEffect(() => {
    if (!root || !locatingPath || locatingPath !== selectedPath) return;
    const prefix = root.path === '/' ? '/' : root.path.replace(/\/+$/, '') + '/';
    if (!locatingPath.startsWith(prefix)) {
      setLocatingPath(undefined);
      return;
    }

    let node = root;
    const parents = locatingPath.slice(prefix.length).split('/').slice(0, -1);
    for (const name of parents) {
      const dir = node.dirs.find((child) => child.name === name);
      if (!dir) {
        setLocatingPath(undefined);
        return;
      }
      if (!dir.loaded) {
        const controller = new AbortController();
        void fetchDirContents(dir.path, controller.signal).then((data) => {
          if (controller.signal.aborted) return;
          setRoot((current) => current ? updateNode(current, dir.path, (child) => (
            child.loaded ? { ...child, expanded: true } : directoryNode(dir.path, data)
          )) : current);
        }).catch((reason: unknown) => {
          if (controller.signal.aborted) return;
          setError(reason instanceof Error ? reason.stack || reason.message : String(reason));
          setLocatingPath(undefined);
        });
        return () => controller.abort();
      }
      if (!dir.expanded) {
        setRoot((current) => current ? updateNode(current, dir.path, (child) => ({ ...child, expanded: true })) : current);
        return;
      }
      node = dir;
    }

    selectedFileRef.current?.scrollIntoView({ block: 'center', inline: 'nearest' });
    setLocatingPath(undefined);
  }, [root, locatingPath, selectedPath]);

  const toggleDir = useCallback(async (node: DirNode) => {
    if (node.expanded) {
      setRoot((r) => r ? updateNode(r, node.path, (n) => ({ ...n, expanded: false })) : r);
      return;
    }

    if (!node.loaded) {
      setError('');
      try {
        const data = await fetchDirContents(node.path);
        setRoot((r) => r ? updateNode(r, node.path, (n) => (
          n.loaded ? { ...n, expanded: true } : directoryNode(node.path, data)
        )) : r);
        return;
      } catch (reason) {
        setError(reason instanceof Error ? reason.stack || reason.message : String(reason));
        return;
      }
    }
    setRoot((r) => r ? updateNode(r, node.path, (n) => ({ ...n, expanded: true })) : r);
  }, []);

  // Images vs text, stale-response guarding and blob lifecycle all live in
  // useFileViewer, shared with the chat message viewer.
  const openFile = useCallback((dirPath: string, file: FileEntry) => {
    const fullPath = dirPath === '/' ? '/' + file.name : dirPath + '/' + file.name;
    if (onFileSelect) {
      onFileSelect(fullPath);
      return;
    }
    void openViewer(fullPath, { name: file.name, size: file.size });
  }, [onFileSelect, openViewer]);

  // Resize logic
  const handleResizeStart = useCallback((e: React.MouseEvent) => {
    e.preventDefault();
    resizing.current = true;
    const startX = e.clientX;
    const startWidth = panelWidth;

    const onMove = (ev: MouseEvent) => {
      if (!resizing.current) return;
      const diff = startX - ev.clientX;
      const newWidth = Math.min(Math.max(startWidth + diff, 300), window.innerWidth * 0.6);
      setPanelWidth(newWidth);
    };
    const onUp = () => {
      resizing.current = false;
      document.removeEventListener('mousemove', onMove);
      document.removeEventListener('mouseup', onUp);
      resizeCleanup.current = null;
    };
    document.addEventListener('mousemove', onMove);
    document.addEventListener('mouseup', onUp);
    resizeCleanup.current = () => {
      document.removeEventListener('mousemove', onMove);
      document.removeEventListener('mouseup', onUp);
    };
  }, [panelWidth]);

  const getRelativePath = (absPath: string) => {
    if (rootPath && absPath.startsWith(rootPath)) {
      const rel = absPath.slice(rootPath.length);
      return rel.startsWith('/') ? rel.slice(1) : rel;
    }
    return absPath;
  };

  const handleCopyRelativePath = (e: React.MouseEvent, absPath: string) => {
    e.stopPropagation();
    const rel = getRelativePath(absPath);
    copyToClipboard(rel).then(() => {
      showToast(t('fileBrowser.copiedRelativePath'));
    }).catch((reason: unknown) => {
      setError(reason instanceof Error ? reason.stack || reason.message : String(reason));
    });
  };

  const handleDownload = (e: React.MouseEvent, filePath: string, fileName: string) => {
    e.stopPropagation();
    if (downloadingPath) return;
    setDownloadingPath(filePath);
    setError('');
    void downloadWorkspaceFile(filePath, fileName).catch((reason: unknown) => {
      setError(reason instanceof Error ? reason.stack || reason.message : String(reason));
    }).finally(() => {
      setDownloadingPath((current) => (current === filePath ? '' : current));
    });
  };

  const renderTree = (node: DirNode, depth: number) => {
    const indent = depth * 16;
    return (
      <div key={node.path}>
        {/* Directories */}
        {node.dirs.map((dir) => (
          <div key={dir.path}>
            <div
              className="fb-tree-item"
              style={{ paddingLeft: indent + 10 }}
              onClick={() => toggleDir(dir)}
            >
              <svg className={`fb-arrow ${dir.expanded ? 'expanded' : ''}`} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                <path d="M9 18l6-6-6-6" />
              </svg>
              <svg className="fb-icon" viewBox="0 0 24 24" fill="none" stroke="#f59e0b" strokeWidth="2">
                <path d="M22 19a2 2 0 01-2 2H4a2 2 0 01-2-2V5a2 2 0 012-2h5l2 3h9a2 2 0 012 2z" />
              </svg>
              <span className="fb-name" title={dir.path}>{dir.name}</span>
              <span className="fb-row-actions">
                <button
                  type="button"
                  className="fb-row-action is-hover-only"
                  title={t('fileBrowser.copyRelativePath')}
                  aria-label={t('fileBrowser.copyRelativePath')}
                  onClick={(e) => handleCopyRelativePath(e, dir.path)}
                >
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" width="14" height="14">
                    <rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
                    <path d="M5 15H4a2 2 0 01-2-2V4a2 2 0 012-2h9a2 2 0 012 2v1" />
                  </svg>
                </button>
              </span>
            </div>
            {dir.expanded && dir.loaded && renderTree(dir, depth + 1)}
            {dir.expanded && !dir.loaded && (
              <div className="fb-tree-loading" style={{ paddingLeft: indent + 40 }}>{t('common.loading')}</div>
            )}
          </div>
        ))}
        {/* Files */}
        {node.files.map((file) => {
          const filePath = node.path === '/' ? '/' + file.name : node.path + '/' + file.name;
          return (
            <div
              key={filePath}
              ref={selectedPath === filePath ? selectedFileRef : undefined}
              className={`fb-tree-item ${(selectedPath ?? viewingFile?.path) === filePath ? 'active' : ''}`}
              style={{ paddingLeft: indent + 10 }}
              onClick={() => openFile(node.path, file)}
            >
              <svg className="fb-arrow placeholder" viewBox="0 0 24 24"><path /></svg>
              <svg className="fb-icon" viewBox="0 0 24 24" fill="none" stroke="#6b7280" strokeWidth="2">
                <path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z" />
                <polyline points="14 2 14 8 20 8" />
              </svg>
              <span className="fb-name" title={filePath}>{file.name}</span>
              <span className={`fb-row-actions${downloadingPath === filePath ? ' is-active' : ''}`}>
                <button
                  type="button"
                  className="fb-row-action is-hover-only"
                  title={t('fileBrowser.copyRelativePath')}
                  aria-label={t('fileBrowser.copyRelativePath')}
                  onClick={(e) => handleCopyRelativePath(e, filePath)}
                >
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" width="14" height="14">
                    <rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
                    <path d="M5 15H4a2 2 0 01-2-2V4a2 2 0 012-2h9a2 2 0 012 2v1" />
                  </svg>
                </button>
                <button
                  type="button"
                  className="fb-row-action"
                  title={downloadingPath === filePath ? t('fileBrowser.downloading') : t('fileBrowser.download')}
                  aria-label={downloadingPath === filePath ? t('fileBrowser.downloading') : t('fileBrowser.download')}
                  aria-busy={downloadingPath === filePath}
                  disabled={downloadingPath !== ''}
                  onClick={(e) => handleDownload(e, filePath, file.name)}
                >
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" width="14" height="14">
                    <path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4" />
                    <polyline points="7 10 12 15 17 10" />
                    <line x1="12" y1="15" x2="12" y2="3" />
                  </svg>
                </button>
              </span>
              <span className="fb-size">{formatFileSize(file.size)}</span>
            </div>
          );
        })}
      </div>
    );
  };

  return (
    <>
      <div className="filebrowser-overlay" onClick={onClose} />
      <div className="filebrowser-panel" ref={panelRef} style={isMobile ? undefined : { width: panelWidth }}>
        {!isMobile && <div className="filebrowser-resize-handle" onMouseDown={handleResizeStart} />}
        <div className="filebrowser-header">
          <h3 title={root?.path || rootPath}>{root?.path || rootPath || t('fileBrowser.files')}</h3>
          <button className="filebrowser-close-btn" aria-label={t('common.close')} onClick={onClose}>
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <path d="M18 6L6 18M6 6l12 12" />
            </svg>
          </button>
        </div>

        <div className={`filebrowser-content ${isMobile && viewingFile ? 'mobile-viewing' : ''}`}>
          <div className="filebrowser-tree">
            {error && <pre className="fb-tree-error" role="alert">{error}</pre>}
            {!root && !error && <div className="fb-tree-loading">{t('common.loading')}</div>}
            {root && renderTree(root, 0)}
          </div>

          {/* Mobile: inline viewer */}
          {isMobile && viewingFile && (
            <FileViewer
              file={viewingFile}
              jobId={jobId}
              className="fb-viewer-modal-inline"
              onBack={closeViewer}
              onClose={closeViewer}
            />
          )}
        </div>

        {/* Desktop: centered modal viewer */}
        {!isMobile && viewingFile && (
          <>
            <div className="fb-modal-overlay" onClick={closeViewer} />
            <FileViewer
              file={viewingFile}
              jobId={jobId}
              className="fb-modal file-viewer-modal"
              onClose={closeViewer}
            />
          </>
        )}
      </div>
    </>
  );
}
