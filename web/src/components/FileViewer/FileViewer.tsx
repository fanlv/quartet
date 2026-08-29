import { useEffect, useRef, useState } from 'react';
import { copyToClipboard } from '../../utils/clipboard';
import { buildFilePreviewUrl, formatFileSize } from '../../utils/file';
import { detectLanguage, getLanguageLabel, tokenizeLine } from '../../utils/syntaxHighlight';
import { showToast } from '../../utils/toast';
import type { FileViewerFile } from '../../types';
import './FileViewer.css';

interface FileViewerProps {
  file: FileViewerFile;
  /** Job scope for read-file / preview links (workspace resolution). */
  jobId?: string;
  /** Container class supplied by the host surface (modal, inline panel, ...). */
  className?: string;
  onClose: () => void;
  /** Renders a back arrow in the header when provided (mobile inline viewer). */
  onBack?: () => void;
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

export function FileViewer({ file, jobId, className, onClose, onBack }: FileViewerProps) {
  const lines = file.content ? file.content.split('\n') : [];
  const lang = detectLanguage(file.path);
  const langLabel = getLanguageLabel(file.path);
  const [copiedPath, setCopiedPath] = useState(false);
  const [copiedContent, setCopiedContent] = useState(false);

  // Auto-scroll to the highlighted line once per opened file. Reset on path
  // change so reusing the same viewer instance for another file re-scrolls.
  const scrolledRef = useRef(false);
  useEffect(() => {
    scrolledRef.current = false;
  }, [file.path]);

  // Text actions are meaningless for images/binaries/failed loads.
  const hasText = !file.loading && !file.error && !file.isImage && !file.binary;

  const handleCopyPath = () => {
    copyToClipboard(file.path).then(() => {
      setCopiedPath(true);
      showToast('已复制路径');
      setTimeout(() => setCopiedPath(false), 2000);
    }).catch(() => showToast('复制失败'));
  };

  const handleCopyContent = () => {
    copyToClipboard(file.content).then(() => {
      setCopiedContent(true);
      showToast('已复制内容');
      setTimeout(() => setCopiedContent(false), 2000);
    }).catch(() => showToast('复制失败'));
  };

  const handleOpenStandalonePreview = () => {
    window.open(buildFilePreviewUrl(file.path, jobId), '_blank', 'noopener,noreferrer');
  };

  const renderBody = () => {
    if (file.loading) {
      return (
        <div className="file-viewer-loading">
          <div className="file-viewer-loading-bar" />
          <span>加载中...</span>
        </div>
      );
    }
    if (file.error) {
      return <div className="file-viewer-error">{file.error}</div>;
    }
    if (file.isImage) {
      return (
        <div className="file-viewer-image">
          {file.imageUrl
            ? <img src={file.imageUrl} alt={file.name} />
            : <div className="file-viewer-loading"><span>加载图片中...</span></div>}
        </div>
      );
    }
    if (file.binary) {
      return <div className="file-viewer-binary">二进制文件，无法预览</div>;
    }
    return (
      <div className="file-viewer-body">
        <div className="file-viewer-code" role="table" aria-label="file content">
          {lines.map((lineContent, idx) => {
            const lineNum = idx + 1;
            const startLine = file.line;
            const endLine = file.endLine ?? file.line;
            const isHighlighted = startLine !== undefined && endLine !== undefined && lineNum >= startLine && lineNum <= endLine;
            const isScrollTarget = startLine !== undefined && lineNum === startLine;
            return (
              <div
                key={idx}
                className={`file-viewer-row${isHighlighted ? ' file-viewer-line-highlight' : ''}`}
                ref={
                  isScrollTarget
                    ? (el) => {
                        if (el && !scrolledRef.current) {
                          scrolledRef.current = true;
                          el.scrollIntoView({ block: 'center' });
                        }
                      }
                    : undefined
                }
                role="row"
              >
                <div className="file-viewer-line-number" role="cell">{lineNum}</div>
                <div className="file-viewer-line-content" role="cell"><HighlightedLine line={lineContent} lang={lang} /></div>
              </div>
            );
          })}
        </div>
      </div>
    );
  };

  return (
    <div className={className}>
      <div className="file-viewer-header">
        <div className="file-viewer-header-left">
          {onBack && (
            <button className="file-viewer-header-btn" title="返回" aria-label="返回" onClick={onBack}>
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
          <span className="file-viewer-filename" title={file.path}>{file.name}</span>
          {langLabel && <span className="file-viewer-lang-badge">{langLabel}</span>}
          {file.size > 0 && <span className="file-viewer-size">{formatFileSize(file.size)}</span>}
        </div>
        <div className="file-viewer-header-right">
          {hasText && (
            <button className="file-viewer-header-btn" title="在新页面预览" aria-label="在新页面预览" onClick={handleOpenStandalonePreview}>
              <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="2"><path d="M14 3h7v7"/><path d="M10 14 21 3"/><path d="M21 14v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5"/></svg>
            </button>
          )}
          {hasText && (
            <button className="file-viewer-header-btn" title="复制内容" aria-label="复制内容" onClick={handleCopyContent}>
              {copiedContent ? (
                <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2"><polyline points="20 6 9 17 4 12" /></svg>
              ) : (
                <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
              )}
            </button>
          )}
          <button className="file-viewer-header-btn" title="复制路径" aria-label="复制路径" onClick={handleCopyPath}>
            {copiedPath ? (
              <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2"><polyline points="20 6 9 17 4 12" /></svg>
            ) : (
              <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2"><path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71" /><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71" /></svg>
            )}
          </button>
          <button className="file-viewer-header-btn file-viewer-close-btn" title="关闭" aria-label="关闭" onClick={onClose}>
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <path d="M18 6L6 18M6 6l12 12" />
            </svg>
          </button>
        </div>
      </div>
      {file.truncated && (
        <div className="file-viewer-notice">文件超过 1MB，仅展示前 1MB 内容</div>
      )}
      {renderBody()}
      <div className="file-viewer-footer">
        <span className="file-viewer-path">{file.path}</span>
        {hasText && <span className="file-viewer-line-count">{lines.length} lines</span>}
      </div>
    </div>
  );
}
