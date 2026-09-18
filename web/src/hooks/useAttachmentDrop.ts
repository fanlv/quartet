import { useCallback, useEffect, useRef, useState, type DragEventHandler } from 'react';

type AddAttachments = (files: FileList) => void | Promise<void>;

function isFileDrag(types: readonly string[]): boolean {
  return Array.from(types).includes('Files');
}

export function useAttachmentDrop(addAttachments: AddAttachments, disabled = false) {
  const [isDraggingAttachment, setIsDraggingAttachment] = useState(false);
  const dragDepthRef = useRef(0);

  const resetDragState = useCallback(() => {
    dragDepthRef.current = 0;
    setIsDraggingAttachment(false);
  }, []);

  useEffect(() => {
    if (disabled) resetDragState();
  }, [disabled, resetDragState]);

  const onDragEnter = useCallback<DragEventHandler<HTMLElement>>((event) => {
    if (!isFileDrag(event.dataTransfer.types)) return;
    event.preventDefault();
    event.stopPropagation();
    dragDepthRef.current += 1;
    if (!disabled) setIsDraggingAttachment(true);
  }, [disabled]);

  const onDragOver = useCallback<DragEventHandler<HTMLElement>>((event) => {
    if (!isFileDrag(event.dataTransfer.types)) return;
    event.preventDefault();
    event.stopPropagation();
    event.dataTransfer.dropEffect = disabled ? 'none' : 'copy';
  }, [disabled]);

  const onDragLeave = useCallback<DragEventHandler<HTMLElement>>((event) => {
    event.preventDefault();
    event.stopPropagation();
    dragDepthRef.current = Math.max(0, dragDepthRef.current - 1);
    if (dragDepthRef.current === 0) setIsDraggingAttachment(false);
  }, []);

  const onDrop = useCallback<DragEventHandler<HTMLElement>>((event) => {
    if (!isFileDrag(event.dataTransfer.types)) return;
    event.preventDefault();
    event.stopPropagation();
    resetDragState();
    if (!disabled && event.dataTransfer.files.length > 0) {
      void addAttachments(event.dataTransfer.files);
    }
  }, [addAttachments, disabled, resetDragState]);

  return {
    isDraggingAttachment,
    attachmentDropHandlers: { onDragEnter, onDragOver, onDragLeave, onDrop },
  };
}
