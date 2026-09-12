import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react';
import './MermaidDiagram.css';

type MermaidAPI = typeof import('mermaid')['default'];

let mermaidPromise: Promise<MermaidAPI> | null = null;
const minZoom = 0.25;
const maxZoom = 2;
const zoomStep = 0.25;

function loadMermaid(): Promise<MermaidAPI> {
  if (!mermaidPromise) {
    mermaidPromise = import('mermaid').then(({ default: mermaid }) => {
      mermaid.initialize({
        startOnLoad: false,
        securityLevel: 'strict',
        suppressErrorRendering: true,
        theme: 'base',
        look: 'classic',
        fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', 'Noto Sans SC', sans-serif",
        themeVariables: {
          background: '#ffffff',
          primaryColor: '#edf9f0',
          primaryTextColor: '#142019',
          primaryBorderColor: '#72bd87',
          secondaryColor: '#f3f7f4',
          secondaryTextColor: '#29362d',
          secondaryBorderColor: '#a8b6ac',
          tertiaryColor: '#f8fbf8',
          tertiaryTextColor: '#29362d',
          tertiaryBorderColor: '#cdd9d0',
          lineColor: '#5d6c62',
          edgeLabelBackground: '#ffffff',
          clusterBkg: '#f8fbf8',
          clusterBorder: '#cdd9d0',
          noteBkgColor: '#fff7e7',
          noteBorderColor: '#e5bd73',
          noteTextColor: '#4a3a1f',
        },
        flowchart: {
          curve: 'basis',
          htmlLabels: false,
          useMaxWidth: false,
        },
      });
      return mermaid;
    }).catch((error) => {
      mermaidPromise = null;
      throw error;
    });
  }
  return mermaidPromise;
}

function fullErrorDetail(error: unknown): string {
  if (error instanceof Error) return error.stack || `${error.name}: ${error.message}`;
  return String(error);
}

function computeFitZoom(container: HTMLDivElement, naturalWidth: number): number {
  if (naturalWidth <= 0) return 1;
  const style = window.getComputedStyle(container);
  const horizontalPadding = Number.parseFloat(style.paddingLeft) + Number.parseFloat(style.paddingRight);
  const available = container.clientWidth - horizontalPadding;
  if (!Number.isFinite(available) || available <= 0) return 1;
  return Math.min(1, available / naturalWidth);
}

export function MermaidDiagram({ source }: { source: string }) {
  const reactId = useId();
  const renderId = useMemo(() => `mermaid-${reactId.replace(/[^a-zA-Z0-9_-]/g, '')}`, [reactId]);
  const containerRef = useRef<HTMLDivElement>(null);
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading');
  const [error, setError] = useState('');
  const [naturalWidth, setNaturalWidth] = useState(0);
  const [zoom, setZoom] = useState(1);

  useEffect(() => {
    let cancelled = false;
    const container = containerRef.current;
    setStatus('loading');
    setError('');
    setNaturalWidth(0);
    setZoom(1);
    container?.replaceChildren();

    void loadMermaid()
      .then((mermaid) => mermaid.render(renderId, source))
      .then(({ svg, bindFunctions }) => {
        if (cancelled || !container) return;
        container.innerHTML = svg;
        const svgElement = container.querySelector('svg');
        const width = svgElement?.viewBox.baseVal.width;
        if (svgElement && width && Number.isFinite(width)) {
          svgElement.style.width = `${Math.ceil(width)}px`;
          svgElement.style.maxWidth = 'none';
          svgElement.style.height = 'auto';
          const natural = Math.ceil(width);
          setNaturalWidth(natural);
          setZoom(computeFitZoom(container, natural));
        }
        bindFunctions?.(container);
        setStatus('ready');
      })
      .catch((reason: unknown) => {
        if (cancelled) return;
        console.error('[MermaidDiagram] Mermaid render failed', reason);
        setError(fullErrorDetail(reason));
        setStatus('error');
      });

    return () => {
      cancelled = true;
      container?.replaceChildren();
    };
  }, [renderId, source]);

  useEffect(() => {
    const svgElement = containerRef.current?.querySelector('svg');
    if (!svgElement || !naturalWidth) return;
    svgElement.style.width = `${Math.round(naturalWidth * zoom)}px`;
  }, [naturalWidth, zoom]);

  const updateZoom = useCallback((nextZoom: number) => {
    setZoom(Math.min(maxZoom, Math.max(minZoom, nextZoom)));
  }, []);

  const fitToWidth = useCallback(() => {
    const container = containerRef.current;
    if (!container || !naturalWidth) return;
    updateZoom(computeFitZoom(container, naturalWidth));
    container.scrollLeft = 0;
  }, [naturalWidth, updateZoom]);

  if (status === 'error') {
    return (
      <div className="mermaid-diagram-error" role="alert">
        <strong>Mermaid 图表渲染失败</strong>
        <pre>{error}</pre>
        <details>
          <summary>查看 Mermaid 源文</summary>
          <pre>{source}</pre>
        </details>
      </div>
    );
  }

  return (
    <figure className={`mermaid-diagram ${status === 'loading' ? 'is-loading' : ''}`}>
      {status === 'loading' && (
        <div className="mermaid-diagram-loading" role="status">
          <span className="mermaid-diagram-spinner" />
          <span>正在渲染图表…</span>
        </div>
      )}
      {status === 'ready' && (
        <figcaption className="mermaid-diagram-toolbar">
          <span>Mermaid</span>
          <div className="mermaid-diagram-zoom" role="group" aria-label="图表缩放">
            <button type="button" title="缩小" aria-label="缩小图表" disabled={zoom <= minZoom} onClick={() => updateZoom(zoom - zoomStep)}>
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12h14" /></svg>
            </button>
            <button type="button" className="mermaid-diagram-percent" title="重置为 100%" aria-label={`当前缩放 ${Math.round(zoom * 100)}%，点击重置为 100%`} onClick={() => updateZoom(1)}>
              {Math.round(zoom * 100)}%
            </button>
            <button type="button" title="放大" aria-label="放大图表" disabled={zoom >= maxZoom} onClick={() => updateZoom(zoom + zoomStep)}>
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5v14M5 12h14" /></svg>
            </button>
            <button type="button" title="适应宽度" aria-label="图表适应宽度" onClick={fitToWidth}>
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m8 3-5 5 5 5M3 8h7M16 3l5 5-5 5M21 8h-7M8 21l-5-5 5-5M3 16h7M16 21l5-5-5-5M21 16h-7" /></svg>
            </button>
          </div>
        </figcaption>
      )}
      <div ref={containerRef} className="mermaid-diagram-canvas" role="img" aria-label="Mermaid 图表" />
    </figure>
  );
}
