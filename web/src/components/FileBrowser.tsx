import { useState, useCallback, useEffect, useRef } from 'react';
import './FileBrowser.css';
import { useIsMobile } from '../hooks/useIsMobile';
import { copyToClipboard } from '../utils/clipboard';
import { detectLanguage, getLanguageLabel, tokenizeLine } from '../utils/syntaxHighlight';

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

interface ViewingFile {
  path: string;
  name: string;
  content: string;
  size: number;
  truncated: boolean;
  binary: boolean;
  loading: boolean;
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

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

const imageExts = new Set(['.png', '.jpg', '.jpeg', '.gif', '.bmp', '.webp', '.svg', '.ico']);

function isImageFile(name: string): boolean {
  const ext = name.lastIndexOf('.') >= 0 ? name.slice(name.lastIndexOf('.')).toLowerCase() : '';
  return imageExts.has(ext);
}

function showToast(message: string) {
  const existing = document.querySelector('.copy-toast');
  if (existing) existing.remove();
  const toast = document.createElement('div');
  toast.className = 'copy-toast';
  toast.textContent = message;
  document.body.appendChild(toast);
  setTimeout(() => toast.classList.add('show'), 10);
  setTimeout(() => { toast.classList.remove('show'); setTimeout(() => toast.remove(), 300); }, 2000);
}

function HighlightedLine({ line, lang }: { line: string; lang: string | null }) {
  const tokens = tokenizeLine(line, lang);
  if (tokens.length === 1 && tokens[0].type === null) {
    return <>{line || '\u00A0'}</>;
  }
  return (
    <>
      {tokens.map((tok, i) =>
        tok.type ? (
          <span key={i} className={`hl-${tok.type}`}>{tok.value}</span>
        ) : (
          tok.value
        )
      )}
      {line === '' && '\u00A0'}
    </>
  );
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

async function fetchFileContent(path: string, jobId?: string): Promise<{ content: string; size: number; truncated: boolean; binary: boolean } | null> {
  try {
    const res = await fetch('/api/v1/read-file', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path, job_id: jobId || '' }),
    });
    const data = await res.json();
    if (data.code === 0) {
      return {
        content: data.content,
        size: data.size,
        truncated: !!data.truncated,
        binary: !!data.binary,
      };
    }
  } catch { /* ignore */ }
  return null;
}

async function fetchServeFileAsBlob(path: string): Promise<string> {
  const res = await fetch(`/api/v1/serve-file?path=${encodeURIComponent(path)}`);
  if (!res.ok) throw new Error(`Failed to fetch file: ${res.status}`);
  const blob = await res.blob();
  return URL.createObjectURL(blob);
}

export function FileBrowser({ rootPath, jobId, onClose }: FileBrowserProps) {
  const [root, setRoot] = useState<DirNode | null>(null);
  const [viewingFile, setViewingFile] = useState<ViewingFile | null>(null);
  const [imageBlobUrl, setImageBlobUrl] = useState<string | null>(null);
  const [panelWidth, setPanelWidth] = useState(420);
  const resizing = useRef(false);
  const resizeCleanup = useRef<(() => void) | null>(null);

  // Clean up resize listeners on unmount to prevent leaks
  useEffect(() => {
    return () => { resizeCleanup.current?.(); };
  }, []);
  const panelRef = useRef<HTMLDivElement>(null);
  const requestedFileRef = useRef<string>('');
  const isMobile = useIsMobile();

  const viewingPath = viewingFile?.path;
  const viewingName = viewingFile?.name;

  // Load blob URL for image files. No explicit reset when we leave image
  // state: the previous effect run's cleanup callback revokes the old URL
  // and sets state to null, so subsequent renders start clean.
  useEffect(() => {
    if (!viewingPath || !viewingName || !isImageFile(viewingName)) return;
    let cancelled = false;
    let objectUrl: string | null = null;
    fetchServeFileAsBlob(viewingPath)
      .then((url) => {
        if (cancelled) { URL.revokeObjectURL(url); return; }
        objectUrl = url;
        setImageBlobUrl(url);
      })
      .catch(() => { if (!cancelled) setImageBlobUrl(null); });
    return () => {
      cancelled = true;
      if (objectUrl) {
        URL.revokeObjectURL(objectUrl);
        objectUrl = null;
      }
      // Best-effort: keep state in sync for dependency changes; do not
      // rely on setState for cleanup correctness during unmount.
      setImageBlobUrl(null);
    };
  }, [viewingPath, viewingName]);

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

  const openFile = useCallback(async (dirPath: string, file: FileEntry) => {
    const fullPath = dirPath === '/' ? '/' + file.name : dirPath + '/' + file.name;
    requestedFileRef.current = fullPath;

    // For images, use serve-file endpoint directly — no need to fetch content
    if (isImageFile(file.name)) {
      setViewingFile({
        path: fullPath,
        name: file.name,
        content: '',
        size: file.size,
        truncated: false,
        binary: true,
        loading: false,
      });
      return;
    }

    setViewingFile({
      path: fullPath,
      name: file.name,
      content: '',
      size: file.size,
      truncated: false,
      binary: false,
      loading: true,
    });
    const data = await fetchFileContent(fullPath, jobId);
    // Guard against stale response from a previously clicked file
    if (requestedFileRef.current !== fullPath) return;
    if (data) {
      setViewingFile({
        path: fullPath,
        name: file.name,
        content: data.content,
        size: data.size,
        truncated: data.truncated,
        binary: data.binary,
        loading: false,
      });
    } else {
      setViewingFile((f) => f ? { ...f, content: 'Failed to load file', loading: false } : f);
    }
  }, [jobId]);

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
              <span className="fb-size">{formatSize(file.size)}</span>
            </div>
          );
        })}
      </div>
    );
  };

  const lang = viewingFile ? detectLanguage(viewingFile.path) : null;
  const langLabel = viewingFile ? getLanguageLabel(viewingFile.path) : '';
  const lines = viewingFile?.content ? viewingFile.content.split('\n') : [];
  const [copiedPath, setCopiedPath] = useState(false);
  const [copiedContent, setCopiedContent] = useState(false);

  const handleCopyPath = () => {
    if (!viewingFile) return;
    copyToClipboard(viewingFile.path).then(() => {
      setCopiedPath(true);
      showToast('已复制路径');
      setTimeout(() => setCopiedPath(false), 2000);
    }).catch(() => showToast('复制失败'));
  };

  const handleCopyContent = () => {
    if (!viewingFile) return;
    copyToClipboard(viewingFile.content).then(() => {
      setCopiedContent(true);
      showToast('已复制内容');
      setTimeout(() => setCopiedContent(false), 2000);
    }).catch(() => showToast('复制失败'));
  };

  const handleOpenStandalonePreview = () => {
    if (!viewingFile) return;
    const url = new URL(window.location.href);
    url.searchParams.set('view', 'file-preview');
    url.searchParams.set('path', viewingFile.path);
    if (jobId) url.searchParams.set('jobId', jobId);
    window.open(url.toString(), '_blank', 'noopener,noreferrer');
  };

  const renderFileViewerContent = () => {
    if (!viewingFile) return null;
    if (viewingFile.loading) {
      return (
        <div className="file-viewer-loading">
          <div className="file-viewer-loading-bar" />
          <span>加载中...</span>
        </div>
      );
    }
    if (isImageFile(viewingFile.name)) {
      return (
        <div className="filebrowser-viewer-image">
          {imageBlobUrl ? <img src={imageBlobUrl} alt={viewingFile.name} /> : <div className="file-viewer-loading"><span>Loading image...</span></div>}
        </div>
      );
    }
    if (viewingFile.binary) {
      return <div className="file-viewer-binary">二进制文件，无法预览</div>;
    }
    return (
      <div className="file-viewer-body">
        <pre className="file-viewer-code">
          <table className="file-viewer-table">
            <tbody>
              {lines.map((lineContent, idx) => (
                <tr key={idx}>
                  <td className="file-viewer-line-number">{idx + 1}</td>
                  <td className="file-viewer-line-content"><HighlightedLine line={lineContent} lang={lang} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        </pre>
      </div>
    );
  };

  const renderFileViewerHeader = (showBack?: boolean) => (
    <div className="file-viewer-header">
      <div className="file-viewer-header-left">
        {showBack && (
          <button className="file-viewer-header-btn" onClick={() => setViewingFile(null)}>
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <path d="M19 12H5M12 19l-7-7 7-7" />
            </svg>
          </button>
        )}
        <div className="file-viewer-file-icon">
          <svg viewBox="0 0 16 16" width="16" height="16" fill="currentColor">
            <path d="M3.5 1A1.5 1.5 0 0 0 2 2.5v11A1.5 1.5 0 0 0 3.5 15h9a1.5 1.5 0 0 0 1.5-1.5V5.414a1 1 0 0 0-.293-.707l-3.414-3.414A1 1 0 0 0 9.586 1H3.5Zm0 1h5.586L13 5.914V13.5a.5.5 0 0 1-.5.5h-9a.5.5 0 0 1-.5-.5v-11a.5.5 0 0 1 .5-.5Z"/>
          </svg>
        </div>
        <span className="file-viewer-filename" title={viewingFile?.path}>{viewingFile?.name}</span>
        {langLabel && <span className="file-viewer-lang-badge">{langLabel}</span>}
        {viewingFile && viewingFile.size > 0 && <span className="file-viewer-size">{formatSize(viewingFile.size)}</span>}
      </div>
      <div className="file-viewer-header-right">
        {viewingFile && !viewingFile.loading && !viewingFile.binary && !isImageFile(viewingFile.name) && (
          <button className="file-viewer-header-btn" title="在新页面预览" aria-label="在新页面预览" onClick={handleOpenStandalonePreview}>
            <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="2"><path d="M14 3h7v7"/><path d="M10 14 21 3"/><path d="M21 14v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5"/></svg>
          </button>
        )}
        {!viewingFile?.binary && !isImageFile(viewingFile?.name || '') && (
          <button className="file-viewer-header-btn" title="复制内容" onClick={handleCopyContent}>
            {copiedContent ? (
              <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2"><polyline points="20 6 9 17 4 12" /></svg>
            ) : (
              <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
            )}
          </button>
        )}
        <button className="file-viewer-header-btn" title="复制路径" onClick={handleCopyPath}>
          {copiedPath ? (
            <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2"><polyline points="20 6 9 17 4 12" /></svg>
          ) : (
            <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2"><path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71" /><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71" /></svg>
          )}
        </button>
        <button className="file-viewer-header-btn file-viewer-close-btn" onClick={() => setViewingFile(null)}>
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <path d="M18 6L6 18M6 6l12 12" />
          </svg>
        </button>
      </div>
    </div>
  );

  const renderFileViewerFooter = () => (
    <div className="file-viewer-footer">
      <span className="file-viewer-path">{viewingFile?.path}</span>
      {!viewingFile?.binary && !isImageFile(viewingFile?.name || '') && (
        <span className="file-viewer-line-count">{lines.length} lines</span>
      )}
    </div>
  );

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
            <div className="fb-viewer-modal-inline">
              {renderFileViewerHeader(true)}
              {viewingFile.truncated && (
                <div className="file-viewer-notice">文件超过 1MB，仅展示前 1MB 内容</div>
              )}
              {renderFileViewerContent()}
              {renderFileViewerFooter()}
            </div>
          )}
        </div>

        {/* Desktop: centered modal viewer */}
        {!isMobile && viewingFile && (
          <>
            <div className="fb-modal-overlay" onClick={() => setViewingFile(null)} />
            <div className="fb-modal file-viewer-modal">
              {renderFileViewerHeader()}
              {viewingFile.truncated && (
                <div className="file-viewer-notice">文件超过 1MB，仅展示前 1MB 内容</div>
              )}
              {renderFileViewerContent()}
              {renderFileViewerFooter()}
            </div>
          </>
        )}
      </div>
    </>
  );
}
