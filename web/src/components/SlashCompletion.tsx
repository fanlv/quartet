// Presentational pieces of the shared slash ("/") completion — see
// utils/slashCompletion.ts for the hook, keyboard helper and segment splitter.

import { Fragment, useEffect, useRef } from 'react';
import type { RefObject } from 'react';
import { useTranslation } from 'react-i18next';
import { splitSkillSegments } from '../utils/slashCompletion';
import type { SlashItem } from '../utils/slashCompletion';

/** Grouped floater listing commands + skills. Anchored above the input via
 *  .chat-slash-floater (absolute); the caller renders it inside a
 *  position:relative wrapper. */
export function SlashFloater({ items, activeIdx, onPick, onActiveIdxChange }: {
  items: SlashItem[];
  activeIdx: number;
  onPick: (item: SlashItem) => void;
  onActiveIdxChange?: (idx: number) => void;
}) {
  const { t } = useTranslation();
  const floaterRef = useRef<HTMLDivElement>(null);

  // Keep the keyboard-active row visible while ArrowUp/Down navigating a
  // list taller than the floater's max-height.
  useEffect(() => {
    const el = floaterRef.current?.querySelector('.chat-slash-item.active');
    // `?.` on the method itself: jsdom (component tests) lacks scrollIntoView.
    el?.scrollIntoView?.({ block: 'nearest' });
  }, [activeIdx, items]);

  if (items.length === 0) return null;
  return (
    <div className="chat-slash-floater" ref={floaterRef}>
      {items.map((item, i) => (
        <Fragment key={item.name}>
          {(i === 0 || items[i - 1].kind !== item.kind) && (
            <div className="chat-slash-group">
              {item.kind === 'command' ? t('chat.slashCommands') : t('chat.slashSkills')}
            </div>
          )}
          <div
            className={`chat-slash-item ${i === activeIdx ? 'active' : ''}`}
            onMouseEnter={() => onActiveIdxChange?.(i)}
            onMouseDown={(e) => {
              e.preventDefault();
              onPick(item);
            }}
          >
            <div className="chat-slash-item-main">
              <span className="chat-slash-item-name">{item.name}</span>
              {item.scope && (
                <span className={`chat-slash-item-scope chat-slash-scope-${item.scope}`}>{item.scope}</span>
              )}
            </div>
            {item.description && (
              <span className="chat-slash-item-desc">{item.description}</span>
            )}
          </div>
        </Fragment>
      ))}
    </div>
  );
}

/** Highlight layer under a transparent-text textarea. Must mirror the
 *  textarea's exact text metrics — page-specific padding/font-size come from
 *  `className` (e.g. .home-input-backdrop overrides). */
export function SkillBackdrop({ input, skillNameSet, className, backdropRef }: {
  input: string;
  skillNameSet: ReadonlySet<string>;
  className?: string;
  backdropRef: RefObject<HTMLDivElement | null>;
}) {
  return (
    <div
      className={`chat-input-backdrop${className ? ` ${className}` : ''}`}
      ref={backdropRef}
      aria-hidden="true"
    >
      {splitSkillSegments(input, skillNameSet).map((seg, i) => (
        <Fragment key={i}>
          {seg.chip ? <span className="chat-skill-chip">{seg.text}</span> : seg.text}
        </Fragment>
      ))}
      {input.endsWith('\n') ? ' ' : ''}
    </div>
  );
}
