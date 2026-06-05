import { useCallback, useEffect, useRef, useState } from 'react';
import './AgentsLocalEditor.css';

interface AgentsLocalEditorProps {
  filePath: string;
  jobId?: string;
  onClose: () => void;
}

type SaveStatus = 'idle' | 'saving' | 'error';

export function AgentsLocalEditor({ filePath, jobId, onClose }: AgentsLocalEditorProps) {
  const [content, setContent] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [saveStatus, setSaveStatus] = useState<SaveStatus>('idle');
  const [dirty, setDirty] = useState(false);
  // lastSavedAt records the wall-clock time of the most recent successful
  // write. It persists across typing so that a user who keeps editing after
  // a Save still sees confirmation that their earlier click went through —
  // otherwise a slow network + a single keystroke would silently swallow
  // the "Saved" state and leave them unsure whether to click Save again.
  const [lastSavedAt, setLastSavedAt] = useState<number | null>(null);
  const overlayRef = useRef<HTMLDivElement>(null);
  const editorRef = useRef<HTMLDivElement>(null);
  const contentRef = useRef('');

  const readFile = useCallback(async (path: string): Promise<{ code: number; content?: string }> => {
    const res = await fetch('/api/v1/read-file', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path, job_id: jobId || '' }),
    });
    return res.json();
  }, [jobId]);

  const writeFile = useCallback(async (path: string, fileContent: string): Promise<{ code: number }> => {
    const res = await fetch('/api/v1/write-file', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path, content: fileContent, job_id: jobId || '' }),
    });
    return res.json();
  }, [jobId]);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      setLoading(true);
      try {
        const data = await readFile(filePath);
        if (!cancelled) {
          const next = data.code === 0 ? (data.content || '') : '';
          contentRef.current = next;
          setContent(next);
        }
      } catch {
        if (!cancelled) {
          contentRef.current = '';
          setContent('');
        }
      }
      if (!cancelled) {
        setLoading(false);
        setDirty(false);
        setSaveStatus('idle');
        setLastSavedAt(null);
      }
    };
    load();
    return () => { cancelled = true; };
  }, [filePath, readFile]);

  // iPad/手机端：使用 visualViewport API 同步 fixed 面板高度，避免键盘遮挡
  useEffect(() => {
    const vv = window.visualViewport;
    if (!vv) return;

    const overlayEl = overlayRef.current;
    const editorEl = editorRef.current;
    if (!overlayEl || !editorEl) return;

    const sync = () => {
      const h = vv.height;
      const top = vv.offsetTop;

      overlayEl.style.height = `${h}px`;
      overlayEl.style.top = `${top}px`;
      overlayEl.style.bottom = 'auto';

      editorEl.style.height = `${h}px`;
      editorEl.style.top = `${top}px`;
      editorEl.style.bottom = 'auto';
    };

    const scrollFocusedIntoView = () => {
      const el = document.activeElement;
      if (el instanceof HTMLTextAreaElement) {
        setTimeout(() => {
          el.scrollIntoView({ block: 'center', behavior: 'smooth' });
        }, 150);
      }
    };

    const onResize = () => {
      sync();
      scrollFocusedIntoView();
    };

    const onFocusIn = (e: FocusEvent) => {
      if (e.target instanceof HTMLTextAreaElement) {
        setTimeout(() => {
          (e.target as HTMLElement).scrollIntoView({ block: 'center', behavior: 'smooth' });
        }, 300);
      }
    };

    vv.addEventListener('resize', onResize);
    vv.addEventListener('scroll', sync);
    editorEl?.addEventListener('focusin', onFocusIn);
    sync();

    return () => {
      vv.removeEventListener('resize', onResize);
      vv.removeEventListener('scroll', sync);
      editorEl?.removeEventListener('focusin', onFocusIn);
      overlayEl.style.height = '';
      overlayEl.style.top = '';
      overlayEl.style.bottom = '';
      editorEl.style.height = '';
      editorEl.style.top = '';
      editorEl.style.bottom = '';
    };
  }, []);

  const handleChange = useCallback((e: React.ChangeEvent<HTMLTextAreaElement>) => {
    const next = e.target.value;
    contentRef.current = next;
    setContent(next);
    setDirty(true);
    // Clear a prior error so the user can retry, but DON'T touch
    // lastSavedAt — keeping it lets the status line keep showing
    // "Saved at HH:MM:SS · unsaved changes" instead of going blank.
    setSaveStatus((prev) => (prev === 'error' ? 'idle' : prev));
  }, []);

  const handleSave = useCallback(async () => {
    // Snapshot the content we're about to persist. If the user types during
    // the async write, `content` state will diverge from `saved` and we must
    // NOT clear the dirty flag — otherwise the "Save" button disables and
    // the latest keystrokes look persisted when they aren't.
    const saved = content;
    setSaving(true);
    setSaveStatus('saving');
    try {
      // NOTE: 这里就是要用 AGENTS.local.md / CLAUDE.local.md，不是 bug。
      // Prompt 设置写 AGENTS.md（全局），聊天页编辑器写 AGENTS.local.md（本地覆盖）。
      const agentsResult = await writeFile(filePath, saved);
      if (agentsResult.code !== 0) {
        setSaveStatus('error');
        return;
      }
      const claudePath = filePath.replace(/AGENTS\.local\.md$/, 'CLAUDE.local.md');
      if (claudePath !== filePath) {
        const claudeResult = await writeFile(claudePath, saved);
        if (claudeResult.code !== 0) {
          // AGENTS.local.md saved; CLAUDE.local.md failed. Leave
          // dirty=true so the user notices and retries.
          setSaveStatus('error');
          return;
        }
      }
      // Record the successful save independently of the post-save draft
      // state. If the user typed during the await, the status line still
      // shows "Saved at HH:MM:SS · unsaved changes" so they can tell the
      // earlier click went through and know they have new edits to save.
      setLastSavedAt(Date.now());
      setSaveStatus('idle');
      if (contentRef.current === saved) {
        setDirty(false);
      }
    } catch {
      setSaveStatus('error');
    } finally {
      setSaving(false);
    }
  }, [filePath, content, writeFile]);

  return (
    <>
      <div className="agents-editor-overlay" ref={overlayRef} onClick={onClose} />
      <div className="agents-editor" ref={editorRef}>
      <div className="agents-editor-header">
        <h3>AGENTS.local.md</h3>
        <div className="agents-editor-actions">
          <button
            className="agents-editor-save-btn"
            onClick={handleSave}
            disabled={saving || !dirty}
          >
            {saving ? 'Saving...' : 'Save'}
          </button>
          <button className="agents-editor-close-btn" onClick={onClose}>
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <path d="M18 6L6 18M6 6l12 12" />
            </svg>
          </button>
        </div>
      </div>
      {loading ? (
        <div className="agents-editor-loading">Loading...</div>
      ) : (
        <textarea
          className="agents-editor-textarea"
          value={content}
          onChange={handleChange}
          spellCheck={false}
        />
      )}
      <div className="agents-editor-status">
        {saveStatus === 'saving' && 'Saving...'}
        {saveStatus === 'error' && 'Save failed'}
        {saveStatus === 'idle' && lastSavedAt !== null && (
          <>
            Saved at {new Date(lastSavedAt).toLocaleTimeString()}
            {dirty && ' · unsaved changes'}
          </>
        )}
      </div>
    </div>
    </>
  );
}
