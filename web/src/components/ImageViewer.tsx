import { useCallback, useEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent, type WheelEvent as ReactWheelEvent } from 'react';
import { useTranslation } from 'react-i18next';
import './ImageViewer.css';

interface ImageViewerProps {
  src: string;
  alt?: string;
  onClose: () => void;
}

interface Point {
  x: number;
  y: number;
}

interface PinchStart {
  distance: number;
  scale: number;
  position: Point;
  center: Point;
}

const MIN_SCALE = 0.25;
const MAX_SCALE = 5;
const ZOOM_STEP = 0.25;

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

function distanceBetween(points: Point[]): number {
  if (points.length < 2) return 0;
  return Math.hypot(points[0].x - points[1].x, points[0].y - points[1].y);
}

function centerBetween(points: Point[]): Point {
  if (points.length < 2) return points[0] || { x: 0, y: 0 };
  return {
    x: (points[0].x + points[1].x) / 2,
    y: (points[0].y + points[1].y) / 2,
  };
}

function imageName(src: string, alt?: string): string {
  if (alt?.trim()) return alt.trim();
  if (src.startsWith('data:')) return 'Image';
  let sourcePath = src.split(/[?#]/, 1)[0];
  try {
    const parsed = new URL(src, window.location.origin);
    sourcePath = parsed.searchParams.get('path') || parsed.pathname;
  } catch {
    // Keep the path-only fallback for malformed or non-standard image URLs.
  }
  const name = sourcePath.split('/').filter(Boolean).pop();
  if (!name) return 'Image';
  try {
    return decodeURIComponent(name);
  } catch {
    return name;
  }
}

export function ImageViewer({ src, alt, onClose }: ImageViewerProps) {
  const { t } = useTranslation();
  const dialogRef = useRef<HTMLDivElement>(null);
  const stageRef = useRef<HTMLDivElement>(null);
  const imageRef = useRef<HTMLImageElement>(null);
  const pointersRef = useRef(new Map<number, Point>());
  const dragStartRef = useRef<{ pointer: Point; position: Point } | null>(null);
  const pinchStartRef = useRef<PinchStart | null>(null);
  const scaleRef = useRef(1);
  const positionRef = useRef<Point>({ x: 0, y: 0 });
  const [scale, setScale] = useState(1);
  const [position, setPosition] = useState<Point>({ x: 0, y: 0 });
  const [dragging, setDragging] = useState(false);
  const [loadError, setLoadError] = useState(false);
  const name = useMemo(() => imageName(src, alt), [src, alt]);

  const commitScale = useCallback((nextScale: number) => {
    const clamped = clamp(nextScale, MIN_SCALE, MAX_SCALE);
    scaleRef.current = clamped;
    setScale(clamped);
    return clamped;
  }, []);

  const clampPosition = useCallback((next: Point, nextScale = scaleRef.current): Point => {
    const stage = stageRef.current;
    const image = imageRef.current;
    if (!stage || !image || nextScale <= 1) return { x: 0, y: 0 };

    const maxX = Math.max(0, (image.offsetWidth * nextScale - stage.clientWidth) / 2);
    const maxY = Math.max(0, (image.offsetHeight * nextScale - stage.clientHeight) / 2);
    return {
      x: clamp(next.x, -maxX, maxX),
      y: clamp(next.y, -maxY, maxY),
    };
  }, []);

  const commitPosition = useCallback((next: Point, nextScale = scaleRef.current) => {
    const clamped = clampPosition(next, nextScale);
    positionRef.current = clamped;
    setPosition(clamped);
  }, [clampPosition]);

  const resetView = useCallback(() => {
    commitScale(1);
    commitPosition({ x: 0, y: 0 }, 1);
  }, [commitPosition, commitScale]);

  const zoomAt = useCallback((requestedScale: number, clientPoint?: Point) => {
    const previousScale = scaleRef.current;
    const nextScale = commitScale(requestedScale);
    if (nextScale <= 1) {
      commitPosition({ x: 0, y: 0 }, nextScale);
      return;
    }

    const stage = stageRef.current;
    if (!stage || !clientPoint) {
      commitPosition(positionRef.current, nextScale);
      return;
    }

    const bounds = stage.getBoundingClientRect();
    const focus = {
      x: clientPoint.x - bounds.left - bounds.width / 2,
      y: clientPoint.y - bounds.top - bounds.height / 2,
    };
    const ratio = nextScale / previousScale;
    commitPosition({
      x: focus.x - (focus.x - positionRef.current.x) * ratio,
      y: focus.y - (focus.y - positionRef.current.y) * ratio,
    }, nextScale);
  }, [commitPosition, commitScale]);

  useEffect(() => {
    const previouslyFocused = document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null;
    dialogRef.current?.focus();

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onClose();
      } else if (event.key === '+' || event.key === '=') {
        event.preventDefault();
        zoomAt(scaleRef.current + ZOOM_STEP);
      } else if (event.key === '-') {
        event.preventDefault();
        zoomAt(scaleRef.current - ZOOM_STEP);
      } else if (event.key === '0') {
        event.preventDefault();
        resetView();
      }
    };

    window.addEventListener('keydown', handleKeyDown);
    return () => {
      window.removeEventListener('keydown', handleKeyDown);
      previouslyFocused?.focus();
    };
  }, [onClose, resetView, zoomAt]);

  useEffect(() => {
    resetView();
    setLoadError(false);
  }, [resetView, src]);

  useEffect(() => {
    const handleResize = () => commitPosition(positionRef.current);
    window.addEventListener('resize', handleResize);
    return () => window.removeEventListener('resize', handleResize);
  }, [commitPosition]);

  const handleWheel = (event: ReactWheelEvent<HTMLDivElement>) => {
    event.preventDefault();
    const direction = event.deltaY < 0 ? 1 : -1;
    zoomAt(scaleRef.current + direction * ZOOM_STEP, { x: event.clientX, y: event.clientY });
  };

  const handlePointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    event.currentTarget.setPointerCapture(event.pointerId);
    pointersRef.current.set(event.pointerId, { x: event.clientX, y: event.clientY });
    const points = [...pointersRef.current.values()];

    if (points.length === 1) {
      dragStartRef.current = { pointer: points[0], position: positionRef.current };
      setDragging(scaleRef.current > 1);
    } else if (points.length === 2) {
      dragStartRef.current = null;
      pinchStartRef.current = {
        distance: distanceBetween(points),
        scale: scaleRef.current,
        position: positionRef.current,
        center: centerBetween(points),
      };
      setDragging(true);
    }
  };

  const handlePointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (!pointersRef.current.has(event.pointerId)) return;
    pointersRef.current.set(event.pointerId, { x: event.clientX, y: event.clientY });
    const points = [...pointersRef.current.values()];

    if (points.length >= 2 && pinchStartRef.current) {
      const start = pinchStartRef.current;
      if (start.distance === 0) return;
      const nextScale = commitScale(start.scale * (distanceBetween(points) / start.distance));
      const stage = stageRef.current;
      const currentCenter = centerBetween(points);
      if (!stage) return;
      const bounds = stage.getBoundingClientRect();
      const startFocus = {
        x: start.center.x - bounds.left - bounds.width / 2,
        y: start.center.y - bounds.top - bounds.height / 2,
      };
      const currentFocus = {
        x: currentCenter.x - bounds.left - bounds.width / 2,
        y: currentCenter.y - bounds.top - bounds.height / 2,
      };
      const ratio = nextScale / start.scale;
      commitPosition({
        x: currentFocus.x - (startFocus.x - start.position.x) * ratio,
        y: currentFocus.y - (startFocus.y - start.position.y) * ratio,
      }, nextScale);
      return;
    }

    if (points.length === 1 && dragStartRef.current && scaleRef.current > 1) {
      commitPosition({
        x: dragStartRef.current.position.x + event.clientX - dragStartRef.current.pointer.x,
        y: dragStartRef.current.position.y + event.clientY - dragStartRef.current.pointer.y,
      });
    }
  };

  const handlePointerEnd = (event: ReactPointerEvent<HTMLDivElement>) => {
    pointersRef.current.delete(event.pointerId);
    const points = [...pointersRef.current.values()];
    pinchStartRef.current = null;

    if (points.length === 1) {
      dragStartRef.current = { pointer: points[0], position: positionRef.current };
      setDragging(scaleRef.current > 1);
    } else {
      dragStartRef.current = null;
      setDragging(false);
    }
  };

  return (
    <div
      ref={dialogRef}
      className="image-viewer-dialog"
      role="dialog"
      aria-modal="true"
      aria-label={t('chat.imageViewer.dialogLabel')}
      tabIndex={-1}
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <header className="image-viewer-toolbar">
        <div className="image-viewer-title" title={name}>{name}</div>
        <div className="image-viewer-controls" aria-label={t('chat.imageViewer.zoomControls')}>
          <button
            type="button"
            className="image-viewer-button"
            onClick={() => zoomAt(scaleRef.current - ZOOM_STEP)}
            disabled={scale <= MIN_SCALE}
            title={t('chat.imageViewer.zoomOut')}
            aria-label={t('chat.imageViewer.zoomOut')}
          >
            <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12h14" /></svg>
          </button>
          <button
            type="button"
            className="image-viewer-scale"
            onClick={resetView}
            title={t('chat.imageViewer.resetZoom')}
            aria-label={t('chat.imageViewer.resetZoom')}
          >
            {Math.round(scale * 100)}%
          </button>
          <button
            type="button"
            className="image-viewer-button"
            onClick={() => zoomAt(scaleRef.current + ZOOM_STEP)}
            disabled={scale >= MAX_SCALE}
            title={t('chat.imageViewer.zoomIn')}
            aria-label={t('chat.imageViewer.zoomIn')}
          >
            <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5v14M5 12h14" /></svg>
          </button>
        </div>
        <div className="image-viewer-actions">
          <button
            type="button"
            className="image-viewer-button"
            onClick={() => window.open(src, '_blank', 'noopener,noreferrer')}
            title={t('chat.imageViewer.openOriginal')}
            aria-label={t('chat.imageViewer.openOriginal')}
          >
            <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M14 3h7v7M10 14 21 3M21 14v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5" /></svg>
          </button>
          <button
            type="button"
            className="image-viewer-button image-viewer-close"
            onClick={onClose}
            title={t('chat.imageViewer.close')}
            aria-label={t('chat.imageViewer.close')}
          >
            <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M18 6 6 18M6 6l12 12" /></svg>
          </button>
        </div>
      </header>

      <div
        ref={stageRef}
        className={`image-viewer-stage${scale > 1 ? ' can-pan' : ''}${dragging ? ' dragging' : ''}`}
        onWheel={handleWheel}
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        onPointerUp={handlePointerEnd}
        onPointerCancel={handlePointerEnd}
        onDoubleClick={(event) => {
          if (scaleRef.current === 1) {
            zoomAt(2, { x: event.clientX, y: event.clientY });
          } else {
            resetView();
          }
        }}
      >
        {loadError ? (
          <div className="image-viewer-error" role="alert">
            {t('chat.imageViewer.loadFailed', { error: src })}
          </div>
        ) : (
          <img
            ref={imageRef}
            src={src}
            alt={alt || name}
            draggable={false}
            referrerPolicy="no-referrer"
            onError={() => setLoadError(true)}
            style={{ transform: `translate3d(${position.x}px, ${position.y}px, 0) scale(${scale})` }}
          />
        )}
      </div>

      <div className="image-viewer-hint">{t('chat.imageViewer.hint')}</div>
    </div>
  );
}
