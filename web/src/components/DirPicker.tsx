import { useState, useEffect, useRef, useCallback } from 'react';
import './DirPicker.css';

// Parent directory of an absolute POSIX path ('' when already at root or not
// absolute). Used to recover when a file path is fed to a directory listing.
function parentOf(path: string): string {
  if (!path || !path.startsWith('/')) return '';
  const trimmed = path.replace(/\/+$/, '');
  const idx = trimmed.lastIndexOf('/');
  if (idx <= 0) return idx === 0 ? '/' : '';
  return trimmed.slice(0, idx);
}

interface DirFileEntry {
  name: string;
  size: number;
  modTime: string;
}

interface DirPickerProps {
  initialPath: string;
  basePath?: string;
  // When true the picker also lists files: clicking a directory navigates into
  // it, clicking a file selects it, and Confirm returns the selected file (or
  // the current directory when no file is chosen). Default false keeps the
  // dir-only behavior used by workspace settings etc.
  selectFile?: boolean;
  // Optional dialog heading; defaults to "Select Working Directory".
  title?: string;
  onConfirm: (path: string) => void;
  onCancel: () => void;
}

export function DirPicker({ initialPath, basePath, selectFile, title, onConfirm, onCancel }: DirPickerProps) {
  const [currentPath, setCurrentPath] = useState(initialPath || '');
  const [dirs, setDirs] = useState<string[]>([]);
  const [files, setFiles] = useState<DirFileEntry[]>([]);
  // Absolute path of the file the user picked (file mode only). Cleared on any
  // directory navigation so it never points outside the directory on screen.
  const [selectedFile, setSelectedFile] = useState('');
  const [parentPath, setParentPath] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [inputValue, setInputValue] = useState('');
  const [showHidden, setShowHidden] = useState(false);
  const [newFolderMode, setNewFolderMode] = useState(false);
  const [newFolderName, setNewFolderName] = useState('');
  const [newFolderError, setNewFolderError] = useState('');
  const [recentDirs, setRecentDirs] = useState<string[]>([]);
  const [showDropdown, setShowDropdown] = useState(false);
  const modalRef = useRef<HTMLDivElement>(null);
  const newFolderInputRef = useRef<HTMLInputElement>(null);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const inputWrapperRef = useRef<HTMLDivElement>(null);

  const isWithinBase = useCallback((path: string): boolean => {
    if (!basePath) return true;
    const normalizedBase = basePath.endsWith('/') ? basePath : basePath + '/';
    return path === basePath || path.startsWith(normalizedBase);
  }, [basePath]);

  // Monotonic request id: only the latest navigation's response may touch
  // state, so a slow stale list-dir can't overwrite a newer location.
  const requestSeqRef = useRef(0);

  const fetchDir = useCallback(async (path: string, preselectFile?: string) => {
    const seq = ++requestSeqRef.current;
    // If basePath is set and the requested path is above it, clamp to basePath
    if (basePath && !isWithinBase(path)) {
      path = basePath;
    }
    setLoading(true);
    setError('');
    setSelectedFile('');
    // In file mode the requested path may point at a FILE (list-dir errors on a
    // non-directory). Retry once against its parent and preselect the file, so
    // re-opening the picker on a file-valued variable lands on the right folder.
    let target = path;
    let preselect = preselectFile || '';
    try {
      for (let attempt = 0; attempt < 2; attempt++) {
        const query = new URLSearchParams();
        if (target) query.set('path', target);
        if (selectFile) query.set('showFiles', 'true');
        const params = query.toString() ? `?${query.toString()}` : '';
        const res = await fetch(`/api/v1/list-dir${params}`);
        const data = await res.json();
        if (seq !== requestSeqRef.current) return;
        if (data.code === 0) {
          setCurrentPath(data.current);
          setParentPath(data.parent || '');
          setDirs(data.dirs || []);
          setFiles(selectFile && Array.isArray(data.files) ? data.files : []);
          setInputValue(data.current);
          if (preselect) setSelectedFile(preselect);
          return;
        }
        const parent = attempt === 0 && !preselect && selectFile ? parentOf(target) : '';
        if (parent && parent !== target) {
          preselect = target;
          target = parent;
          continue;
        }
        setError(data.msg || 'Failed to list directory');
        return;
      }
    } catch {
      if (seq === requestSeqRef.current) setError('Network error');
    } finally {
      if (seq === requestSeqRef.current) setLoading(false);
    }
  }, [basePath, isWithinBase, selectFile]);

  const fetchRecentDirs = useCallback(async () => {
    try {
      const res = await fetch('/api/v1/recent-dirs');
      const data = await res.json();
      if (data.code === 0 && Array.isArray(data.dirs)) {
        setRecentDirs(data.dirs);
      }
    } catch {
      // ignore
    }
  }, []);

  const saveRecentDir = async (dir: string) => {
    try {
      await fetch('/api/v1/recent-dirs', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ dir }),
      });
    } catch {
      // ignore
    }
  };

  // In file mode the user may pick a file; otherwise the current directory is
  // the selection. saveRecentDir always records a directory, so derive the
  // containing dir for a picked file.
  const joinPath = (base: string, name: string) => (base === '/' ? '/' + name : base + '/' + name);
  const effectiveSelected = selectFile && selectedFile ? selectedFile : currentPath;
  const handleConfirmSelected = () => {
    saveRecentDir(currentPath);
    onConfirm(effectiveSelected);
  };

  const handleCreateFolder = async () => {
    const name = newFolderName.trim();
    if (!name) return;
    setNewFolderError('');
    try {
      const res = await fetch('/api/v1/mkdir', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ parent: currentPath, name }),
      });
      const data = await res.json();
      if (data.code === 0) {
        setNewFolderMode(false);
        setNewFolderName('');
        // Refresh and navigate into the new folder
        await fetchDir(data.path);
      } else {
        setNewFolderError(data.msg || 'Failed to create folder');
      }
    } catch {
      setNewFolderError('Network error');
    }
  };

  const initializedRef = useRef(false);

  useEffect(() => {
    // Fire the initial directory fetch exactly once. Re-running the
    // effect whenever `basePath` changed (via the `fetchDir` dep) would
    // snap the user back to `initialPath` — wiping out their navigation
    // the moment the parent re-renders with a new base. If the host
    // intentionally changes `initialPath` (different workspace), the
    // parent can force a re-init by remounting this component.
    if (initializedRef.current) return;
    initializedRef.current = true;
    fetchDir(initialPath);
    fetchRecentDirs();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (newFolderMode && newFolderInputRef.current) {
      newFolderInputRef.current.focus();
    }
  }, [newFolderMode]);

  // Close on Escape
  useEffect(() => {
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        if (showDropdown) {
          setShowDropdown(false);
        } else if (newFolderMode) {
          setNewFolderMode(false);
          setNewFolderName('');
          setNewFolderError('');
        } else {
          onCancel();
        }
      }
    };
    document.addEventListener('keydown', handleKey);
    return () => document.removeEventListener('keydown', handleKey);
  }, [onCancel, newFolderMode, showDropdown]);

  // Close dropdown on click outside
  useEffect(() => {
    if (!showDropdown) return;
    const handleClick = (e: MouseEvent) => {
      if (
        inputWrapperRef.current &&
        !inputWrapperRef.current.contains(e.target as Node)
      ) {
        setShowDropdown(false);
      }
    };
    document.addEventListener('mousedown', handleClick);
    return () => document.removeEventListener('mousedown', handleClick);
  }, [showDropdown]);

  // Close on click outside
  const handleOverlayClick = (e: React.MouseEvent) => {
    if (e.target === e.currentTarget) onCancel();
  };

  const handleInputKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && inputValue.trim()) {
      setShowDropdown(false);
      fetchDir(inputValue.trim());
    }
  };

  const handleNewFolderKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter') {
      handleCreateFolder();
    } else if (e.key === 'Escape') {
      setNewFolderMode(false);
      setNewFolderName('');
      setNewFolderError('');
    }
  };

  const handleSelectRecentDir = (dir: string) => {
    if (!isWithinBase(dir)) return;
    setShowDropdown(false);
    setInputValue(dir);
    fetchDir(dir);
  };

  const pathSegments = currentPath.split('/').filter(Boolean);

  return (
    <div className="dirpicker-overlay" onClick={handleOverlayClick}>
      <div className="dirpicker-modal" ref={modalRef}>
        <div className="dirpicker-header">
          <h3>{title || 'Select Working Directory'}</h3>
          {basePath && <div className="dirpicker-base-hint">Restricted to: {basePath}</div>}
          <button className="dirpicker-close" onClick={onCancel}>
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <path d="M18 6L6 18M6 6l12 12" />
            </svg>
          </button>
        </div>

        <div className="dirpicker-pathbar">
          <div className="dirpicker-input-wrapper" ref={inputWrapperRef}>
            <input
              className="dirpicker-path-input"
              value={inputValue}
              onChange={(e) => setInputValue(e.target.value)}
              onKeyDown={handleInputKeyDown}
              placeholder="Enter path and press Enter"
            />
            <button
              className={`dirpicker-dropdown-toggle ${showDropdown ? 'active' : ''}`}
              onClick={() => setShowDropdown(!showDropdown)}
              title="Recent directories"
            >
              <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
                <path d="M6 9l6 6 6-6" />
              </svg>
            </button>
            {showDropdown && (
              <div className="dirpicker-dropdown" ref={dropdownRef}>
                {recentDirs.length === 0 ? (
                  <div className="dirpicker-dropdown-empty">No recent directories</div>
                ) : (
                  <>
                    <div className="dirpicker-dropdown-header">Recent</div>
                    {recentDirs.filter((d) => isWithinBase(d)).map((dir) => (
                      <div
                        key={dir}
                        className="dirpicker-dropdown-item"
                        onClick={() => handleSelectRecentDir(dir)}
                        title={dir}
                      >
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="#888" strokeWidth="2">
                          <circle cx="12" cy="12" r="10" />
                          <path d="M12 6v6l4 2" />
                        </svg>
                        {dir}
                      </div>
                    ))}
                  </>
                )}
              </div>
            )}
          </div>
          <button className="dirpicker-go-btn" onClick={() => inputValue.trim() && fetchDir(inputValue.trim())}>
            Go
          </button>
        </div>

        <div className="dirpicker-breadcrumb">
          <span className="dirpicker-crumb" onClick={() => fetchDir('/')}>
            /
          </span>
          {pathSegments.map((seg, i) => {
            const fullPath = '/' + pathSegments.slice(0, i + 1).join('/');
            return (
              <span key={fullPath}>
                <span className="dirpicker-crumb-sep">/</span>
                <span
                  className={`dirpicker-crumb ${i === pathSegments.length - 1 ? 'active' : ''}`}
                  onClick={() => fetchDir(fullPath)}
                >
                  {seg}
                </span>
              </span>
            );
          })}
        </div>

        <div className="dirpicker-toolbar">
          <label className="dirpicker-hidden-toggle">
            <input type="checkbox" checked={showHidden} onChange={(e) => setShowHidden(e.target.checked)} />
            Show hidden
          </label>
          <button
            className="dirpicker-newfolder-btn"
            onClick={() => { setNewFolderMode(true); setNewFolderError(''); }}
            disabled={newFolderMode}
          >
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <path d="M12 5v14M5 12h14" />
            </svg>
            New Folder
          </button>
        </div>

        <div className="dirpicker-list">
          {loading && <div className="dirpicker-status">Loading...</div>}
          {error && <div className="dirpicker-status dirpicker-error">{error}</div>}
          {!loading && !error && (
            <>
              {parentPath && isWithinBase(parentPath) && (
                <div className="dirpicker-item dirpicker-parent" onClick={() => fetchDir(parentPath)}>
                  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                    <path d="M15 18l-6-6 6-6" />
                  </svg>
                  <span>..</span>
                </div>
              )}
              {newFolderMode && (
                <div className="dirpicker-item dirpicker-newfolderrow">
                  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#16a34a" strokeWidth="2">
                    <path d="M22 19a2 2 0 01-2 2H4a2 2 0 01-2-2V5a2 2 0 012-2h5l2 3h9a2 2 0 012 2z" />
                  </svg>
                  <input
                    ref={newFolderInputRef}
                    className="dirpicker-newfolder-input"
                    value={newFolderName}
                    onChange={(e) => setNewFolderName(e.target.value)}
                    onKeyDown={handleNewFolderKeyDown}
                    placeholder="Folder name"
                  />
                  <button className="dirpicker-newfolder-ok" onClick={handleCreateFolder} disabled={!newFolderName.trim()}>
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                      <path d="M20 6L9 17l-5-5" />
                    </svg>
                  </button>
                  <button className="dirpicker-newfolder-cancel" onClick={() => { setNewFolderMode(false); setNewFolderName(''); setNewFolderError(''); }}>
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                      <path d="M18 6L6 18M6 6l12 12" />
                    </svg>
                  </button>
                </div>
              )}
              {newFolderError && <div className="dirpicker-newfolder-error">{newFolderError}</div>}
              {dirs.length === 0 && files.length === 0 && !parentPath && !newFolderMode && (
                <div className="dirpicker-status">{selectFile ? 'Empty directory' : 'No subdirectories'}</div>
              )}
              {dirs
                .filter((d) => showHidden || !d.startsWith('.'))
                .map((dir) => (
                  <div
                    key={dir}
                    className="dirpicker-item"
                    onClick={() => fetchDir(joinPath(currentPath, dir))}
                  >
                    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#f59e0b" strokeWidth="2">
                      <path d="M22 19a2 2 0 01-2 2H4a2 2 0 01-2-2V5a2 2 0 012-2h5l2 3h9a2 2 0 012 2z" />
                    </svg>
                    <span>{dir}</span>
                  </div>
                ))}
              {selectFile &&
                files
                  .filter((f) => showHidden || !f.name.startsWith('.'))
                  .map((file) => {
                    const fullPath = joinPath(currentPath, file.name);
                    return (
                      <div
                        key={file.name}
                        className={`dirpicker-item dirpicker-file ${selectedFile === fullPath ? 'selected' : ''}`}
                        onClick={() => setSelectedFile(fullPath)}
                      >
                        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#64748b" strokeWidth="2">
                          <path d="M13 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V9z" />
                          <path d="M13 2v7h7" />
                        </svg>
                        <span>{file.name}</span>
                      </div>
                    );
                  })}
            </>
          )}
        </div>

        <div className="dirpicker-footer">
          <div className="dirpicker-selected">
            <span className="dirpicker-selected-label">Selected:</span>
            <code>{effectiveSelected}</code>
          </div>
          <div className="dirpicker-actions">
            <button className="dirpicker-btn dirpicker-btn-cancel" onClick={onCancel}>Cancel</button>
            <button className="dirpicker-btn dirpicker-btn-confirm" onClick={handleConfirmSelected} disabled={!effectiveSelected}>
              Confirm
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
