import { useCallback, useEffect, useRef, useState } from 'react';
import type { FileAttachment } from '../types';

export interface UploadedAttachment extends FileAttachment {
  isImage: boolean;
}

export interface PendingAttachment {
  id: string;
  file: File;
  previewUrl?: string;
  uploading: boolean;
  uploaded?: UploadedAttachment;
  error?: string;
}

type UploadAttachment = (file: File) => Promise<UploadedAttachment>;

function revokePreviewUrls(attachments: PendingAttachment[]) {
  for (const attachment of attachments) {
    if (attachment.previewUrl) URL.revokeObjectURL(attachment.previewUrl);
  }
}

function attachmentID(file: File): string {
  return `${file.name}:${file.size}:${file.lastModified}:${crypto.randomUUID?.() ?? Math.random()}`;
}

export function isImageFile(file: File): boolean {
  if (['image/png', 'image/jpeg', 'image/gif', 'image/webp', 'image/bmp', 'image/avif', 'image/heic', 'image/heif'].includes(file.type.toLowerCase())) return true;
  return /\.(?:avif|bmp|gif|heic|heif|jpe?g|png|webp)$/i.test(file.name);
}

export async function uploadChatAttachment(file: File): Promise<UploadedAttachment> {
  const formData = new FormData();
  formData.append('file', file);
  const response = await fetch('/api/v1/upload-file', { method: 'POST', body: formData });
  const rawBody = typeof response.text === 'function' ? await response.text() : '';
  let data: { code?: number; msg?: string; path?: string; name?: string; mimeType?: string; size?: number } | null = null;
  if (rawBody) {
    try { data = JSON.parse(rawBody); } catch { /* preserve the raw response below */ }
  } else if (typeof response.json === 'function') {
    data = await response.json().catch(() => null);
  }
  if (response.ok === false || data?.code !== 0 || !data.path) {
    throw new Error(data?.msg || `POST /api/v1/upload-file returned HTTP ${response.status}${rawBody ? `\n${rawBody}` : ''}`);
  }
  return {
    path: data.path,
    name: data.name || file.name,
    mimeType: data.mimeType || file.type || 'application/octet-stream',
    size: typeof data.size === 'number' ? data.size : file.size,
    isImage: isImageFile(file),
  };
}

export function usePendingAttachments(uploadAttachment: UploadAttachment) {
  const [pendingAttachments, setPendingAttachments] = useState<PendingAttachment[]>([]);
  const attachmentsRef = useRef<PendingAttachment[]>([]);
  const mountedRef = useRef(true);

  useEffect(() => {
    attachmentsRef.current = pendingAttachments;
  }, [pendingAttachments]);

  const addAttachments = useCallback(async (files: FileList | File[] | null) => {
    if (!files) return;
    const selected = Array.from(files);
    if (selected.length === 0) return;

    const additions: PendingAttachment[] = selected.map((file) => ({
      id: attachmentID(file),
      file,
      previewUrl: isImageFile(file) ? URL.createObjectURL(file) : undefined,
      uploading: true,
    }));
    setPendingAttachments((previous) => {
      const next = [...previous, ...additions];
      attachmentsRef.current = next;
      return next;
    });

    for (const attachment of additions) {
      try {
        const uploaded = await uploadAttachment(attachment.file);
        if (!mountedRef.current) return;
        setPendingAttachments((previous) => {
          const next = previous.map((item) => item.id === attachment.id
            ? { ...item, uploading: false, uploaded }
            : item);
          attachmentsRef.current = next;
          return next;
        });
      } catch (error) {
        if (!mountedRef.current) return;
        setPendingAttachments((previous) => {
          const next = previous.map((item) => item.id === attachment.id
            ? { ...item, uploading: false, error: error instanceof Error ? error.message : String(error) }
            : item);
          attachmentsRef.current = next;
          return next;
        });
      }
    }
  }, [uploadAttachment]);

  const removeAttachment = useCallback((id: string) => {
    const attachment = attachmentsRef.current.find((item) => item.id === id);
    if (attachment?.previewUrl) URL.revokeObjectURL(attachment.previewUrl);
    setPendingAttachments((previous) => {
      const next = previous.filter((item) => item.id !== id);
      attachmentsRef.current = next;
      return next;
    });
  }, []);

  const clearAttachments = useCallback(() => {
    revokePreviewUrls(attachmentsRef.current);
    attachmentsRef.current = [];
    setPendingAttachments([]);
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      revokePreviewUrls(attachmentsRef.current);
      attachmentsRef.current = [];
    };
  }, []);

  return { pendingAttachments, addAttachments, removeAttachment, clearAttachments };
}
