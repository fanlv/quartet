import { useEffect, useLayoutEffect, useRef, useState, useCallback, CSSProperties, ReactNode } from 'react';

interface VirtualListProps<T> {
  items: T[];
  itemHeight: number;
  // Pixels rendered above/below the viewport to cover fast scrolls.
  overscan?: number;
  // Optional max height for the scroll container. When unset, the component
  // fills its parent (which must have a bounded height).
  maxHeight?: number | string;
  className?: string;
  style?: CSSProperties;
  renderItem: (item: T, index: number) => ReactNode;
  // Called when the user scrolls near the bottom — used to trigger infinite
  // scroll / loadMore. Threshold is in pixels.
  onEndReached?: () => void;
  endReachedThreshold?: number;
  // Stable key extractor. Defaults to index-based which is fine when items
  // don't reorder within the list.
  getKey?: (item: T, index: number) => string | number;
  // Optional footer rendered after the spacer (e.g. "loading more…").
  footer?: ReactNode;
  // Optional empty state.
  empty?: ReactNode;
}

/**
 * Minimal fixed-height virtual list: only renders rows that overlap the
 * viewport plus an overscan band. Written in-house to avoid pulling in
 * react-window / @tanstack/react-virtual for this single use case.
 *
 * Requirements:
 *   - All items share the same height (pass `itemHeight`).
 *   - The container must have a bounded height (via `maxHeight` or CSS).
 */
export function VirtualList<T>({
  items,
  itemHeight,
  overscan = 5,
  maxHeight,
  className,
  style,
  renderItem,
  onEndReached,
  endReachedThreshold = 200,
  getKey,
  footer,
  empty,
}: VirtualListProps<T>) {
  const containerRef = useRef<HTMLDivElement>(null);
  const [scrollTop, setScrollTop] = useState(0);
  const [viewportHeight, setViewportHeight] = useState(0);
  // Track whether onEndReached has fired for the current hasMore window so we
  // don't spam it on every scroll tick.
  const endFiredRef = useRef(false);

  // Measure the container synchronously before the browser paints so the
  // first render doesn't show an under-sliced list. useLayoutEffect also
  // lets us drop the "read containerRef.current during render" fallback,
  // which the react-hooks/refs rule (rightly) forbids.
  useLayoutEffect(() => {
    const el = containerRef.current;
    if (!el) return;

    const updateViewport = () => setViewportHeight(el.clientHeight);
    updateViewport();

    const ro = typeof ResizeObserver !== 'undefined' ? new ResizeObserver(updateViewport) : null;
    ro?.observe(el);

    return () => ro?.disconnect();
  }, []);

  // When items length changes (new page loaded) allow another endReached fire.
  useEffect(() => {
    endFiredRef.current = false;
  }, [items.length]);

  const handleScroll = useCallback(
    (e: React.UIEvent<HTMLDivElement>) => {
      const target = e.currentTarget;
      setScrollTop(target.scrollTop);

      if (!onEndReached) return;
      const distanceToBottom = target.scrollHeight - target.scrollTop - target.clientHeight;
      if (distanceToBottom >= endReachedThreshold) {
        // Left the bottom zone — re-arm. If the previous trigger's load
        // failed silently (items.length never changed), scrolling back down
        // must be able to retry; otherwise auto-load is dead until refresh.
        endFiredRef.current = false;
        return;
      }
      if (!endFiredRef.current) {
        endFiredRef.current = true;
        onEndReached();
      }
    },
    [onEndReached, endReachedThreshold]
  );

  if (items.length === 0) {
    const containerStyle: CSSProperties = { overflowY: 'auto', ...style };
    if (maxHeight !== undefined) containerStyle.maxHeight = maxHeight;
    return (
      <div ref={containerRef} className={className} style={containerStyle}>
        {empty ?? null}
      </div>
    );
  }

  const totalHeight = items.length * itemHeight;
  const start = Math.max(0, Math.floor(scrollTop / itemHeight) - overscan);
  const end = Math.min(
    items.length,
    Math.ceil((scrollTop + viewportHeight) / itemHeight) + overscan
  );
  const offsetY = start * itemHeight;
  const visible = items.slice(start, end);

  const containerStyle: CSSProperties = { overflowY: 'auto', position: 'relative', ...style };
  if (maxHeight !== undefined) containerStyle.maxHeight = maxHeight;

  return (
    <div ref={containerRef} className={className} style={containerStyle} onScroll={handleScroll}>
      <div style={{ height: totalHeight, position: 'relative' }}>
        <div style={{ transform: `translateY(${offsetY}px)` }}>
          {visible.map((item, i) => {
            const absoluteIndex = start + i;
            const key = getKey ? getKey(item, absoluteIndex) : absoluteIndex;
            return (
              <div key={key} style={{ height: itemHeight }}>
                {renderItem(item, absoluteIndex)}
              </div>
            );
          })}
        </div>
      </div>
      {footer}
    </div>
  );
}
