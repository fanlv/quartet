import { useState, useCallback, useEffect, useRef } from 'react';
import './FileBrowser.css';
import { useIsMobile } from '../hooks/useIsMobile';
import { useFileViewer } from '../hooks/useFileViewer';
import { copyToClipboard } from '../utils/clipboard';
import { formatFileSize } from '../utils/file';
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
}

async function fetchDirContents(path: string): Promise<{ dirs: string[]; files: FileEntry[]; current: string; parent: string } | null> {
  try {
    const params = path ? `?path=${encodeURIComponent(path)}&showFiles=true` : '?showFiles=true';
    const res = await fetch(`/api/v1/list-dir${params}`);
    const data = await res.json();
    if (data.code === 0) {
      return {
        dirs: data.dirs || [],
        files: data.files || [],
        current: data.current,
        parent: data.parent || '',
      };
    }
  } catch { /* ignore */ }
  return null;
}

export function FileBrowser({ rootPath, jobId, onClose }: FileBrowserProps) {
  const [root, setRoot] = useState<DirNode | null>(null);
  const { file: viewingFile, open: openViewer, close: closeViewer } = useFileViewer(jobId);
  const [panelWidth, setPanelWidth] = useState(420);
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
    const loadRoot = async () => {
      const data = await fetchDirContents(rootPath || '');
      if (data) {
        setRoot({
          name: data.current.split('/').filter(Boolean).pop() || '/',
          path: data.current,
          dirs: data.dirs.map((d) => ({
            name: d,
            path: data.current === '/' ? '/' + d : data.current + '/' + d,
            dirs: [],
            files: [],
            loaded: false,
            expanded: false,
          })),
          files: data.files,
          loaded: true,
          expanded: true,
        });
      }
    };
    loadRoot();
  }, [rootPath]);

  const toggleDir = useCallback(async (node: DirNode) => {
    if (node.expanded) {
      setRoot((r) => r ? updateNode(r, node.path, (n) => ({ ...n, expanded: false })) : r);
      return;
    }

    if (!node.loaded) {
      const data = await fetchDirContents(node.path);
      if (data) {
        setRoot((r) => r ? updateNode(r, node.path, (n) => ({
          ...n,
          dirs: data.dirs.map((d) => ({
            name: d,
            path: node.path === '/' ? '/' + d : node.path + '/' + d,
            dirs: [],
            files: [],
            loaded: false,
            expanded: false,
          })),
          files: data.files,
          loaded: true,
          expanded: true,
        })) : r);
        return;
      }
    }
    setRoot((r) => r ? updateNode(r, node.path, (n) => ({ ...n, expanded: true })) : r);
  }, []);

  // Images vs text, stale-response guarding and blob lifecycle all live in
  // useFileViewer, shared with the chat message viewer.
  const openFile = useCallback((dirPath: string, file: FileEntry) => {
    const fullPath = dirPath === '/' ? '/' + file.name : dirPath + '/' + file.name;
    void openViewer(fullPath, { name: file.name, size: file.size });
  }, [openViewer]);

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
      showToast('已复制相对路径');
    }).catch(() => showToast('复制失败'));
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
              <span className="fb-name">{dir.name}</span>
              <button
                className="fb-copy-path-btn"
                title="复制相对路径"
                onClick={(e) => handleCopyRelativePath(e, dir.path)}
              >
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" width="14" height="14">
                  <rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
                  <path d="M5 15H4a2 2 0 01-2-2V4a2 2 0 012-2h9a2 2 0 012 2v1" />
                </svg>
              </button>
            </div>
            {dir.expanded && dir.loaded && renderTree(dir, depth + 1)}
            {dir.expanded && !dir.loaded && (
              <div className="fb-tree-loading" style={{ paddingLeft: indent + 40 }}>Loading...</div>
            )}
          </div>
        ))}
        {/* Files */}
        {node.files.map((file) => {
          const filePath = node.path === '/' ? '/' + file.name : node.path + '/' + file.name;
          return (
            <div
              key={filePath}
              className={`fb-tree-item ${viewingFile?.path === filePath ? 'active' : ''}`}
              style={{ paddingLeft: indent + 10 }}
              onClick={() => openFile(node.path, file)}
            >
              <svg className="fb-arrow placeholder" viewBox="0 0 24 24"><path /></svg>
              <svg className="fb-icon" viewBox="0 0 24 24" fill="none" stroke="#6b7280" strokeWidth="2">
                <path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z" />
                <polyline points="14 2 14 8 20 8" />
              </svg>
              <span className="fb-name">{file.name}</span>
              <button
                className="fb-copy-path-btn"
                title="复制相对路径"
                onClick={(e) => handleCopyRelativePath(e, filePath)}
              >
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" width="14" height="14">
                  <rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
                  <path d="M5 15H4a2 2 0 01-2-2V4a2 2 0 012-2h9a2 2 0 012 2v1" />
                </svg>
              </button>
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
          <h3>{root?.path || rootPath || 'Files'}</h3>
          <button className="filebrowser-close-btn" onClick={onClose}>
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <path d="M18 6L6 18M6 6l12 12" />
            </svg>
          </button>
        </div>

        <div className={`filebrowser-content ${isMobile && viewingFile ? 'mobile-viewing' : ''}`}>
          <div className="filebrowser-tree">
            {!root && <div className="fb-tree-loading">Loading...</div>}
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
