import SwiftUI
import UniformTypeIdentifiers
import UIKit
import QuickLook

enum ChatAttachmentProcessor {
    static let maximumImageBytes = 10 * 1024 * 1024
    static let maximumFileBytes = 10 * 1024 * 1024

    @MainActor
    static func prepareFileUpload(
        data: Data,
        suggestedFilename: String,
        contentType: UTType? = nil
    ) throws -> PendingUpload {
        let type = contentType ?? inferredType(from: suggestedFilename)
        if type?.conforms(to: .image) == true, UIImage(data: data) != nil {
            return try prepareImageUpload(data: data, suggestedFilename: suggestedFilename, contentType: type)
        }
        guard !data.isEmpty else {
            throw APIError(summary: "文件为空", detail: "未读取到有效文件数据。")
        }
        guard data.count <= maximumFileBytes else {
            throw APIError(summary: "文件过大", detail: "文件超过服务端 10MB 限制，请选择更小的文件后重试。")
        }
        let filename = URL(fileURLWithPath: suggestedFilename).lastPathComponent
        return PendingUpload(
            data: data,
            filename: filename.isEmpty ? "ios-\(UUID().uuidString)" : filename,
            mimeType: type?.preferredMIMEType ?? "application/octet-stream",
            isImage: false
        )
    }

    @MainActor
    static func prepareImageUpload(
        data: Data,
        suggestedFilename: String? = nil,
        contentType: UTType? = nil
    ) throws -> PendingUpload {
        guard !data.isEmpty else {
            throw APIError(summary: "图片为空", detail: "未读取到有效图片数据。")
        }

        let baseName = sanitizedBaseName(from: suggestedFilename)
        let type = contentType ?? inferredType(from: suggestedFilename) ?? .jpeg
        if data.count <= maximumImageBytes, UIImage(data: data) != nil {
            return PendingUpload(
                data: data,
                filename: "\(baseName).\(type.preferredFilenameExtension ?? "jpg")",
                mimeType: type.preferredMIMEType ?? "image/jpeg",
                isImage: true
            )
        }

        guard let image = UIImage(data: data) else {
            throw APIError(
                summary: "图片数据无效",
                detail: "无法解析所选文件，当前仅支持可解码的图片文件。"
            )
        }
        return try prepareImageUpload(image: image, suggestedFilename: suggestedFilename)
    }

    @MainActor
    static func prepareImageUpload(image: UIImage, suggestedFilename: String? = nil) throws -> PendingUpload {
        let normalized = normalizedImage(image)
        let baseName = sanitizedBaseName(from: suggestedFilename)
        let dimensions: [CGFloat] = [2304, 1920, 1600, 1280, 960]
        let qualities: [CGFloat] = [0.9, 0.8, 0.7, 0.58, 0.46, 0.34]

        for dimension in dimensions {
            let candidate = resizedImage(normalized, maxDimension: dimension)
            for quality in qualities {
                guard let data = candidate.jpegData(compressionQuality: quality) else { continue }
                if data.count <= maximumImageBytes {
                    return PendingUpload(
                        data: data,
                        filename: "\(baseName).jpg",
                        mimeType: "image/jpeg",
                        isImage: true
                    )
                }
            }
        }

        throw APIError(
            summary: "图片过大",
            detail: "压缩后的图片仍超过服务端 10MB 限制，请选择更小的图片后重试。"
        )
    }

    private static func inferredType(from filename: String?) -> UTType? {
        guard let filename,
              let ext = filename.split(separator: ".").last,
              !ext.isEmpty else { return nil }
        return UTType(filenameExtension: String(ext))
    }

    private static func sanitizedBaseName(from filename: String?) -> String {
        let raw = filename?
            .split(separator: ".")
            .dropLast()
            .joined(separator: ".")
            .trimmingCharacters(in: .whitespacesAndNewlines)
        if let raw, !raw.isEmpty {
            return raw.replacingOccurrences(of: "/", with: "-")
        }
        return "ios-\(UUID().uuidString)"
    }

    @MainActor
    private static func normalizedImage(_ image: UIImage) -> UIImage {
        guard image.imageOrientation != .up else { return image }
        let format = UIGraphicsImageRendererFormat.default()
        format.scale = 1
        return UIGraphicsImageRenderer(size: image.size, format: format).image { _ in
            image.draw(in: CGRect(origin: .zero, size: image.size))
        }
    }

    @MainActor
    private static func resizedImage(_ image: UIImage, maxDimension: CGFloat) -> UIImage {
        let size = image.size
        let longestEdge = max(size.width, size.height)
        guard longestEdge > maxDimension else { return image }
        let scale = maxDimension / longestEdge
        let targetSize = CGSize(width: max(1, floor(size.width * scale)), height: max(1, floor(size.height * scale)))
        let format = UIGraphicsImageRendererFormat.default()
        format.scale = 1
        return UIGraphicsImageRenderer(size: targetSize, format: format).image { _ in
            image.draw(in: CGRect(origin: .zero, size: targetSize))
        }
    }
}

struct ChatAttachmentPreview: View {
    let upload: PendingUpload

    private var image: UIImage? {
        UIImage(data: upload.data)
    }

    private var sizeDescription: String {
        ByteCountFormatter.string(fromByteCount: Int64(upload.data.count), countStyle: .file)
    }

    var body: some View {
        HStack(alignment: .center, spacing: 12) {
            Group {
                if let image {
                    Image(uiImage: image)
                        .resizable()
                        .scaledToFill()
                } else {
                    Image(systemName: upload.isImage ? "photo" : "doc.fill")
                        .font(.quartet(.large))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
            }
            .frame(width: 48, height: 48)
            .clipShape(RoundedRectangle(cornerRadius: 10))
            .overlay(RoundedRectangle(cornerRadius: 10).stroke(QuartetTheme.divider, lineWidth: 1))

            VStack(alignment: .leading, spacing: 4) {
                Text(upload.filename)
                    .font(.quartet(.control, weight: .semibold))
                    .lineLimit(1)
                Text("\(upload.mimeType) · \(sizeDescription)")
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }

            Spacer(minLength: 0)
        }
        .padding(12)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 14))
        .overlay(RoundedRectangle(cornerRadius: 14).stroke(QuartetTheme.divider, lineWidth: 1))
    }
}

struct CameraImagePicker: UIViewControllerRepresentable {
    let onImagePicked: @MainActor (UIImage) -> Void
    let onCancel: @MainActor () -> Void

    func makeCoordinator() -> Coordinator {
        Coordinator(onImagePicked: onImagePicked, onCancel: onCancel)
    }

    func makeUIViewController(context: Context) -> UIImagePickerController {
        let picker = UIImagePickerController()
        picker.sourceType = .camera
        picker.cameraCaptureMode = .photo
        picker.delegate = context.coordinator
        return picker
    }

    func updateUIViewController(_ uiViewController: UIImagePickerController, context: Context) {}

    final class Coordinator: NSObject, UIImagePickerControllerDelegate, UINavigationControllerDelegate {
        private let onImagePicked: @MainActor (UIImage) -> Void
        private let onCancel: @MainActor () -> Void

        init(
            onImagePicked: @escaping @MainActor (UIImage) -> Void,
            onCancel: @escaping @MainActor () -> Void
        ) {
            self.onImagePicked = onImagePicked
            self.onCancel = onCancel
        }

        func imagePickerControllerDidCancel(_ picker: UIImagePickerController) {
            Task { @MainActor in
                onCancel()
            }
        }

        func imagePickerController(
            _ picker: UIImagePickerController,
            didFinishPickingMediaWithInfo info: [UIImagePickerController.InfoKey: Any]
        ) {
            guard let image = info[.originalImage] as? UIImage else {
                Task { @MainActor in
                    onCancel()
                }
                return
            }
            Task { @MainActor in
                onImagePicked(image)
            }
        }
    }
}

struct DocumentAttachmentPicker: UIViewControllerRepresentable {
    let onDocumentPicked: @MainActor (URL) -> Void
    let onCancel: @MainActor () -> Void

    func makeCoordinator() -> Coordinator {
        Coordinator(onDocumentPicked: onDocumentPicked, onCancel: onCancel)
    }

    func makeUIViewController(context: Context) -> UIDocumentPickerViewController {
        let picker = UIDocumentPickerViewController(
            forOpeningContentTypes: [.item],
            asCopy: true
        )
        picker.delegate = context.coordinator
        picker.allowsMultipleSelection = false
        return picker
    }

    func updateUIViewController(_ uiViewController: UIDocumentPickerViewController, context: Context) {}

    final class Coordinator: NSObject, UIDocumentPickerDelegate {
        private let onDocumentPicked: @MainActor (URL) -> Void
        private let onCancel: @MainActor () -> Void

        init(
            onDocumentPicked: @escaping @MainActor (URL) -> Void,
            onCancel: @escaping @MainActor () -> Void
        ) {
            self.onDocumentPicked = onDocumentPicked
            self.onCancel = onCancel
        }

        func documentPickerWasCancelled(_ controller: UIDocumentPickerViewController) {
            Task { @MainActor in
                onCancel()
            }
        }

        func documentPicker(_ controller: UIDocumentPickerViewController, didPickDocumentsAt urls: [URL]) {
            guard let url = urls.first else {
                Task { @MainActor in
                    onCancel()
                }
                return
            }
            Task { @MainActor in
                onDocumentPicked(url)
            }
        }
    }
}

struct LocalFilePreview: UIViewControllerRepresentable {
    let url: URL

    func makeCoordinator() -> Coordinator { Coordinator(url: url) }

    func makeUIViewController(context: Context) -> QLPreviewController {
        let controller = QLPreviewController()
        controller.dataSource = context.coordinator
        return controller
    }

    func updateUIViewController(_ uiViewController: QLPreviewController, context: Context) {
        context.coordinator.url = url
        uiViewController.reloadData()
    }

    final class Coordinator: NSObject, QLPreviewControllerDataSource {
        var url: URL

        init(url: URL) { self.url = url }

        func numberOfPreviewItems(in controller: QLPreviewController) -> Int { 1 }

        func previewController(_ controller: QLPreviewController, previewItemAt index: Int) -> QLPreviewItem {
            url as NSURL
        }
    }
}
