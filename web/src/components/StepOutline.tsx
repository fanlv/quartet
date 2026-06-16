import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Message, MessageRoleEnum, MessageStatusEnum, ToolCallStatusEnum } from '../types';
import { DurationBadge } from './DurationBadge';
import './StepOutline.css';

// Sum every step's individual duration. Finished steps contribute a fixed
// (endedAt - startedAt) to baseMs; running steps pass their startedAt through
// so the badge keeps adding (now - startedAt) live. The badge computes
// baseMs + Σ(now - start), which equals the running total of all steps.
function buildTotal(steps: OutlineStep[]): { startedAt: number[]; baseMs: number; hasAny: boolean } {
  const startedAt: number[] = [];
  let baseMs = 0;
  let hasAny = false;
  for (const step of steps) {
    if (step.startedAt == null) continue;
    hasAny = true;
    if (step.running || step.endedAt == null) {
      startedAt.push(step.startedAt);
    } else {
      baseMs += Math.max(0, step.endedAt - step.startedAt);
    }
  }
  return { startedAt, baseMs, hasAny };
}

interface StepOutlineProps {
  messages: Message[];
  onJump: (messageId: string) => void;
  onClose?: () => void;
}

type StepKind = 'thinking' | 'assistant' | 'tool';

interface OutlineStep {
  // The message id the step maps to; clicking scrolls the matching
  // [data-message-id] bubble into view.
  messageId: string;
  // A thinking + body assistant message contributes two rows that share a
  // message id, so the key disambiguates them for React.
  key: string;
  kind: StepKind;
  label: string;
  // DurationBadge inputs. `startedAt === undefined` means "do not show a
  // duration" (e.g. legacy rows missing the needed timestamps); a running
  // step passes startedAt without endedAt so the badge ticks live.
  startedAt?: number;
  endedAt?: number;
  running: boolean;
}

// Derive a flat, ordered list of steps from the rendered message list. One
// message can yield up to two steps (a thinking segment + a body segment).
// Mirrors the duration logic in MessageItem so the outline and the bubbles
// agree on elapsed time.
function buildSteps(messages: Message[]): OutlineStep[] {
  const steps: OutlineStep[] = [];
  for (const message of messages) {
    if (message.role === MessageRoleEnum.ASSISTANT) {
      const isStreaming = message.status === MessageStatusEnum.Started;
      if (message.thinkingContent) {
        steps.push({
          messageId: message.id,
          key: `${message.id}:thinking`,
          kind: 'thinking',
          label: '',
          startedAt: message.isThinking || message.thinkingFinishedAt != null ? message.createdAt : undefined,
          endedAt: message.thinkingFinishedAt,
          running: !!message.isThinking,
        });
      }
      if (message.content?.trim()) {
        const startedAt = (() => {
          if (isStreaming) return message.thinkingFinishedAt ?? message.createdAt;
          if (message.finishedAt == null) return undefined;
          const hasThinking = !!message.thinkingContent;
          if (hasThinking && message.thinkingFinishedAt == null) return undefined;
          return message.thinkingFinishedAt ?? message.createdAt;
        })();
        steps.push({
          messageId: message.id,
          key: `${message.id}:body`,
          kind: 'assistant',
          label: message.isShellOutput ? 'Shell' : message.content.trim().replace(/\s+/g, ' '),
          startedAt,
          endedAt: message.finishedAt,
          running: isStreaming,
        });
      }
    } else if (message.role === MessageRoleEnum.TOOL) {
      const running = message.toolCallStatus === ToolCallStatusEnum.Processing;
      const shouldShowDuration = running || message.finishedAt != null;
      steps.push({
        messageId: message.id,
        key: `${message.id}:tool`,
        kind: 'tool',
        label: message.toolCallName || 'Tool',
        startedAt: shouldShowDuration ? message.createdAt : undefined,
        endedAt: message.finishedAt,
        running,
      });
    }
  }
  return steps;
}

function StepIcon({ kind }: { kind: StepKind }) {
  if (kind === 'thinking') return <span className="step-outline-icon">💭</span>;
  if (kind === 'tool') {
    return (
      <span className="step-outline-icon">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
          <path d="M14.7 6.3a4 4 0 00-5.4 5.4l-6.3 6.3a1 1 0 000 1.4l1.6 1.6a1 1 0 001.4 0l6.3-6.3a4 4 0 005.4-5.4l-2.6 2.6-2.4-2.4z" />
        </svg>
      </span>
    );
  }
  return <span className="step-outline-icon">✨</span>;
}

export function StepOutline({ messages, onJump, onClose }: StepOutlineProps) {
  const { t } = useTranslation();
  const steps = useMemo(() => buildSteps(messages), [messages]);
  const total = useMemo(() => buildTotal(steps), [steps]);

  return (
    <aside className="step-outline" data-testid="step-outline">
      <div className="step-outline-header">
        <span className="step-outline-title">{t('chat.outline.title')}</span>
        <span className="step-outline-count" data-testid="step-outline-count">
          {t('chat.outline.totalSteps', { count: steps.length })}
          {total.hasAny && (
            <DurationBadge startedAt={total.startedAt} baseMs={total.baseMs} variant="total" />
          )}
        </span>
        {onClose && (
          <button
            type="button"
            className="step-outline-close"
            onClick={onClose}
            aria-label={t('common.close')}
            title={t('common.close')}
          >
            ×
          </button>
        )}
      </div>
      <div className="step-outline-list" data-testid="step-outline-list">
        {steps.length === 0 ? (
          <div className="step-outline-empty">{t('chat.outline.empty')}</div>
        ) : (
          steps.map((step, index) => {
            const label =
              step.kind === 'thinking'
                ? t('chat.deepThinking')
                : step.kind === 'assistant'
                  ? step.label || t('chat.outline.assistant')
                  : step.label;
            return (
              <button
                type="button"
                key={step.key}
                className={`step-outline-item kind-${step.kind}${step.running ? ' running' : ''}`}
                data-testid="step-outline-item"
                onClick={() => onJump(step.messageId)}
                title={label}
              >
                <span className="step-outline-index">{index + 1}</span>
                <StepIcon kind={step.kind} />
                <span className="step-outline-label">{label}</span>
                <DurationBadge
                  startedAt={step.startedAt}
                  endedAt={step.endedAt}
                  variant={step.kind === 'thinking' ? 'thinking' : step.kind === 'tool' ? 'tool' : 'assistant'}
                />
              </button>
            );
          })
        )}
      </div>
    </aside>
  );
}
