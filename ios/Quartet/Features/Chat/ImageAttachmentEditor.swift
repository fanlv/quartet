import SwiftUI
import UIKit

struct ImageAttachmentEditRequest: Identifiable {
    let id = UUID()
    let image: UIImage
    let suggestedFilename: String
}

@MainActor
struct ImageAttachmentEditor: View {
    private enum Stage: Equatable {
        case crop
        case markup
    }

    let request: ImageAttachmentEditRequest
    let onCancel: @MainActor () -> Void
    let onComplete: @MainActor (UIImage, String) -> Void

    private let sourceImage: UIImage

    @State private var stage: Stage
    @State private var cropRect: CGRect
    @State private var croppedImage: UIImage?
    @State private var strokes: [ImageMarkupStroke]
    @State private var redoStrokes: [ImageMarkupStroke]
    @State private var selectedColor: ImageMarkupColor
    @State private var selectedWidth: ImageMarkupWidth
    @State private var selectedTool: ImageMarkupTool

    init(
        request: ImageAttachmentEditRequest,
        onCancel: @escaping @MainActor () -> Void,
        onComplete: @escaping @MainActor (UIImage, String) -> Void
    ) {
        self.request = request
        self.onCancel = onCancel
        self.onComplete = onComplete
        sourceImage = ImageAttachmentRenderer.preparedImage(request.image)
        _stage = State(initialValue: .crop)
        _cropRect = State(initialValue: CGRect(x: 0, y: 0, width: 1, height: 1))
        _croppedImage = State(initialValue: nil)
        _strokes = State(initialValue: [])
        _redoStrokes = State(initialValue: [])
        _selectedColor = State(initialValue: .red)
        _selectedWidth = State(initialValue: .medium)
        _selectedTool = State(initialValue: .pen)
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                stageHeader

                switch stage {
                case .crop:
                    cropEditor
                case .markup:
                    markupEditor
                }
            }
            .background(QuartetTheme.canvas)
            .navigationTitle("编辑图片".localizedForApp)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("关闭".localizedForApp) { onCancel() }
                        .accessibilityHint("不添加当前图片，并停止编辑其余图片".localizedForApp)
                }
                ToolbarItem(placement: .confirmationAction) {
                    switch stage {
                    case .crop:
                        Button("下一步".localizedForApp) { advanceToMarkup() }
                            .font(.quartet(.control, weight: .semibold))
                    case .markup:
                        Button("完成".localizedForApp) { finishEditing() }
                            .font(.quartet(.control, weight: .semibold))
                    }
                }
            }
        }
        .interactiveDismissDisabled()
    }

    private var stageHeader: some View {
        HStack(spacing: 8) {
            stagePill(title: "1  裁剪", active: stage == .crop)
            Rectangle()
                .fill(QuartetTheme.divider)
                .frame(width: 28, height: 1)
                .accessibilityHidden(true)
            stagePill(title: "2  标记", active: stage == .markup)
        }
        .padding(.horizontal, 18)
        .padding(.vertical, 12)
        .background(QuartetTheme.surface)
        .accessibilityElement(children: .combine)
        .accessibilityLabel(stage == .crop
            ? "图片编辑，第 1 步，共 2 步：裁剪".localizedForApp
            : "图片编辑，第 2 步，共 2 步：标记".localizedForApp)
    }

    private func stagePill(title: String, active: Bool) -> some View {
        Text(title.localizedForApp)
            .font(.quartet(.detail, weight: .semibold))
            .foregroundStyle(active ? QuartetTheme.onAccent : QuartetTheme.secondaryText)
            .padding(.horizontal, 12)
            .frame(minHeight: 30)
            .background(active ? QuartetTheme.accent : QuartetTheme.elevated, in: Capsule())
    }

    private var cropEditor: some View {
        VStack(spacing: 14) {
            Text("拖动裁剪框或四角控制点，保留需要的画面。".localizedForApp)
                .font(.quartet(.detail))
                .foregroundStyle(QuartetTheme.secondaryText)
                .multilineTextAlignment(.center)
                .padding(.horizontal, 18)

            ImageCropCanvas(image: sourceImage, cropRect: $cropRect)
                .accessibilityLabel("调整图片裁剪区域".localizedForApp)
                .accessibilityHint("拖动裁剪框移动区域，拖动四角改变大小。".localizedForApp)

            Button {
                cropRect = CGRect(x: 0, y: 0, width: 1, height: 1)
            } label: {
                Label("重置裁剪".localizedForApp, systemImage: "arrow.counterclockwise")
                    .font(.quartet(.control, weight: .semibold))
                    .frame(maxWidth: .infinity, minHeight: 44)
            }
            .buttonStyle(.plain)
            .foregroundStyle(QuartetTheme.accent)
            .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .padding(.horizontal, 18)
            .padding(.bottom, 12)
        }
        .padding(.top, 14)
    }

    @ViewBuilder
    private var markupEditor: some View {
        if let croppedImage {
            VStack(spacing: 12) {
                Text("直接在图片上拖动画笔进行标记。".localizedForApp)
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal, 18)

                ImageMarkupCanvas(
                    image: croppedImage,
                    strokes: $strokes,
                    redoStrokes: $redoStrokes,
                    tool: selectedTool,
                    color: selectedColor,
                    width: selectedWidth
                )
                .accessibilityLabel("在图片上画线标记".localizedForApp)
                .accessibilityHint("单指拖动即可画线。".localizedForApp)

                markupToolbar
            }
            .padding(.top, 14)
        }
    }

    private var markupToolbar: some View {
        VStack(spacing: 12) {
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 10) {
                    ForEach(ImageMarkupColor.allCases) { color in
                        Button {
                            selectedTool = .pen
                            selectedColor = color
                        } label: {
                            Circle()
                                .fill(color.color)
                                .frame(width: 28, height: 28)
                                .padding(4)
                                .overlay {
                                    Circle()
                                        .stroke(
                                            selectedTool == .pen && selectedColor == color
                                                ? QuartetTheme.accent
                                                : QuartetTheme.divider,
                                            lineWidth: selectedTool == .pen && selectedColor == color ? 3 : 1
                                        )
                                }
                        }
                        .buttonStyle(.plain)
                        .frame(minWidth: 44, minHeight: 44)
                        .accessibilityLabel(AppLanguage.localizedFormat("%@画笔", color.title.localizedForApp))
                        .accessibilityAddTraits(selectedTool == .pen && selectedColor == color ? .isSelected : [])
                    }

                    Divider().frame(height: 28)

                    Button { selectedTool = .eraser } label: {
                        Label("橡皮擦".localizedForApp, systemImage: "eraser.fill")
                            .font(.quartet(.control, weight: .semibold))
                            .padding(.horizontal, 12)
                            .frame(minHeight: 40)
                            .foregroundStyle(selectedTool == .eraser ? QuartetTheme.onAccent : QuartetTheme.primaryText)
                            .background(selectedTool == .eraser ? QuartetTheme.accent : QuartetTheme.elevated, in: Capsule())
                    }
                    .buttonStyle(.plain)
                    .accessibilityAddTraits(selectedTool == .eraser ? .isSelected : [])
                }
                .padding(.horizontal, 18)
            }

            HStack(spacing: 8) {
                ForEach(ImageMarkupWidth.allCases) { width in
                    Button { selectedWidth = width } label: {
                        Text(width.title.localizedForApp)
                            .font(.quartet(.detail, weight: .semibold))
                            .foregroundStyle(selectedWidth == width ? QuartetTheme.onAccent : QuartetTheme.primaryText)
                            .frame(minWidth: 42, minHeight: 38)
                            .background(selectedWidth == width ? QuartetTheme.accent : QuartetTheme.elevated, in: Capsule())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(AppLanguage.localizedFormat("画笔粗细：%@", width.title.localizedForApp))
                    .accessibilityAddTraits(selectedWidth == width ? .isSelected : [])
                }

                Spacer(minLength: 4)

                editorIconButton(
                    title: "撤销",
                    systemImage: "arrow.uturn.backward",
                    disabled: strokes.isEmpty
                ) {
                    guard let stroke = strokes.popLast() else { return }
                    redoStrokes.append(stroke)
                }
                editorIconButton(
                    title: "重做",
                    systemImage: "arrow.uturn.forward",
                    disabled: redoStrokes.isEmpty
                ) {
                    guard let stroke = redoStrokes.popLast() else { return }
                    strokes.append(stroke)
                }
                editorIconButton(
                    title: "清除标记",
                    systemImage: "trash",
                    disabled: strokes.isEmpty
                ) {
                    redoStrokes.append(contentsOf: strokes.reversed())
                    strokes.removeAll()
                }
            }
            .padding(.horizontal, 18)

            Button {
                stage = .crop
            } label: {
                Label("返回裁剪".localizedForApp, systemImage: "crop")
                    .font(.quartet(.control, weight: .semibold))
                    .frame(maxWidth: .infinity, minHeight: 44)
            }
            .buttonStyle(.plain)
            .foregroundStyle(QuartetTheme.accent)
            .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .padding(.horizontal, 18)
        }
        .padding(.bottom, 12)
    }

    private func editorIconButton(
        title: String,
        systemImage: String,
        disabled: Bool,
        action: @escaping () -> Void
    ) -> some View {
        Button(action: action) {
            Image(systemName: systemImage)
                .font(.quartet(.control, weight: .semibold))
                .frame(width: 38, height: 38)
                .background(QuartetTheme.elevated, in: Circle())
        }
        .buttonStyle(.plain)
        .foregroundStyle(disabled ? QuartetTheme.secondaryText.opacity(0.45) : QuartetTheme.primaryText)
        .disabled(disabled)
        .accessibilityLabel(title.localizedForApp)
    }

    private func advanceToMarkup() {
        croppedImage = ImageAttachmentRenderer.crop(sourceImage, to: cropRect)
        stage = .markup
    }

    private func finishEditing() {
        guard let croppedImage else { return }
        let result = ImageAttachmentRenderer.renderMarkup(strokes, over: croppedImage)
        onComplete(result, request.suggestedFilename)
    }
}

private enum ImageMarkupTool: Equatable {
    case pen
    case eraser
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

    var id: String { rawValue }

    var title: String {
        switch self {
        case .thin: "细"
        case .medium: "中"
        case .thick: "粗"
        }
    }

    var relativeValue: CGFloat {
        switch self {
        case .thin: 0.006
        case .medium: 0.012
        case .thick: 0.024
        }
    }
}

private struct ImageMarkupStroke: Identifiable {
    let id = UUID()
    let points: [CGPoint]
    let tool: ImageMarkupTool
    let color: ImageMarkupColor
    let width: ImageMarkupWidth
}

private struct ImageCropCanvas: View {
    private enum Handle: CaseIterable, Hashable, Identifiable {
        case topLeft
        case topRight
        case bottomLeft
        case bottomRight

        var id: Self { self }

        var accessibilityLabel: String {
            switch self {
            case .topLeft: "左上裁剪控制点"
            case .topRight: "右上裁剪控制点"
            case .bottomLeft: "左下裁剪控制点"
            case .bottomRight: "右下裁剪控制点"
            }
        }
    }

    let image: UIImage
    @Binding var cropRect: CGRect

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

                ForEach(Handle.allCases) { handle in
                    cropHandle(handle, cropFrame: cropFrame, imageFrame: imageFrame)
                }
            }
            .clipShape(RoundedRectangle(cornerRadius: 16, style: .continuous))
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
        .frame(width: 44, height: 44)
        .position(point)
        .contentShape(Rectangle())
        .gesture(resizeGesture(handle, in: imageFrame))
        .accessibilityLabel(handle.accessibilityLabel.localizedForApp)
        .accessibilityHint("拖动以调整裁剪区域".localizedForApp)
    }

    private func moveGesture(in imageFrame: CGRect) -> some Gesture {
        DragGesture()
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
            .onEnded { _ in gestureStartRect = nil }
    }

    private func resizeGesture(_ handle: Handle, in imageFrame: CGRect) -> some Gesture {
        DragGesture(minimumDistance: 0)
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
            .onEnded { _ in gestureStartRect = nil }
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

    private func handlePoint(_ handle: Handle, in rect: CGRect) -> CGPoint {
        switch handle {
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
    @Binding var strokes: [ImageMarkupStroke]
    @Binding var redoStrokes: [ImageMarkupStroke]
    let tool: ImageMarkupTool
    let color: ImageMarkupColor
    let width: ImageMarkupWidth

    @State private var currentPoints: [CGPoint] = []

    var body: some View {
        GeometryReader { proxy in
            let imageFrame = aspectFitFrame(contentSize: image.size, containerSize: proxy.size)

            ZStack(alignment: .topLeading) {
                Color.black.opacity(0.92)

                Image(uiImage: image)
                    .resizable()
                    .frame(width: imageFrame.width, height: imageFrame.height)
                    .position(x: imageFrame.midX, y: imageFrame.midY)

                drawingLayer(size: imageFrame.size)
                    .frame(width: imageFrame.width, height: imageFrame.height)
                    .position(x: imageFrame.midX, y: imageFrame.midY)
                    .clipped()
                    .contentShape(Rectangle())
                    .gesture(drawingGesture(in: imageFrame.size))
            }
            .clipShape(RoundedRectangle(cornerRadius: 16, style: .continuous))
        }
        .frame(maxHeight: .infinity)
    }

    private func drawingLayer(size: CGSize) -> some View {
        Canvas { context, canvasSize in
            for stroke in strokes {
                draw(stroke, in: &context, size: canvasSize)
            }
            if !currentPoints.isEmpty {
                draw(
                    ImageMarkupStroke(points: currentPoints, tool: tool, color: color, width: width),
                    in: &context,
                    size: canvasSize
                )
            }
        }
        .frame(width: size.width, height: size.height)
    }

    private func draw(_ stroke: ImageMarkupStroke, in context: inout GraphicsContext, size: CGSize) {
        guard let first = stroke.points.first else { return }
        var path = Path()
        let firstPoint = CGPoint(x: first.x * size.width, y: first.y * size.height)
        path.move(to: firstPoint)
        if stroke.points.count == 1 {
            path.addLine(to: CGPoint(x: firstPoint.x + 0.01, y: firstPoint.y + 0.01))
        } else {
            for point in stroke.points.dropFirst() {
                path.addLine(to: CGPoint(x: point.x * size.width, y: point.y * size.height))
            }
        }

        var strokeContext = context
        if stroke.tool == .eraser {
            strokeContext.blendMode = .destinationOut
        }
        strokeContext.stroke(
            path,
            with: .color(stroke.tool == .eraser ? .white : stroke.color.color),
            style: StrokeStyle(
                lineWidth: max(2, stroke.width.relativeValue * min(size.width, size.height)),
                lineCap: .round,
                lineJoin: .round
            )
        )
    }

    private func drawingGesture(in size: CGSize) -> some Gesture {
        DragGesture(minimumDistance: 0, coordinateSpace: .local)
            .onChanged { value in
                let point = normalized(value.location, in: size)
                if let previous = currentPoints.last, distance(previous, point) < 0.002 { return }
                currentPoints.append(point)
            }
            .onEnded { value in
                let point = normalized(value.location, in: size)
                if currentPoints.isEmpty || distance(currentPoints[currentPoints.count - 1], point) >= 0.002 {
                    currentPoints.append(point)
                }
                guard !currentPoints.isEmpty else { return }
                strokes.append(ImageMarkupStroke(points: currentPoints, tool: tool, color: color, width: width))
                currentPoints = []
                redoStrokes = []
            }
    }

    private func normalized(_ point: CGPoint, in size: CGSize) -> CGPoint {
        CGPoint(
            x: min(max(0, point.x / max(size.width, 1)), 1),
            y: min(max(0, point.y / max(size.height, 1)), 1)
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
    private static let maximumEditingDimension: CGFloat = 4_096

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

    static func renderMarkup(_ strokes: [ImageMarkupStroke], over image: UIImage) -> UIImage {
        guard !strokes.isEmpty else { return image }
        let size = image.size
        let format = UIGraphicsImageRendererFormat.default()
        format.scale = 1
        format.opaque = false

        let overlay = UIGraphicsImageRenderer(size: size, format: format).image { rendererContext in
            for stroke in strokes {
                draw(stroke, in: rendererContext.cgContext, size: size)
            }
        }
        return UIGraphicsImageRenderer(size: size, format: format).image { _ in
            image.draw(in: CGRect(origin: .zero, size: size))
            overlay.draw(in: CGRect(origin: .zero, size: size))
        }
    }

    private static func draw(_ stroke: ImageMarkupStroke, in context: CGContext, size: CGSize) {
        guard let first = stroke.points.first else { return }
        context.saveGState()
        defer { context.restoreGState() }
        context.setBlendMode(stroke.tool == .eraser ? .clear : .normal)
        context.setStrokeColor(stroke.color.uiColor.cgColor)
        context.setLineWidth(max(2, stroke.width.relativeValue * min(size.width, size.height)))
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
        context.strokePath()
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
