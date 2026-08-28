import SwiftUI
import UIKit

struct ImageAttachmentEditRequest: Identifiable {
    let id = UUID()
    let image: UIImage
    let suggestedFilename: String
}

@MainActor
struct ImageAttachmentEditor: View {
    private enum Tool: Equatable {
        case pen
        case sticker
        case text
        case crop
        case mosaic
    }

    let request: ImageAttachmentEditRequest
    let onCancel: @MainActor () -> Void
    let onComplete: @MainActor (UIImage, String) -> Void

    private let sourceImage: UIImage
    private let mosaicImage: UIImage

    @State private var selectedTool: Tool?
    @State private var canvasImage: UIImage
    @State private var canvasMosaicImage: UIImage
    @State private var cropRect: CGRect
    @State private var markups: [ImageMarkupItem]
    @State private var redoMarkups: [ImageMarkupItem]
    @State private var selectedColor: ImageMarkupColor
    @State private var selectedWidth: ImageMarkupWidth
    @State private var textDraft: String
    @State private var cropPreviewImage: UIImage?
    @State private var cropUndoRects: [CGRect]
    @State private var cropRedoRects: [CGRect]
    @FocusState private var textFieldFocused: Bool

    init(
        request: ImageAttachmentEditRequest,
        onCancel: @escaping @MainActor () -> Void,
        onComplete: @escaping @MainActor (UIImage, String) -> Void
    ) {
        self.request = request
        self.onCancel = onCancel
        self.onComplete = onComplete
        let preparedImage = ImageAttachmentRenderer.preparedImage(request.image)
        sourceImage = preparedImage
        let preparedMosaicImage = ImageAttachmentRenderer.mosaic(preparedImage)
        mosaicImage = preparedMosaicImage
        _selectedTool = State(initialValue: nil)
        _canvasImage = State(initialValue: preparedImage)
        _canvasMosaicImage = State(initialValue: preparedMosaicImage)
        _cropRect = State(initialValue: CGRect(x: 0, y: 0, width: 1, height: 1))
        _markups = State(initialValue: [])
        _redoMarkups = State(initialValue: [])
        _selectedColor = State(initialValue: .red)
        _selectedWidth = State(initialValue: .medium)
        _textDraft = State(initialValue: "")
        _cropPreviewImage = State(initialValue: nil)
        _cropUndoRects = State(initialValue: [])
        _cropRedoRects = State(initialValue: [])
    }

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()

            VStack(spacing: 0) {
                topBar
                editorCanvas
                toolOptions
                bottomToolbar
            }
        }
        .preferredColorScheme(.dark)
        .interactiveDismissDisabled()
    }

    private var topBar: some View {
        HStack(spacing: 10) {
            Button("取消".localizedForApp) { onCancel() }
                .font(.quartet(.regular))
                .foregroundStyle(Color.white)
                .frame(minWidth: 52, minHeight: 48, alignment: .leading)
                .accessibilityHint("放弃这次图片编辑".localizedForApp)

            Spacer()

            editorIconButton(
                title: "撤销",
                systemImage: "arrow.uturn.backward",
                disabled: !canUndo,
                action: undo
            )
            editorIconButton(
                title: "重做",
                systemImage: "arrow.uturn.forward",
                disabled: !canRedo,
                action: redo
            )
        }
        .padding(.horizontal, 18)
        .frame(height: 58)
    }

    @ViewBuilder
    private var editorCanvas: some View {
        if selectedTool == .crop {
            ImageCropCanvas(
                image: cropPreviewImage ?? sourceImage,
                cropRect: $cropRect,
                onCropCommitted: recordCropChange
            )
                .accessibilityLabel("调整图片裁剪区域".localizedForApp)
                .accessibilityHint("拖动裁剪框移动区域，拖动四角改变大小。".localizedForApp)
        } else {
            ImageMarkupCanvas(
                image: canvasImage,
                mosaicImage: canvasMosaicImage,
                sourceImageSize: sourceImage.size,
                cropRect: cropRect,
                markups: $markups,
                redoMarkups: $redoMarkups,
                drawingTool: selectedTool == .mosaic ? .mosaic : (selectedTool == .pen ? .pen : nil),
                color: selectedColor,
                width: selectedWidth
            )
            .accessibilityLabel("编辑图片标记".localizedForApp)
            .accessibilityHint(canvasAccessibilityHint)
            .simultaneousGesture(TapGesture().onEnded { textFieldFocused = false })
        }
    }

    @ViewBuilder
    private var toolOptions: some View {
        switch selectedTool {
        case nil:
            Color.clear
                .frame(height: 58)
        case .some(.pen):
            HStack(spacing: 10) {
                colorPicker
                widthPicker
            }
            .padding(.horizontal, 16)
            .frame(height: 58)
        case .some(.mosaic):
            HStack {
                Text("马赛克粗细".localizedForApp)
                    .font(.quartet(.detail, weight: .semibold))
                    .foregroundStyle(Color.white.opacity(0.72))
                Spacer()
                widthPicker
            }
            .padding(.horizontal, 18)
            .frame(height: 58)
        case .some(.sticker):
            stickerPicker
                .frame(height: 58)
        case .some(.text):
            VStack(spacing: 2) {
                colorPicker
                    .frame(height: 42)
                textEntry
                    .frame(height: 48)
            }
            .frame(height: 90)
        case .some(.crop):
            HStack {
                Text("拖动边框调整裁剪区域".localizedForApp)
                    .font(.quartet(.detail))
                    .foregroundStyle(Color.white.opacity(0.7))
                Spacer()
                Button("还原".localizedForApp) {
                    let fullImageRect = CGRect(x: 0, y: 0, width: 1, height: 1)
                    guard cropRect != fullImageRect else { return }
                    cropUndoRects.append(cropRect)
                    cropRedoRects.removeAll()
                    cropRect = fullImageRect
                }
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(Color.white)
            }
            .padding(.horizontal, 18)
            .frame(height: 58)
        }
    }

    private var colorPicker: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 10) {
                ForEach(ImageMarkupColor.allCases) { color in
                    Button { selectedColor = color } label: {
                        Circle()
                            .fill(color.color)
                            .frame(width: 24, height: 24)
                            .padding(4)
                            .overlay {
                                Circle()
                                    .stroke(Color.white.opacity(selectedColor == color ? 0.95 : 0.22), lineWidth: selectedColor == color ? 2 : 1)
                            }
                    }
                    .buttonStyle(.plain)
                    .frame(minWidth: 40, minHeight: 44)
                    .accessibilityLabel(color.title.localizedForApp)
                    .accessibilityAddTraits(selectedColor == color ? .isSelected : [])
                }
            }
        }
    }

    private var widthPicker: some View {
        HStack(spacing: 4) {
            ForEach(ImageMarkupWidth.allCases) { width in
                Button { selectedWidth = width } label: {
                    Circle()
                        .fill(selectedWidth == width ? QuartetTheme.accent : Color.white.opacity(0.68))
                        .frame(width: width.previewDiameter, height: width.previewDiameter)
                        .frame(width: 34, height: 40)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(AppLanguage.localizedFormat("画笔粗细：%@", width.title.localizedForApp))
                .accessibilityAddTraits(selectedWidth == width ? .isSelected : [])
            }
        }
    }

    private var stickerPicker: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 4) {
                ForEach(["👍", "❤️", "😂", "😮", "✅", "❗️", "⭐️", "👀"], id: \.self) { sticker in
                    Button { addSticker(sticker) } label: {
                        Text(sticker)
                            .font(.quartet(.large))
                            .frame(width: 44, height: 44)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(AppLanguage.localizedFormat("添加贴纸 %@", sticker))
                }
            }
            .padding(.horizontal, 12)
        }
    }

    private var textEntry: some View {
        HStack(spacing: 10) {
            TextField("输入文字".localizedForApp, text: $textDraft)
                .font(.quartet(.control))
                .foregroundStyle(Color.white)
                .focused($textFieldFocused)
                .submitLabel(.done)
                .onSubmit(addTextMarkup)
                .padding(.horizontal, 12)
                .frame(height: 40)
                .background(Color.white.opacity(0.12), in: RoundedRectangle(cornerRadius: 10, style: .continuous))

            Button("添加".localizedForApp, action: addTextMarkup)
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(textDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? Color.white.opacity(0.35) : QuartetTheme.accent)
                .disabled(textDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        }
        .padding(.horizontal, 16)
    }

    private var bottomToolbar: some View {
        HStack(spacing: 2) {
            toolButton(.pen, title: "画笔", systemImage: "pencil.tip")
            toolButton(.sticker, title: "贴纸", systemImage: "face.smiling")
            toolButton(.text, title: "文字", systemImage: "character.cursor.ibeam")
            toolButton(.crop, title: "裁剪", systemImage: "crop")
            toolButton(.mosaic, title: "马赛克", systemImage: "square.grid.3x3.fill")

            Spacer(minLength: 4)

            Button("完成".localizedForApp) { finishEditing() }
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(Color.white)
                .padding(.horizontal, 14)
                .frame(minHeight: 44)
                .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 11, style: .continuous))
                .accessibilityHint("保存编辑并返回新任务".localizedForApp)
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 10)
        .background(Color.black.opacity(0.96))
    }

    private func toolButton(_ tool: Tool, title: String, systemImage: String) -> some View {
        Button {
            let wasCropping = selectedTool == .crop
            selectedTool = tool
            cropPreviewImage = tool == .crop
                ? ImageAttachmentRenderer.renderMarkup(markups, over: sourceImage, mosaicImage: mosaicImage)
                : nil
            if wasCropping, tool != .crop { refreshCanvasImages() }
            if tool == .text {
                Task { @MainActor in
                    await Task.yield()
                    textFieldFocused = true
                }
            } else {
                textFieldFocused = false
            }
        } label: {
            VStack(spacing: 4) {
                Image(systemName: systemImage)
                    .font(.quartet(.headline, weight: .medium))
                    .frame(width: 30, height: 24)
                Circle()
                    .fill(selectedTool == tool ? QuartetTheme.accent : Color.clear)
                    .frame(width: 4, height: 4)
            }
            .foregroundStyle(selectedTool == tool ? QuartetTheme.accent : Color.white)
            .frame(minWidth: 36, minHeight: 48)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(title.localizedForApp)
        .accessibilityAddTraits(selectedTool == tool ? .isSelected : [])
    }

    private func editorIconButton(
        title: String,
        systemImage: String,
        disabled: Bool,
        action: @escaping () -> Void
    ) -> some View {
        Button(action: action) {
            Image(systemName: systemImage)
                .font(.quartet(.headline, weight: .medium))
                .frame(width: 44, height: 44)
        }
        .buttonStyle(.plain)
        .foregroundStyle(disabled ? Color.white.opacity(0.26) : Color.white)
        .disabled(disabled)
        .accessibilityLabel(title.localizedForApp)
    }

    private func undo() {
        textFieldFocused = false
        if selectedTool == .crop, let previous = cropUndoRects.popLast() {
            cropRedoRects.append(cropRect)
            cropRect = previous
            return
        }
        guard let markup = markups.popLast() else { return }
        redoMarkups.append(markup)
        refreshCropPreviewIfNeeded()
    }

    private func redo() {
        textFieldFocused = false
        if selectedTool == .crop, let next = cropRedoRects.popLast() {
            cropUndoRects.append(cropRect)
            cropRect = next
            return
        }
        guard let markup = redoMarkups.popLast() else { return }
        markups.append(markup)
        refreshCropPreviewIfNeeded()
    }

    private var canUndo: Bool {
        selectedTool == .crop ? (!cropUndoRects.isEmpty || !markups.isEmpty) : !markups.isEmpty
    }

    private var canRedo: Bool {
        selectedTool == .crop ? (!cropRedoRects.isEmpty || !redoMarkups.isEmpty) : !redoMarkups.isEmpty
    }

    private func recordCropChange(from previous: CGRect) {
        guard previous != cropRect else { return }
        cropUndoRects.append(previous)
        cropRedoRects.removeAll()
    }

    private func refreshCropPreviewIfNeeded() {
        guard selectedTool == .crop else { return }
        cropPreviewImage = ImageAttachmentRenderer.renderMarkup(markups, over: sourceImage, mosaicImage: mosaicImage)
    }

    private func refreshCanvasImages() {
        canvasImage = ImageAttachmentRenderer.crop(sourceImage, to: cropRect)
        canvasMosaicImage = ImageAttachmentRenderer.crop(mosaicImage, to: cropRect)
    }

    private var canvasAccessibilityHint: String {
        switch selectedTool {
        case nil: "选择下方工具开始编辑。".localizedForApp
        case .some(.pen): "单指拖动即可画线。".localizedForApp
        case .some(.mosaic): "单指涂抹需要遮挡的区域。".localizedForApp
        case .some(.sticker), .some(.text): "拖动已添加的文字或贴纸可调整位置。".localizedForApp
        case .some(.crop): ""
        }
    }

    private func addSticker(_ sticker: String) {
        appendMarkup(.text(ImageTextMarkup(
            text: sticker,
            color: .white,
            position: nextMarkupPosition(),
            relativeFontSize: relativeFontSize(multiplier: 1.35),
            isSticker: true
        )))
    }

    private func addTextMarkup() {
        let text = textDraft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        appendMarkup(.text(ImageTextMarkup(
            text: text,
            color: selectedColor,
            position: nextMarkupPosition(),
            relativeFontSize: relativeFontSize(multiplier: 1),
            isSticker: false
        )))
        textDraft = ""
        textFieldFocused = false
    }

    private func appendMarkup(_ content: ImageMarkupItem.Content) {
        markups.append(ImageMarkupItem(content: content))
        redoMarkups.removeAll()
    }

    private func nextMarkupPosition() -> CGPoint {
        let offset = CGFloat(markups.count % 4) * 0.035
        return CGPoint(
            x: min(cropRect.maxX, cropRect.midX + offset),
            y: min(cropRect.maxY, cropRect.midY + offset)
        )
    }

    private func relativeFontSize(multiplier: CGFloat) -> CGFloat {
        let visibleMinimum = min(sourceImage.size.width * cropRect.width, sourceImage.size.height * cropRect.height)
        return 0.072 * multiplier * visibleMinimum / max(1, min(sourceImage.size.width, sourceImage.size.height))
    }

    private func finishEditing() {
        textFieldFocused = false
        let marked = ImageAttachmentRenderer.renderMarkup(markups, over: sourceImage, mosaicImage: mosaicImage)
        let result = ImageAttachmentRenderer.crop(marked, to: cropRect)
        onComplete(result, request.suggestedFilename)
    }
}

private enum ImageMarkupTool: Equatable {
    case pen
    case mosaic
}

private enum ImageMarkupColor: String, CaseIterable, Equatable, Identifiable {
    case red
    case yellow
    case green
    case blue
    case black
    case white

    var id: String { rawValue }

    var title: String {
        switch self {
        case .red: "红色"
        case .yellow: "黄色"
        case .green: "绿色"
        case .blue: "蓝色"
        case .black: "黑色"
        case .white: "白色"
        }
    }

    var color: Color { Color(uiColor: uiColor) }

    var uiColor: UIColor {
        switch self {
        case .red: UIColor(red: 0.95, green: 0.18, blue: 0.20, alpha: 1)
        case .yellow: UIColor(red: 1, green: 0.78, blue: 0.08, alpha: 1)
        case .green: UIColor(red: 0.10, green: 0.72, blue: 0.32, alpha: 1)
        case .blue: UIColor(red: 0.10, green: 0.45, blue: 0.95, alpha: 1)
        case .black: .black
        case .white: .white
        }
    }
}

private enum ImageMarkupWidth: String, CaseIterable, Equatable, Identifiable {
    case thin
    case medium
    case thick
    case extraThick

    var id: String { rawValue }

    var title: String {
        switch self {
        case .thin: "细"
        case .medium: "中"
        case .thick: "粗"
        case .extraThick: "特粗"
        }
    }

    var relativeValue: CGFloat {
        switch self {
        case .thin: 0.006
        case .medium: 0.012
        case .thick: 0.024
        case .extraThick: 0.05
        }
    }

    var previewDiameter: CGFloat {
        switch self {
        case .thin: 5
        case .medium: 9
        case .thick: 14
        case .extraThick: 22
        }
    }
}

private struct ImageMarkupStroke: Identifiable {
    let id = UUID()
    let points: [CGPoint]
    let tool: ImageMarkupTool
    let color: ImageMarkupColor
    let relativeLineWidth: CGFloat
}

private struct ImageTextMarkup {
    let text: String
    let color: ImageMarkupColor
    var position: CGPoint
    let relativeFontSize: CGFloat
    let isSticker: Bool
}

private struct ImageMarkupItem: Identifiable {
    enum Content {
        case stroke(ImageMarkupStroke)
        case text(ImageTextMarkup)
    }

    let id = UUID()
    var content: Content
}

private struct ImageCropCanvas: View {
    private static let gestureCoordinateSpace = "imageCropCanvas"
    private static let handleHitSize: CGFloat = 68
    private static let edgeHitSize: CGFloat = 44

    private enum Handle: Hashable, Identifiable {
        case top
        case left
        case right
        case bottom
        case topLeft
        case topRight
        case bottomLeft
        case bottomRight

        var id: Self { self }

        static let edges: [Self] = [.top, .left, .right, .bottom]
        static let corners: [Self] = [.topLeft, .topRight, .bottomLeft, .bottomRight]

        var accessibilityLabel: String {
            switch self {
            case .top, .left, .right, .bottom: "调整图片裁剪区域"
            case .topLeft: "左上裁剪控制点"
            case .topRight: "右上裁剪控制点"
            case .bottomLeft: "左下裁剪控制点"
            case .bottomRight: "右下裁剪控制点"
            }
        }
    }

    let image: UIImage
    @Binding var cropRect: CGRect
    let onCropCommitted: @MainActor (CGRect) -> Void

    @State private var gestureStartRect: CGRect?

    var body: some View {
        GeometryReader { proxy in
            let imageFrame = aspectFitFrame(contentSize: image.size, containerSize: proxy.size)
            let cropFrame = denormalized(cropRect, in: imageFrame)

            ZStack(alignment: .topLeading) {
                Color.black.opacity(0.92)

                Image(uiImage: image)
                    .resizable()
                    .frame(width: imageFrame.width, height: imageFrame.height)
                    .position(x: imageFrame.midX, y: imageFrame.midY)

                CropShade(cutout: cropFrame)
                    .fill(Color.black.opacity(0.55), style: FillStyle(eoFill: true))
                    .allowsHitTesting(false)

                CropGrid()
                    .stroke(Color.white.opacity(0.5), lineWidth: 0.75)
                    .frame(width: cropFrame.width, height: cropFrame.height)
                    .position(x: cropFrame.midX, y: cropFrame.midY)
                    .allowsHitTesting(false)

                Rectangle()
                    .stroke(Color.white, lineWidth: 2)
                    .frame(width: cropFrame.width, height: cropFrame.height)
                    .position(x: cropFrame.midX, y: cropFrame.midY)
                    .contentShape(Rectangle())
                    .gesture(moveGesture(in: imageFrame))

                ForEach(Handle.edges) { handle in
                    cropEdge(handle, cropFrame: cropFrame, imageFrame: imageFrame)
                }

                ForEach(Handle.corners) { handle in
                    cropHandle(handle, cropFrame: cropFrame, imageFrame: imageFrame)
                }
            }
            .coordinateSpace(name: Self.gestureCoordinateSpace)
        }
        .frame(maxHeight: .infinity)
    }

    private func cropHandle(_ handle: Handle, cropFrame: CGRect, imageFrame: CGRect) -> some View {
        let point = handlePoint(handle, in: cropFrame)
        return ZStack {
            Circle()
                .fill(Color.black.opacity(0.35))
                .frame(width: 30, height: 30)
            Circle()
                .fill(Color.white)
                .frame(width: 14, height: 14)
                .overlay(Circle().stroke(QuartetTheme.accent, lineWidth: 2))
        }
        .frame(width: Self.handleHitSize, height: Self.handleHitSize)
        .position(point)
        .contentShape(Rectangle())
        .gesture(resizeGesture(handle, in: imageFrame))
        .zIndex(1)
        .accessibilityLabel(handle.accessibilityLabel.localizedForApp)
        .accessibilityHint("拖动以调整裁剪区域".localizedForApp)
    }

    private func cropEdge(_ handle: Handle, cropFrame: CGRect, imageFrame: CGRect) -> some View {
        let vertical = handle == .left || handle == .right
        let hitSize = CGSize(
            width: vertical ? Self.edgeHitSize : max(1, cropFrame.width - Self.handleHitSize),
            height: vertical ? max(1, cropFrame.height - Self.handleHitSize) : Self.edgeHitSize
        )
        return Color.clear
            .frame(width: hitSize.width, height: hitSize.height)
            .position(edgeHitPoint(handle, in: cropFrame))
            .contentShape(Rectangle())
            .gesture(resizeGesture(handle, in: imageFrame))
            .zIndex(1)
            .accessibilityLabel(handle.accessibilityLabel.localizedForApp)
            .accessibilityHint("拖动以调整裁剪区域".localizedForApp)
    }

    private func moveGesture(in imageFrame: CGRect) -> some Gesture {
        DragGesture(minimumDistance: 0, coordinateSpace: .named(Self.gestureCoordinateSpace))
            .onChanged { value in
                let start = gestureStartRect ?? cropRect
                if gestureStartRect == nil { gestureStartRect = start }
                let dx = value.translation.width / max(imageFrame.width, 1)
                let dy = value.translation.height / max(imageFrame.height, 1)
                cropRect = CGRect(
                    x: min(max(0, start.minX + dx), 1 - start.width),
                    y: min(max(0, start.minY + dy), 1 - start.height),
                    width: start.width,
                    height: start.height
                )
            }
            .onEnded { _ in
                if let start = gestureStartRect { onCropCommitted(start) }
                gestureStartRect = nil
            }
    }

    private func resizeGesture(_ handle: Handle, in imageFrame: CGRect) -> some Gesture {
        DragGesture(minimumDistance: 0, coordinateSpace: .named(Self.gestureCoordinateSpace))
            .onChanged { value in
                let start = gestureStartRect ?? cropRect
                if gestureStartRect == nil { gestureStartRect = start }
                let dx = value.translation.width / max(imageFrame.width, 1)
                let dy = value.translation.height / max(imageFrame.height, 1)
                let minimumWidth = min(0.9, 72 / max(imageFrame.width, 1))
                let minimumHeight = min(0.9, 72 / max(imageFrame.height, 1))
                cropRect = resizedRect(
                    start,
                    handle: handle,
                    dx: dx,
                    dy: dy,
                    minimumWidth: minimumWidth,
                    minimumHeight: minimumHeight
                )
            }
            .onEnded { _ in
                if let start = gestureStartRect { onCropCommitted(start) }
                gestureStartRect = nil
            }
    }

    private func resizedRect(
        _ start: CGRect,
        handle: Handle,
        dx: CGFloat,
        dy: CGFloat,
        minimumWidth: CGFloat,
        minimumHeight: CGFloat
    ) -> CGRect {
        var left = start.minX
        var right = start.maxX
        var top = start.minY
        var bottom = start.maxY

        switch handle {
        case .top:
            top = min(max(0, start.minY + dy), start.maxY - minimumHeight)
        case .left:
            left = min(max(0, start.minX + dx), start.maxX - minimumWidth)
        case .right:
            right = max(min(1, start.maxX + dx), start.minX + minimumWidth)
        case .bottom:
            bottom = max(min(1, start.maxY + dy), start.minY + minimumHeight)
        case .topLeft:
            left = min(max(0, start.minX + dx), start.maxX - minimumWidth)
            top = min(max(0, start.minY + dy), start.maxY - minimumHeight)
        case .topRight:
            right = max(min(1, start.maxX + dx), start.minX + minimumWidth)
            top = min(max(0, start.minY + dy), start.maxY - minimumHeight)
        case .bottomLeft:
            left = min(max(0, start.minX + dx), start.maxX - minimumWidth)
            bottom = max(min(1, start.maxY + dy), start.minY + minimumHeight)
        case .bottomRight:
            right = max(min(1, start.maxX + dx), start.minX + minimumWidth)
            bottom = max(min(1, start.maxY + dy), start.minY + minimumHeight)
        }

        return CGRect(x: left, y: top, width: right - left, height: bottom - top)
    }

    private func edgeHitPoint(_ handle: Handle, in rect: CGRect) -> CGPoint {
        switch handle {
        case .top: CGPoint(x: rect.midX, y: rect.minY + Self.edgeHitSize / 2)
        case .left: CGPoint(x: rect.minX + Self.edgeHitSize / 2, y: rect.midY)
        case .right: CGPoint(x: rect.maxX - Self.edgeHitSize / 2, y: rect.midY)
        case .bottom: CGPoint(x: rect.midX, y: rect.maxY - Self.edgeHitSize / 2)
        case .topLeft, .topRight, .bottomLeft, .bottomRight:
            handlePoint(handle, in: rect)
        }
    }

    private func handlePoint(_ handle: Handle, in rect: CGRect) -> CGPoint {
        switch handle {
        case .top: CGPoint(x: rect.midX, y: rect.minY)
        case .left: CGPoint(x: rect.minX, y: rect.midY)
        case .right: CGPoint(x: rect.maxX, y: rect.midY)
        case .bottom: CGPoint(x: rect.midX, y: rect.maxY)
        case .topLeft: CGPoint(x: rect.minX, y: rect.minY)
        case .topRight: CGPoint(x: rect.maxX, y: rect.minY)
        case .bottomLeft: CGPoint(x: rect.minX, y: rect.maxY)
        case .bottomRight: CGPoint(x: rect.maxX, y: rect.maxY)
        }
    }

    private func denormalized(_ rect: CGRect, in frame: CGRect) -> CGRect {
        CGRect(
            x: frame.minX + rect.minX * frame.width,
            y: frame.minY + rect.minY * frame.height,
            width: rect.width * frame.width,
            height: rect.height * frame.height
        )
    }
}

private struct CropShade: Shape {
    let cutout: CGRect

    func path(in rect: CGRect) -> Path {
        var path = Path()
        path.addRect(rect)
        path.addRect(cutout)
        return path
    }
}

private struct CropGrid: Shape {
    func path(in rect: CGRect) -> Path {
        var path = Path()
        for fraction in [CGFloat(1) / 3, CGFloat(2) / 3] {
            path.move(to: CGPoint(x: rect.width * fraction, y: 0))
            path.addLine(to: CGPoint(x: rect.width * fraction, y: rect.height))
            path.move(to: CGPoint(x: 0, y: rect.height * fraction))
            path.addLine(to: CGPoint(x: rect.width, y: rect.height * fraction))
        }
        return path
    }
}

private struct ImageMarkupCanvas: View {
    let image: UIImage
    let mosaicImage: UIImage
    let sourceImageSize: CGSize
    let cropRect: CGRect
    @Binding var markups: [ImageMarkupItem]
    @Binding var redoMarkups: [ImageMarkupItem]
    let drawingTool: ImageMarkupTool?
    let color: ImageMarkupColor
    let width: ImageMarkupWidth

    @State private var currentPoints: [CGPoint] = []
    @State private var dragStartPositions: [UUID: CGPoint] = [:]

    var body: some View {
        GeometryReader { proxy in
            let imageFrame = aspectFitFrame(contentSize: image.size, containerSize: proxy.size)

            ZStack(alignment: .topLeading) {
                Color.black

                ZStack(alignment: .topLeading) {
                    Image(uiImage: image)
                        .resizable()
                        .frame(width: imageFrame.width, height: imageFrame.height)

                    drawingLayer(size: imageFrame.size, mosaicImage: mosaicImage)

                    ForEach(textMarkups) { markup in
                        textOverlay(markup, imageSize: imageFrame.size)
                    }

                    if drawingTool != nil {
                        Rectangle()
                            .fill(Color.clear)
                            .contentShape(Rectangle())
                            .frame(width: imageFrame.width, height: imageFrame.height)
                            .gesture(drawingGesture(in: imageFrame.size))
                    }
                }
                    .frame(width: imageFrame.width, height: imageFrame.height)
                    .position(x: imageFrame.midX, y: imageFrame.midY)
                    .clipped()
            }
        }
        .frame(maxHeight: .infinity)
    }

    private var textMarkups: [ImageMarkupItem] {
        markups.filter { markup in
            if case .text = markup.content { return true }
            return false
        }
    }

    private func drawingLayer(size: CGSize, mosaicImage: UIImage) -> some View {
        Canvas { context, canvasSize in
            for markup in markups {
                guard case .stroke(let stroke) = markup.content else { continue }
                draw(stroke, in: &context, size: canvasSize, mosaicImage: mosaicImage)
            }
            if let drawingTool, !currentPoints.isEmpty {
                draw(
                    ImageMarkupStroke(
                        points: currentPoints,
                        tool: drawingTool,
                        color: color,
                        relativeLineWidth: currentRelativeLineWidth
                    ),
                    in: &context,
                    size: canvasSize,
                    mosaicImage: mosaicImage
                )
            }
        }
        .frame(width: size.width, height: size.height)
    }

    private func draw(
        _ stroke: ImageMarkupStroke,
        in context: inout GraphicsContext,
        size: CGSize,
        mosaicImage: UIImage
    ) {
        guard let first = stroke.points.first else { return }
        var path = Path()
        let firstPoint = canvasPoint(first, size: size)
        path.move(to: firstPoint)
        if stroke.points.count == 1 {
            path.addLine(to: CGPoint(x: firstPoint.x + 0.01, y: firstPoint.y + 0.01))
        } else {
            for point in stroke.points.dropFirst() {
                path.addLine(to: canvasPoint(point, size: size))
            }
        }

        let lineWidth = max(2, stroke.relativeLineWidth * sourceMinimumDimension * displayScale(in: size))
        let style = StrokeStyle(lineWidth: lineWidth, lineCap: .round, lineJoin: .round)
        switch stroke.tool {
        case .pen:
            context.stroke(path, with: .color(stroke.color.color), style: style)
        case .mosaic:
            var mosaicContext = context
            mosaicContext.clip(to: path.strokedPath(style))
            mosaicContext.draw(
                Image(uiImage: mosaicImage),
                in: CGRect(origin: .zero, size: size)
            )
        }
    }

    private func drawingGesture(in size: CGSize) -> some Gesture {
        DragGesture(minimumDistance: 0, coordinateSpace: .local)
            .onChanged { value in
                guard drawingTool != nil else { return }
                let point = sourcePoint(value.location, in: size)
                if let previous = currentPoints.last, distance(previous, point) < 0.002 { return }
                currentPoints.append(point)
            }
            .onEnded { value in
                guard let drawingTool else {
                    currentPoints = []
                    return
                }
                let point = sourcePoint(value.location, in: size)
                if currentPoints.isEmpty || distance(currentPoints[currentPoints.count - 1], point) >= 0.002 {
                    currentPoints.append(point)
                }
                guard !currentPoints.isEmpty else { return }
                markups.append(ImageMarkupItem(content: .stroke(ImageMarkupStroke(
                    points: currentPoints,
                    tool: drawingTool,
                    color: color,
                    relativeLineWidth: currentRelativeLineWidth
                ))))
                currentPoints = []
                redoMarkups = []
            }
    }

    private func textOverlay(_ markup: ImageMarkupItem, imageSize: CGSize) -> some View {
        guard case .text(let textMarkup) = markup.content else { return AnyView(EmptyView()) }
        let localPoint = canvasPoint(textMarkup.position, size: imageSize)
        let fontSize = max(18, textMarkup.relativeFontSize * sourceMinimumDimension * displayScale(in: imageSize))
        return AnyView(
            Text(textMarkup.text)
                .font(.system(size: fontSize, weight: textMarkup.isSticker ? .regular : .bold))
                .foregroundStyle(textMarkup.color.color)
                .shadow(color: Color.black.opacity(textMarkup.isSticker ? 0 : 0.75), radius: 1, x: 0, y: 1)
                .padding(10)
                .position(localPoint)
                .gesture(textDragGesture(markupID: markup.id, imageSize: imageSize))
                .accessibilityLabel(textMarkup.isSticker
                    ? AppLanguage.localizedFormat("贴纸 %@", textMarkup.text)
                    : AppLanguage.localizedFormat("文字 %@", textMarkup.text))
                .accessibilityHint("拖动可调整位置".localizedForApp)
        )
    }

    private func textDragGesture(markupID: UUID, imageSize: CGSize) -> some Gesture {
        DragGesture()
            .onChanged { value in
                guard let index = markups.firstIndex(where: { $0.id == markupID }),
                      case .text(var textMarkup) = markups[index].content else { return }
                let start = dragStartPositions[markupID] ?? textMarkup.position
                if dragStartPositions[markupID] == nil { dragStartPositions[markupID] = start }
                textMarkup.position = CGPoint(
                    x: min(max(cropRect.minX, start.x + value.translation.width / max(imageSize.width, 1) * cropRect.width), cropRect.maxX),
                    y: min(max(cropRect.minY, start.y + value.translation.height / max(imageSize.height, 1) * cropRect.height), cropRect.maxY)
                )
                markups[index].content = .text(textMarkup)
            }
            .onEnded { _ in
                dragStartPositions[markupID] = nil
                redoMarkups.removeAll()
            }
    }

    private var visibleMinimumScale: CGFloat {
        max(0.001, min(
            sourceImageSize.width * cropRect.width,
            sourceImageSize.height * cropRect.height
        ) / sourceMinimumDimension)
    }

    private var currentRelativeLineWidth: CGFloat {
        width.relativeValue * visibleMinimumScale
    }

    private var sourceMinimumDimension: CGFloat {
        max(1, min(sourceImageSize.width, sourceImageSize.height))
    }

    private func displayScale(in size: CGSize) -> CGFloat {
        min(size.width / max(1, image.size.width), size.height / max(1, image.size.height))
    }

    private func sourcePoint(_ point: CGPoint, in size: CGSize) -> CGPoint {
        CGPoint(
            x: cropRect.minX + min(max(0, point.x / max(size.width, 1)), 1) * cropRect.width,
            y: cropRect.minY + min(max(0, point.y / max(size.height, 1)), 1) * cropRect.height
        )
    }

    private func canvasPoint(_ point: CGPoint, size: CGSize) -> CGPoint {
        CGPoint(
            x: (point.x - cropRect.minX) / max(cropRect.width, 0.001) * size.width,
            y: (point.y - cropRect.minY) / max(cropRect.height, 0.001) * size.height
        )
    }

    private func distance(_ lhs: CGPoint, _ rhs: CGPoint) -> CGFloat {
        let dx = lhs.x - rhs.x
        let dy = lhs.y - rhs.y
        return sqrt(dx * dx + dy * dy)
    }
}

@MainActor
private enum ImageAttachmentRenderer {
    // 上传链路最终最多保留 2304px；编辑阶段同步收敛尺寸，避免原图、马赛克图和预览图
    // 同时常驻时占用过多内存。
    private static let maximumEditingDimension: CGFloat = 2_304

    static func preparedImage(_ image: UIImage) -> UIImage {
        guard let cgImage = image.cgImage else { return image }
        let swapsAxes: Bool
        switch image.imageOrientation {
        case .left, .leftMirrored, .right, .rightMirrored:
            swapsAxes = true
        default:
            swapsAxes = false
        }

        let orientedSize = swapsAxes
            ? CGSize(width: CGFloat(cgImage.height), height: CGFloat(cgImage.width))
            : CGSize(width: CGFloat(cgImage.width), height: CGFloat(cgImage.height))
        let longestEdge = max(orientedSize.width, orientedSize.height)
        let scale = longestEdge > maximumEditingDimension ? maximumEditingDimension / longestEdge : 1
        let targetSize = CGSize(
            width: max(1, floor(orientedSize.width * scale)),
            height: max(1, floor(orientedSize.height * scale))
        )
        let format = UIGraphicsImageRendererFormat.default()
        format.scale = 1
        format.opaque = false
        return UIGraphicsImageRenderer(size: targetSize, format: format).image { _ in
            image.draw(in: CGRect(origin: .zero, size: targetSize))
        }
    }

    static func crop(_ image: UIImage, to normalizedRect: CGRect) -> UIImage {
        guard let cgImage = image.cgImage else { return image }
        let bounds = CGRect(
            x: 0,
            y: 0,
            width: CGFloat(cgImage.width),
            height: CGFloat(cgImage.height)
        )
        let crop = CGRect(
            x: normalizedRect.minX * bounds.width,
            y: normalizedRect.minY * bounds.height,
            width: normalizedRect.width * bounds.width,
            height: normalizedRect.height * bounds.height
        )
        .integral
        .intersection(bounds)
        guard crop.width >= 1, crop.height >= 1, let cropped = cgImage.cropping(to: crop) else {
            return image
        }
        return UIImage(cgImage: cropped, scale: 1, orientation: .up)
    }

    static func mosaic(_ image: UIImage) -> UIImage {
        let pixelSize: CGFloat = 18
        let lowResolutionSize = CGSize(
            width: max(1, ceil(image.size.width / pixelSize)),
            height: max(1, ceil(image.size.height / pixelSize))
        )
        let lowResolutionFormat = UIGraphicsImageRendererFormat.default()
        lowResolutionFormat.scale = 1
        lowResolutionFormat.opaque = false
        let lowResolution = UIGraphicsImageRenderer(size: lowResolutionSize, format: lowResolutionFormat).image { context in
            context.cgContext.interpolationQuality = .low
            image.draw(in: CGRect(origin: .zero, size: lowResolutionSize))
        }
        let fullResolutionFormat = UIGraphicsImageRendererFormat.default()
        fullResolutionFormat.scale = 1
        fullResolutionFormat.opaque = false
        return UIGraphicsImageRenderer(size: image.size, format: fullResolutionFormat).image { context in
            context.cgContext.interpolationQuality = .none
            lowResolution.draw(in: CGRect(origin: .zero, size: image.size))
        }
    }

    static func renderMarkup(
        _ markups: [ImageMarkupItem],
        over image: UIImage,
        mosaicImage: UIImage
    ) -> UIImage {
        guard !markups.isEmpty else { return image }
        let size = image.size
        let format = UIGraphicsImageRendererFormat.default()
        format.scale = 1
        format.opaque = false
        return UIGraphicsImageRenderer(size: size, format: format).image { rendererContext in
            image.draw(in: CGRect(origin: .zero, size: size))
            for markup in markups {
                guard case .stroke(let stroke) = markup.content else { continue }
                draw(stroke, in: rendererContext.cgContext, size: size, mosaicImage: mosaicImage)
            }
            // 文本和贴纸始终处于画笔与马赛克之上，与编辑画布的层级保持一致。
            for markup in markups {
                guard case .text(let textMarkup) = markup.content else { continue }
                draw(textMarkup, in: rendererContext.cgContext, size: size)
            }
        }
    }

    private static func draw(
        _ stroke: ImageMarkupStroke,
        in context: CGContext,
        size: CGSize,
        mosaicImage: UIImage
    ) {
        guard let first = stroke.points.first else { return }
        context.saveGState()
        defer { context.restoreGState() }
        context.setLineWidth(max(2, stroke.relativeLineWidth * min(size.width, size.height)))
        context.setLineCap(.round)
        context.setLineJoin(.round)
        context.beginPath()
        let firstPoint = CGPoint(x: first.x * size.width, y: first.y * size.height)
        context.move(to: firstPoint)
        if stroke.points.count == 1 {
            context.addLine(to: CGPoint(x: firstPoint.x + 0.01, y: firstPoint.y + 0.01))
        } else {
            for point in stroke.points.dropFirst() {
                context.addLine(to: CGPoint(x: point.x * size.width, y: point.y * size.height))
            }
        }
        switch stroke.tool {
        case .pen:
            context.setStrokeColor(stroke.color.uiColor.cgColor)
            context.strokePath()
        case .mosaic:
            context.replacePathWithStrokedPath()
            context.clip()
            mosaicImage.draw(in: CGRect(origin: .zero, size: size))
        }
    }

    private static func draw(_ markup: ImageTextMarkup, in context: CGContext, size: CGSize) {
        context.saveGState()
        defer { context.restoreGState() }
        UIGraphicsPushContext(context)
        defer { UIGraphicsPopContext() }

        let fontSize = max(24, markup.relativeFontSize * min(size.width, size.height))
        let font = UIFont.systemFont(ofSize: fontSize, weight: markup.isSticker ? .regular : .bold)
        let shadow = NSShadow()
        shadow.shadowColor = markup.isSticker ? UIColor.clear : UIColor.black.withAlphaComponent(0.72)
        shadow.shadowOffset = CGSize(width: 0, height: max(1, fontSize * 0.035))
        shadow.shadowBlurRadius = max(1, fontSize * 0.025)
        let attributes: [NSAttributedString.Key: Any] = [
            .font: font,
            .foregroundColor: markup.color.uiColor,
            .shadow: shadow
        ]
        let attributed = NSAttributedString(string: markup.text, attributes: attributes)
        let measured = attributed.boundingRect(
            with: CGSize(width: size.width * 0.84, height: size.height),
            options: [.usesLineFragmentOrigin, .usesFontLeading],
            context: nil
        ).integral
        let origin = CGPoint(
            x: min(max(0, markup.position.x * size.width - measured.width / 2), max(0, size.width - measured.width)),
            y: min(max(0, markup.position.y * size.height - measured.height / 2), max(0, size.height - measured.height))
        )
        attributed.draw(
            with: CGRect(origin: origin, size: measured.size),
            options: [.usesLineFragmentOrigin, .usesFontLeading],
            context: nil
        )
    }
}

private func aspectFitFrame(contentSize: CGSize, containerSize: CGSize) -> CGRect {
    guard contentSize.width > 0, contentSize.height > 0, containerSize.width > 0, containerSize.height > 0 else {
        return .zero
    }
    let scale = min(containerSize.width / contentSize.width, containerSize.height / contentSize.height)
    let fitted = CGSize(width: contentSize.width * scale, height: contentSize.height * scale)
    return CGRect(
        x: (containerSize.width - fitted.width) / 2,
        y: (containerSize.height - fitted.height) / 2,
        width: fitted.width,
        height: fitted.height
    )
}
