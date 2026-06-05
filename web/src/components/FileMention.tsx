import { useState, useEffect, useRef, useCallback } from 'react';
import './FileMention.css';

interface FileResult {
  path: string;
  name: string;
  dir: string;
}

interface FileMentionProps {
  keyword: string;
  workdir: string;
  onSelect: (file: FileResult) => void;
  onClose: () => void;
  activeIndex: number;
  onActiveIndexChange: (index: number) => void;
}

function getFileIcon(name: string): string {
  const ext = name.split('.').pop()?.toLowerCase() || '';
  const iconMap: Record<string, string> = {
    ts: '🟦', tsx: '⚛️', js: '🟨', jsx: '⚛️',
    go: '🐹', py: '🐍', rs: '🦀', java: '☕',
    css: '🎨', html: '🌐', json: '📋', md: '📝',
    yaml: '⚙️', yml: '⚙️', toml: '⚙️',
    sh: '💻', bash: '💻', zsh: '💻',
    sql: '🗄️', graphql: '🔮', proto: '📡',
    png: '🖼️', jpg: '🖼️', svg: '🖼️', gif: '🖼️',
  };
  return iconMap[ext] || '📄';
}

export function FileMention({ keyword, workdir, onSelect, onClose, activeIndex, onActiveIndexChange }: FileMentionProps) {
  const [files, setFiles] = useState<FileResult[]>([]);

  // Clamp activeIndex to valid range
  const clampedIndex = files.length > 0 ? Math.min(activeIndex, files.length - 1) : 0;

  // Sync clamped value back to parent so ArrowUp works correctly after overshooting
  useEffect(() => {
    if (clampedIndex !== activeIndex) {
      onActiveIndexChange(clampedIndex);
    }
  }, [clampedIndex, activeIndex, onActiveIndexChange]);

  const [loading, setLoading] = useState(false);
  const popupRef = useRef<HTMLDivElement>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout>>(undefined);

  const fetchFiles = useCallback(async (kw: string) => {
    setLoading(true);
    try {
      const params = new URLSearchParams({ keyword: kw, dir: workdir });
      const res = await fetch(`/api/v1/search-files?${params}`);
      const data = await res.json();
      if (data.code === 0 && data.files) {
        setFiles(data.files);
        onActiveIndexChange(0);
      } else {
        setFiles([]);
      }
    } catch {
      setFiles([]);
    } finally {
      setLoading(false);
    }
  }, [workdir, onActiveIndexChange]);

  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => {
      fetchFiles(keyword);
    }, 200);
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
    };
  }, [keyword, fetchFiles]);

  // Close on outside click
  useEffect(() => {
    const handle = (e: MouseEvent) => {
      if (popupRef.current && !popupRef.current.contains(e.target as Node)) {
        onClose();
      }
    };
    document.addEventListener('mousedown', handle);
    return () => document.removeEventListener('mousedown', handle);
  }, [onClose]);

  // Scroll active item into view
  useEffect(() => {
    if (popupRef.current) {
      const item = popupRef.current.querySelector('.file-mention-item.active');
      if (item) {
        item.scrollIntoView({ block: 'nearest' });
      }
    }
  }, [clampedIndex]);

  return (
    <div className="file-mention-popup" ref={popupRef}>
      <div className="file-mention-header">Files</div>
      {loading && files.length === 0 && (
        <div className="file-mention-loading">Searching...</div>
      )}
      {!loading && files.length === 0 && (
        <div className="file-mention-empty">No files found</div>
      )}
      {files.map((file, idx) => (
        <div
          key={file.path}
          className={`file-mention-item ${idx === clampedIndex ? 'active' : ''}`}
          onMouseEnter={() => onActiveIndexChange(idx)}
          onMouseDown={(e) => e.preventDefault()}
          onTouchEnd={(e) => { e.preventDefault(); onSelect(file); }}
          onClick={() => onSelect(file)}
        >
          <span className="file-mention-icon">{getFileIcon(file.name)}</span>
          <div className="file-mention-info">
            <span className="file-mention-name">{file.name}</span>
            {file.dir && <span className="file-mention-dir">{file.dir}</span>}
          </div>
        </div>
      ))}
    </div>
  );
}

export type { FileResult };
