import { useCallback, useEffect, useRef, useState } from 'react';

export interface PendingImage {
  file: File;
  previewUrl: string;
  uploading: boolean;
  uploadedPath?: string;
  error?: string;
}

type UploadImage = (file: File) => Promise<string>;

function revokePreviewUrls(images: PendingImage[]) {
  for (const image of images) {
    URL.revokeObjectURL(image.previewUrl);
  }
}

export function usePendingImages(uploadImage: UploadImage) {
  const [pendingImages, setPendingImages] = useState<PendingImage[]>([]);
  const pendingImagesRef = useRef<PendingImage[]>([]);
  const mountedRef = useRef(true);

  useEffect(() => {
    pendingImagesRef.current = pendingImages;
  }, [pendingImages]);

  const addImages = useCallback(async (files: FileList | File[] | null) => {
    if (!files) return;
    const imageFiles = Array.from(files).filter((file) => file.type.startsWith('image/'));
    if (imageFiles.length === 0) return;

    const newImages: PendingImage[] = imageFiles.map((file) => ({
      file,
      previewUrl: URL.createObjectURL(file),
      uploading: true,
    }));

    setPendingImages((prev) => {
      const next = [...prev, ...newImages];
      pendingImagesRef.current = next;
      return next;
    });

    for (const image of newImages) {
      try {
        const path = await uploadImage(image.file);
        if (!mountedRef.current) return;
        setPendingImages((prev) => {
          const next = prev.map((pending) =>
            pending.previewUrl === image.previewUrl
              ? { ...pending, uploading: false, uploadedPath: path }
              : pending
          );
          pendingImagesRef.current = next;
          return next;
        });
      } catch (err) {
        if (!mountedRef.current) return;
        setPendingImages((prev) => {
          const next = prev.map((pending) =>
            pending.previewUrl === image.previewUrl
              ? { ...pending, uploading: false, error: err instanceof Error ? err.message : 'Upload failed' }
              : pending
          );
          pendingImagesRef.current = next;
          return next;
        });
      }
    }
  }, [uploadImage]);

  const removeImage = useCallback((previewUrl: string) => {
    const image = pendingImagesRef.current.find((pending) => pending.previewUrl === previewUrl);
    if (image) URL.revokeObjectURL(image.previewUrl);
    setPendingImages((prev) => {
      const next = prev.filter((pending) => pending.previewUrl !== previewUrl);
      pendingImagesRef.current = next;
      return next;
    });
  }, []);

  const clearImages = useCallback(() => {
    const images = pendingImagesRef.current;
    if (images.length === 0) return;
    revokePreviewUrls(images);
    pendingImagesRef.current = [];
    setPendingImages([]);
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      revokePreviewUrls(pendingImagesRef.current);
      pendingImagesRef.current = [];
    };
  }, []);

  return {
    pendingImages,
    addImages,
    removeImage,
    clearImages,
  };
}
