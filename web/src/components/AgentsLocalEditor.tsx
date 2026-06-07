import { useCallback, useEffect, useRef, useState } from 'react';
import './AgentsLocalEditor.css';

interface AgentsLocalEditorProps {
  // 工作目录绝对路径，组件内部据此拼接 AGENTS / CLAUDE 文件路径。
  workdir: string;
  jobId?: string;
  onClose: () => void;
}

type SaveStatus = 'idle' | 'saving' | 'error';

// 可编辑的目标文件。AGENTS.md 为全局规则，AGENTS.local.md 为本地覆盖。
type AgentsTarget = 'AGENTS.local.md' | 'AGENTS.md';

const AGENTS_TARGETS: AgentsTarget[] = ['AGENTS.local.md', 'AGENTS.md'];

// AGENTS.* 真实内容文件对应的 CLAUDE.* 指针文件，内容固定为对 AGENTS.* 的引用，
// 让 Claude Code 通过 @./AGENTS(.local).md 读取真实规则，避免全文重复维护两份。
const CLAUDE_POINTER: Record<AgentsTarget, { file: string; content: string }> = {
  'AGENTS.local.md': { file: 'CLAUDE.local.md', content: '@./AGENTS.local.md' },
  'AGENTS.md': { file: 'CLAUDE.md', content: '@./AGENTS.md' },
};

export function AgentsLocalEditor({ workdir, jobId, onClose }: AgentsLocalEditorProps) {
  const [target, setTarget] = useState<AgentsTarget>('AGENTS.local.md');
  const filePath = `${workdir}/${target}`;
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
  // 初始默认 target 探测完成前，load effect 不主动读文件，避免与探测重复请求。
  const pickedDefaultRef = useRef(false);

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

  // 打开时探测两个文件，决定默认选中哪个：
  // AGENTS.local.md 有内容就选它（覆盖“都有内容”“只有 local”两种情况），
  // 否则选 AGENTS.md（覆盖“都为空”“只有 md”两种情况）。
  // 探测时已读到选中文件内容，直接填入，避免 load effect 重复请求。
  useEffect(() => {
    let cancelled = false;
    const pick = async () => {
      setLoading(true);
      const read = async (name: AgentsTarget): Promise<string> => {
        try {
          const data = await readFile(`${workdir}/${name}`);
          return data.code === 0 ? (data.content || '') : '';
        } catch {
          return '';
        }
      };
      const [localContent, mdContent] = await Promise.all([
        read('AGENTS.local.md'),
        read('AGENTS.md'),
      ]);
      if (cancelled) return;
      const picked: AgentsTarget = localContent.trim() ? 'AGENTS.local.md' : 'AGENTS.md';
      const next = picked === 'AGENTS.local.md' ? localContent : mdContent;
      contentRef.current = next;
      pickedDefaultRef.current = true;
      setTarget(picked);
      setContent(next);
      setLoading(false);
      setDirty(false);
      setSaveStatus('idle');
      setLastSavedAt(null);
    };
    pick();
    return () => { cancelled = true; };
    // 仅在挂载时探测一次；workdir 在编辑器生命周期内不会变化。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    // 默认 target 探测会自行填充首次内容，避免与本 effect 重复读取。
    if (!pickedDefaultRef.current) return;
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
      // 编辑器只写 AGENTS.* 真实内容；对应的 CLAUDE.* 仅写一个引用指针，
      // 让 Claude Code 通过 @./AGENTS(.local).md 读取，无需维护两份全文。
      const agentsResult = await writeFile(filePath, saved);
      if (agentsResult.code !== 0) {
        setSaveStatus('error');
        return;
      }
      const pointer = CLAUDE_POINTER[target];
      const claudeResult = await writeFile(`${workdir}/${pointer.file}`, pointer.content);
      if (claudeResult.code !== 0) {
        // AGENTS.* 已保存，CLAUDE.* 指针写入失败。保持 dirty=true 让用户重试。
        setSaveStatus('error');
        return;
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
  }, [filePath, content, writeFile, target, workdir]);

  const handleTargetChange = useCallback((next: AgentsTarget) => {
    if (next === target) return;
    // 切换文件前若有未保存改动，先确认，避免静默丢失正在编辑的内容。
    if (dirty && !window.confirm('当前文件有未保存的修改，切换将丢弃这些修改，确定继续？')) {
      return;
    }
    setTarget(next);
  }, [target, dirty]);

  return (
    <>
      <div className="agents-editor-overlay" ref={overlayRef} onClick={onClose} />
      <div className="agents-editor" ref={editorRef}>
      <div className="agents-editor-header">
        <div className="agents-editor-target-tabs">
          {AGENTS_TARGETS.map((opt) => (
            <button
              key={opt}
              type="button"
              className={`agents-editor-target-tab ${target === opt ? 'active' : ''}`}
              onClick={() => handleTargetChange(opt)}
              disabled={saving}
            >
              {opt}
            </button>
          ))}
        </div>
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
