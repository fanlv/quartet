import { useState, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Trans } from 'react-i18next';
import { Script } from '../types';
import { ShellEditor } from './ShellEditor';
import './ScriptManagePanel.css';

interface ScriptManagePanelProps {
  onClose?: () => void;
  embedded?: boolean;
}

async function fetchScripts(): Promise<Script[]> {
  const res = await fetch('/api/v1/script/list');
  if (!res.ok) return [];
  const data = await res.json();
  return data.scripts || [];
}

async function saveScriptApi(req: {
  id?: string;
  name: string;
  content: string;
  description: string;
}): Promise<Script | null> {
  const res = await fetch('/api/v1/script/save', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  });
  if (!res.ok) return null;
  const data = await res.json();
  return data.script || null;
}

async function deleteScriptApi(id: string): Promise<boolean> {
  const res = await fetch(`/api/v1/script/${id}`, { method: 'DELETE' });
  return res.ok;
}

function formatTime(t: string): string {
  if (!t) return '';
  const d = new Date(t);
  const now = new Date();
  const diff = now.getTime() - d.getTime();
  if (diff < 60000) return '刚刚';
  if (diff < 3600000) return `${Math.floor(diff / 60000)}分钟前`;
  if (diff < 86400000) return `${Math.floor(diff / 3600000)}小时前`;
  if (diff < 2592000000) return `${Math.floor(diff / 86400000)}天前`;
  return d.toLocaleDateString();
}

export function ScriptManagePanel({ onClose, embedded }: ScriptManagePanelProps) {
  const { t } = useTranslation();
  const [scripts, setScripts] = useState<Script[]>([]);
  const [search, setSearch] = useState('');
  const [editingScript, setEditingScript] = useState<Partial<Script> | null>(null);
  const [deleteId, setDeleteId] = useState('');
  const [saving, setSaving] = useState(false);

  const loadScripts = useCallback(async () => {
    const list = await fetchScripts();
    setScripts(list);
  }, []);

  // Fetch on mount. Although loadScripts eventually calls setScripts, the
  // setState happens inside an async continuation (after `await`), not
  // synchronously in the effect body — the react-hooks rule's static analysis
  // can't tell the difference, hence the disable.
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    loadScripts();
  }, [loadScripts]);

  // Filtered list
  const filtered = scripts.filter((s) => {
    if (search) {
      const q = search.toLowerCase();
      return (
        s.name.toLowerCase().includes(q) ||
        s.content.toLowerCase().includes(q) ||
        s.description.toLowerCase().includes(q)
      );
    }
    return true;
  });

  const handleSave = async () => {
    if (!editingScript || !editingScript.name?.trim() || !editingScript.content?.trim()) return;
    setSaving(true);
    const result = await saveScriptApi({
      id: editingScript.id || undefined,
      name: editingScript.name.trim(),
      content: editingScript.content.trim(),
      description: (editingScript.description || '').trim(),
    });
    setSaving(false);
    if (result) {
      setEditingScript(null);
      await loadScripts();
    }
  };

  const handleDelete = async () => {
    if (!deleteId) return;
    await deleteScriptApi(deleteId);
    setDeleteId('');
    await loadScripts();
  };

  // Close on Escape
  useEffect(() => {
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        if (deleteId) {
          setDeleteId('');
        } else if (editingScript) {
          setEditingScript(null);
        } else {
          onClose?.();
        }
      }
    };
    window.addEventListener('keydown', handleKey);
    return () => window.removeEventListener('keydown', handleKey);
  }, [deleteId, editingScript, onClose]);

  const panelContent = (
    <div className={`script-manage-panel ${embedded ? 'embedded' : ''}`} onClick={(e) => e.stopPropagation()}>
      {!embedded && (
        <div className="script-manage-header">
          <h3>{t('settings.tabs.script')}</h3>
          <button className="script-manage-close" onClick={onClose}>
            &times;
          </button>
        </div>
      )}

        <div className="script-manage-body">
          {editingScript ? (
            /* Edit / Create Form */
            <div className="script-edit-form">
              <button className="script-edit-back" onClick={() => setEditingScript(null)}>
                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                  <path d="M19 12H5M12 19l-7-7 7-7" />
                </svg>
                {t('script.backToList')}
              </button>

              <div className="script-edit-field">
                <label>{t('script.form.name')}</label>
                <input
                  type="text"
                  value={editingScript.name || ''}
                  onChange={(e) => setEditingScript({ ...editingScript, name: e.target.value })}
                  placeholder={t('script.form.namePlaceholder')}
                  autoFocus
                />
              </div>

              <div className="script-edit-field">
                <label>{t('script.form.description')}</label>
                <input
                  type="text"
                  value={editingScript.description || ''}
                  onChange={(e) => setEditingScript({ ...editingScript, description: e.target.value })}
                  placeholder={t('script.form.descriptionPlaceholder')}
                />
              </div>

              <div className="script-edit-field">
                <label>{t('script.form.content')}</label>
                <ShellEditor
                  value={editingScript.content || ''}
                  onChange={(val) => setEditingScript({ ...editingScript, content: val })}
                  placeholder={t('script.form.contentPlaceholder', { varTag: '{{varName}}' })}
                />
                <div className="loop-round-inline-note">
                  <Trans
                    i18nKey="script.form.note"
                    values={{
                      setCmd: 'quartet_set "var" "value"',
                      varTag: '{{varName}}',
                      breakCmd: 'quartet_break',
                      returnCmd: 'quartet_return',
                    }}
                    components={[<code />, <code />, <code />, <code />]}
                  />
                </div>
              </div>

              <div className="script-edit-actions">
                <button className="script-edit-cancel" onClick={() => setEditingScript(null)}>
                  {t('common.cancel')}
                </button>
                <button
                  className="script-edit-save"
                  onClick={handleSave}
                  disabled={!editingScript.name?.trim() || !editingScript.content?.trim() || saving}
                >
                  {saving ? t('script.saving') : editingScript.id ? t('script.update') : t('script.create')}
                </button>
              </div>
            </div>
          ) : (
            /* List View */
            <>
              <div className="script-toolbar">
                <input
                  className="script-search-input"
                  type="text"
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                  placeholder={t('script.searchPlaceholder')}
                />
                <button
                  className="script-add-btn"
                  onClick={() => setEditingScript({ name: '', content: '', description: '' })}
                >
                  {t('script.newShell')}
                </button>
              </div>

              <div className="script-list">
                {filtered.length === 0 ? (
                  <div className="script-list-empty">
                    {scripts.length === 0 ? t('script.noScripts') : t('script.noMatch')}
                  </div>
                ) : (
                  filtered.map((s) => (
                    <div
                      key={s.id}
                      className="script-card"
                      onClick={() =>
                        setEditingScript({
                          id: s.id,
                          name: s.name,
                          content: s.content,
                          description: s.description,
                        })
                      }
                    >
                      <div className="script-card-header">
                        <span className="script-card-name">{s.name}</span>
                        <div className="script-card-actions">
                          <button
                            className="script-card-action-btn delete"
                            title={t('common.delete')}
                            onClick={(e) => {
                              e.stopPropagation();
                              setDeleteId(s.id);
                            }}
                          >
                            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                              <path d="M3 6h18M8 6V4a2 2 0 012-2h4a2 2 0 012 2v2m3 0v14a2 2 0 01-2 2H7a2 2 0 01-2-2V6h14" />
                            </svg>
                          </button>
                        </div>
                      </div>
                      {s.description && (
                        <div className="script-card-desc">{s.description}</div>
                      )}
                      <div className="script-card-content-preview">
                        {s.content.length > 100 ? s.content.slice(0, 100) + '...' : s.content}
                      </div>
                      <div className="script-card-footer">
                        <span className="script-card-time">{formatTime(s.updatedAt)}</span>
                      </div>
                    </div>
                  ))
                )}
              </div>
            </>
          )}
        </div>

      {/* Delete confirmation */}
      {deleteId && (
        <div className="script-delete-overlay" onClick={() => setDeleteId('')}>
          <div className="script-delete-dialog" onClick={(e) => e.stopPropagation()}>
            <h4>{t('script.confirmDeleteTitle')}</h4>
            <p>{t('script.confirmDeleteMessage', { name: scripts.find((s) => s.id === deleteId)?.name })}</p>
            <div className="script-delete-actions">
              <button onClick={() => setDeleteId('')}>{t('common.cancel')}</button>
              <button className="script-delete-confirm-btn" onClick={handleDelete}>
                {t('common.delete')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );

  if (embedded) {
    return panelContent;
  }

  return (
    <div className="script-manage-overlay" onClick={onClose}>
      {panelContent}
    </div>
  );
}
