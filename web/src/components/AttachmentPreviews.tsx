import type { FileAttachment } from '../types';
import type { PendingAttachment } from '../hooks/usePendingAttachments';

function fileExtension(name: string): string {
  const extension = name.split('.').pop();
  return extension && extension !== name ? extension.slice(0, 5).toUpperCase() : 'FILE';
}

function PendingFileCard({ attachment }: { attachment: PendingAttachment }) {
  return (
    <>
      <span className="chat-file-preview-icon" aria-hidden="true">{fileExtension(attachment.file.name)}</span>
      <span className="chat-file-preview-info">
        <span className="chat-file-preview-name">{attachment.file.name}</span>
        <span className="chat-file-preview-size">{formatFileSize(attachment.file.size)}</span>
      </span>
    </>
  );
}

function formatFileSize(size?: number): string {
  if (!size || size < 1) return '';
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}

export function PendingAttachmentPreviews({
  attachments,
  onRemove,
}: {
  attachments: PendingAttachment[];
  onRemove: (id: string) => void;
}) {
  if (attachments.length === 0) return null;
  return (
    <div className="chat-attachment-preview-row">
      {attachments.map((attachment) => (
        <div
          key={attachment.id}
          className={`chat-attachment-preview-item ${attachment.previewUrl ? 'image' : 'file'} ${attachment.error ? 'error' : ''}`}
        >
          {attachment.previewUrl
            ? <img src={attachment.previewUrl} alt={attachment.file.name} className="chat-image-preview-thumb" />
            : <PendingFileCard attachment={attachment} />}
          {attachment.uploading && <div className="chat-image-preview-loading" />}
          {attachment.error && <span className="chat-image-preview-error" title={attachment.error}>!</span>}
          <button
            type="button"
            className="chat-image-preview-remove"
            onClick={() => onRemove(attachment.id)}
            aria-label={`Remove ${attachment.file.name}`}
          >×</button>
        </div>
      ))}
    </div>
  );
}

export function UploadedFilePreviews({
  attachments,
  onRemove,
}: {
  attachments: FileAttachment[];
  onRemove: (path: string) => void;
}) {
  if (attachments.length === 0) return null;
  return (
    <div className="chat-attachment-preview-row">
      {attachments.map((attachment) => (
        <div key={attachment.path} className="chat-attachment-preview-item file">
          <span className="chat-file-preview-icon" aria-hidden="true">{fileExtension(attachment.name)}</span>
          <span className="chat-file-preview-info">
            <span className="chat-file-preview-name">{attachment.name}</span>
            <span className="chat-file-preview-size">{formatFileSize(attachment.size)}</span>
          </span>
          <button type="button" className="chat-image-preview-remove" onClick={() => onRemove(attachment.path)}>×</button>
        </div>
      ))}
    </div>
  );
}
