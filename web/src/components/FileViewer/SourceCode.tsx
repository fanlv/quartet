import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import type { CSSProperties, RefObject } from 'react';
import { useTranslation } from 'react-i18next';
import { detectLanguage, tokenizeLine } from '../../utils/syntaxHighlight';

const BLOCK_LINES = 50;
const OVERSCAN_PIXELS = 500;

interface SourceCodeProps {
  lines: readonly string[];
  path: string;
  classPrefix: 'file-viewer' | 'file-preview-source';
  scrollClassName: string;
  wrapText?: boolean;
  line?: number;
  endLine?: number;
}

interface SourceBlockProps {
  lines: readonly string[];
  start: number;
  language: string | null;
  classPrefix: SourceCodeProps['classPrefix'];
  scrollRef: RefObject<HTMLDivElement | null>;
  layoutKey: string;
  estimatedHeight: number;
  onVisible: () => void;
  line?: number;
  endLine?: number;
}

const HighlightedLine = memo(function HighlightedLine({ line, language }: { line: string; language: string | null }) {
  const tokens = useMemo(() => tokenizeLine(line, language), [line, language]);
  return <>{tokens.map((token, index) => token.type
    ? <span key={index} className={`hl-${token.type}`}>{token.value}</span>
    : token.value)}{line === '' && '\u00a0'}</>;
});

const SourceBlock = memo(function SourceBlock({
  lines, start, language, classPrefix, scrollRef, layoutKey, estimatedHeight, onVisible, line, endLine,
}: SourceBlockProps) {
  const blockRef = useRef<HTMLDivElement>(null);
  const measuredHeightRef = useRef<{ height: number; layoutKey: string; lines: readonly string[] } | null>(null);
  const end = Math.min(lines.length, start + BLOCK_LINES);
  const containsTarget = line !== undefined && line > start && line <= end;
  const [visible, setVisible] = useState(start === 0 || containsTarget);

  // The parent scroller's ref is attached after its children mount. Register
  // visibility observers after all refs are ready, including on the first load.
  useEffect(() => {
    const block = blockRef.current;
    const root = scrollRef.current;
    if (!block || !root) return;
    if (typeof IntersectionObserver === 'undefined') {
      setVisible(true);
      return;
    }
    const observer = new IntersectionObserver(([entry]) => setVisible(entry.isIntersecting), {
      root,
      rootMargin: `${OVERSCAN_PIXELS}px 0px`,
    });
    observer.observe(block);
    return () => observer.disconnect();
  }, [scrollRef]);

  useLayoutEffect(() => {
    const block = blockRef.current;
    if (!block || !visible) return;
    const measure = () => {
      measuredHeightRef.current = { height: block.getBoundingClientRect().height, layoutKey, lines };
    };
    measure();
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(measure);
    observer?.observe(block);
    return () => observer?.disconnect();
  }, [visible, layoutKey, lines]);

  useEffect(() => {
    if (visible) onVisible();
  }, [visible, onVisible]);

  const rowClass = classPrefix === 'file-viewer' ? 'file-viewer-row' : 'file-preview-source-line';
  const cachedHeight = measuredHeightRef.current;
  const placeholderHeight = cachedHeight?.layoutKey === layoutKey && cachedHeight.lines === lines
    ? cachedHeight.height : estimatedHeight;

  return (
    <div
      ref={blockRef}
      role="rowgroup"
      data-source-start={start + 1}
      style={visible ? undefined : { height: placeholderHeight }}
    >
      {visible && lines.slice(start, end).map((source, index) => {
        const lineNumber = start + index + 1;
        const highlighted = line !== undefined && lineNumber >= line && lineNumber <= (endLine ?? line);
        return (
          <div
            key={lineNumber}
            className={`${rowClass}${highlighted ? ' file-viewer-line-highlight' : ''}`}
            role="row"
            aria-rowindex={lineNumber}
          >
            <div className={`${classPrefix}-line-number`} role="cell">{lineNumber}</div>
            <div className={`${classPrefix}-line-content`} role="cell">
              <HighlightedLine line={source} language={language} />
            </div>
          </div>
        );
      })}
    </div>
  );
});

/**
 * Share one source renderer across the modal and standalone preview. Offscreen
 * blocks retain their height but release their rows and highlighted tokens.
 * Measuring visible blocks also handles variable-height wrapped lines.
 */
export function SourceCode({ lines, path, classPrefix, scrollClassName, wrapText = false, line, endLine }: SourceCodeProps) {
  const { t } = useTranslation();
  const scrollRef = useRef<HTMLDivElement>(null);
  const codeRef = useRef<HTMLDivElement>(null);
  const scrolledTargetRef = useRef('');
  const readingAnchorRef = useRef<{ line: number; offset: number; height: number } | null>(null);
  const pendingAnchorRef = useRef<typeof readingAnchorRef.current>(null);
  const previousWrapRef = useRef(wrapText);
  const language = useMemo(() => detectLanguage(path), [path]);
  const [metrics, setMetrics] = useState({ width: 800, lineHeight: 22, characterWidth: 8, horizontalPadding: 100, tabSize: 4 });
  const metricsRef = useRef(metrics);
  const [measured, setMeasured] = useState(false);

  const captureAnchor = useCallback(() => {
    const root = scrollRef.current;
    if (!root || pendingAnchorRef.current) return;
    const top = root.getBoundingClientRect().top;
    for (const row of codeRef.current?.querySelectorAll<HTMLElement>('[role="row"]') || []) {
      const rect = row.getBoundingClientRect();
      if (rect.bottom > top && rect.top < top + root.clientHeight) {
        readingAnchorRef.current = { line: Number(row.getAttribute('aria-rowindex')), offset: rect.top - top, height: rect.height };
        break;
      }
    }
  }, []);

  useLayoutEffect(() => {
    const root = scrollRef.current;
    const code = codeRef.current;
    if (!root || !code) return;
    const measure = () => {
      const style = getComputedStyle(code);
      const number = code.querySelector<HTMLElement>(`.${classPrefix}-line-number`);
      const content = code.querySelector<HTMLElement>(`.${classPrefix}-line-content`);
      const contentStyle = content ? getComputedStyle(content) : style;
      const canvas = document.createElement('canvas').getContext('2d');
      if (canvas) canvas.font = `${style.fontSize} ${style.fontFamily}`;
      const next = {
        width: root.clientWidth,
        lineHeight: parseFloat(style.lineHeight) || 22,
        characterWidth: canvas?.measureText('0').width || 8,
        horizontalPadding: (number?.getBoundingClientRect().width || 50)
          + parseFloat(contentStyle.paddingLeft || '0') + parseFloat(contentStyle.paddingRight || '0'),
        tabSize: parseFloat(contentStyle.tabSize) || 4,
      };
      const previous = metricsRef.current;
      if (Object.keys(next).some((key) => next[key as keyof typeof next] !== previous[key as keyof typeof next])) {
        pendingAnchorRef.current = readingAnchorRef.current;
        metricsRef.current = next;
        setMetrics(next);
      }
      setMeasured(true);
    };
    measure();
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(measure);
    observer?.observe(root);
    return () => observer?.disconnect();
  }, [classPrefix]);

  useLayoutEffect(() => {
    if (previousWrapRef.current !== wrapText) {
      pendingAnchorRef.current ??= readingAnchorRef.current;
      previousWrapRef.current = wrapText;
    }
    const anchor = pendingAnchorRef.current;
    const root = scrollRef.current;
    const target = anchor && codeRef.current?.querySelector<HTMLElement>(`[aria-rowindex="${anchor.line}"]`);
    if (target && root && anchor) {
      const rect = target.getBoundingClientRect();
      const offset = anchor.offset < 0 && anchor.height > 0 ? anchor.offset * rect.height / anchor.height : anchor.offset;
      root.scrollTop += rect.top - root.getBoundingClientRect().top - offset;
    }
    pendingAnchorRef.current = null;
    captureAnchor();
  }, [metrics, wrapText, captureAnchor]);

  // Center a requested line after font/viewport measurement, once per target.
  // Later window resizes must leave the user's current reading position alone.
  useLayoutEffect(() => {
    if (!measured || line === undefined) return;
    const targetKey = `${path}:${line}`;
    if (scrolledTargetRef.current === targetKey) return;
    const target = codeRef.current?.querySelector<HTMLElement>(`[aria-rowindex="${line}"]`);
    const root = scrollRef.current;
    if (!target || !root) return;
    root.scrollTop += target.getBoundingClientRect().top - root.getBoundingClientRect().top
      - (root.clientHeight - target.getBoundingClientRect().height) / 2;
    scrolledTargetRef.current = targetKey;
  }, [measured, path, line, metrics]);

  const columns = useMemo(() => lines.map((source) => source.length
    + (source.match(/\t/g)?.length || 0) * (metrics.tabSize - 1)
    + (source.match(/[\u2e80-\ua4cf\uac00-\ud7a3\uf900-\ufaff\uff01-\uff60]/g)?.length || 0)), [lines, metrics.tabSize]);
  const layout = useMemo(() => {
    const availableColumns = Math.max(1, Math.floor((metrics.width - metrics.horizontalPadding) / metrics.characterWidth));
    const blocks: { start: number; height: number }[] = [];
    let maxColumns = 0;
    for (let start = 0; start < lines.length; start += BLOCK_LINES) {
      let height = 0;
      for (let index = start; index < Math.min(lines.length, start + BLOCK_LINES); index += 1) {
        maxColumns = Math.max(maxColumns, columns[index]);
        height += metrics.lineHeight * (wrapText ? Math.max(1, Math.ceil(columns[index] / availableColumns)) : 1);
      }
      blocks.push({ start, height });
    }
    return { blocks, width: maxColumns * metrics.characterWidth + metrics.horizontalPadding };
  }, [lines.length, columns, metrics, wrapText]);
  const codeStyle = {
    '--source-line-digits': String(lines.length).length,
    ...(!wrapText && { width: layout.width }),
  } as CSSProperties;
  const layoutKey = `${wrapText}:${wrapText ? metrics.width : 0}:${metrics.lineHeight}:${metrics.characterWidth}:${metrics.horizontalPadding}`;

  return (
    <div ref={scrollRef} className={scrollClassName} onScroll={captureAnchor}>
      <div
        ref={codeRef}
        className={`${classPrefix}-code${wrapText ? ' is-wrapped' : ''}`}
        style={codeStyle}
        role="table"
        aria-label={t('fileViewer.content')}
        aria-rowcount={lines.length}
      >
        {layout.blocks.map(({ start, height }) => (
          <SourceBlock
            key={`${path}:${line}:${start}`}
            lines={lines}
            start={start}
            language={language}
            classPrefix={classPrefix}
            scrollRef={scrollRef}
            layoutKey={layoutKey}
            estimatedHeight={height}
            onVisible={captureAnchor}
            line={line}
            endLine={endLine}
          />
        ))}
      </div>
    </div>
  );
}
