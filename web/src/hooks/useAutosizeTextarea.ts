import { useLayoutEffect, type RefObject } from 'react';

/** Grow composer textareas with their content, up to their CSS max-height. */
export function useAutosizeTextarea(textareaRef: RefObject<HTMLTextAreaElement | null>, value: string) {
  useLayoutEffect(() => {
    const textarea = textareaRef.current;
    if (!textarea) return;

    const resize = () => {
      const scrollTop = textarea.scrollTop;
      textarea.style.height = 'auto';
      textarea.style.height = `${textarea.scrollHeight}px`;
      textarea.scrollTop = scrollTop;
    };

    resize();
    let width = textarea.clientWidth;
    const observer = new ResizeObserver(() => {
      if (textarea.clientWidth === width) return;
      width = textarea.clientWidth;
      resize();
    });
    observer.observe(textarea);
    return () => observer.disconnect();
  }, [textareaRef, value]);
}
