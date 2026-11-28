import { useId, useLayoutEffect, useRef, type FormEventHandler, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { TimerIcon } from './TimerPresentation';
import './ScheduleEditModal.css';

export function TimerDialog({ title, busy, onClose, onSubmit, children, actions, compact }: {
  title: string;
  busy: boolean;
  onClose: () => void;
  onSubmit: FormEventHandler<HTMLFormElement>;
  children: ReactNode;
  actions: ReactNode;
  compact?: boolean;
}) {
  const { t } = useTranslation();
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  const bodyRef = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    const dialog = ref.current;
    dialog?.showModal();
    return () => dialog?.close();
  }, []);

  useLayoutEffect(() => {
    bodyRef.current?.scrollTo({ top: 0 });
  }, [title]);

  return (
    <dialog
      ref={ref}
      className="timer-dialog"
      data-compact={compact || undefined}
      aria-labelledby={titleId}
      onCancel={event => { event.preventDefault(); if (!busy) onClose(); }}
      onClick={event => { if (!busy && event.target === event.currentTarget) onClose(); }}
    >
      <form className="timer-dialog-form" onSubmit={onSubmit} aria-busy={busy}>
        <header className="timer-dialog-header">
          <h2 id={titleId}>{title}</h2>
          <button type="button" className="timer-button timer-button-icon timer-button-quiet" aria-label={t('common.close')} disabled={busy} onClick={onClose}>
            <TimerIcon name="close" />
          </button>
        </header>
        <div ref={bodyRef} className="timer-dialog-body">{children}</div>
        <footer className="timer-dialog-footer">{actions}</footer>
      </form>
    </dialog>
  );
}
