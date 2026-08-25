import SwiftUI
import UIKit

/// 消息图片的统一入口。时间线只加载 280pt 缩略图；进入查看器后再按原尺寸解码，
/// 避免普通滚动为了少数会被打开的图片长期占用大块位图内存。
struct AuthenticatedMessageImage: View {
    enum Typography {
        case chat
        case app

        var detailFont: Font {
            switch self {
            case .chat: .chat(.detail)
            case .app: .quartet(.detail)
            }
        }
    }

    @EnvironmentObject private var appModel: AppModel

    let path: String
    var typography: Typography = .chat

    @State private var image: UIImage?
    @State private var error: String?
    @State private var presentsViewer = false

    var body: some View {
        Group {
            if let image {
                Button {
                    presentsViewer = true
                } label: {
                    Image(uiImage: image)
                        .resizable()
                        .scaledToFit()
                        .frame(maxHeight: 280)
                        .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
                        .overlay {
                            RoundedRectangle(cornerRadius: 12, style: .continuous)
                                .stroke(Color.white.opacity(0.12), lineWidth: 1)
                        }
                        .overlay(alignment: .bottomTrailing) {
                            Image(systemName: "arrow.up.left.and.arrow.down.right")
                                .font(.chat(.compact, weight: .bold))
                                .foregroundStyle(.white)
                                .frame(width: 30, height: 30)
                                .background(Color.black.opacity(0.5), in: Circle())
                                .padding(8)
                                .accessibilityHidden(true)
                        }
                        .contentShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
                }
                .buttonStyle(.plain)
                .accessibilityLabel("查看图片".localizedForApp)
                .accessibilityHint("打开支持缩放的图片查看器".localizedForApp)
                .fullScreenCover(isPresented: $presentsViewer) {
                    MessageImageViewer(path: path, thumbnail: image)
                }
            } else if let error {
                Button {
                    appModel.present(APIError(summary: "图片加载失败", detail: error))
                } label: {
                    Label("图片加载失败，查看详情".localizedForApp, systemImage: "photo.badge.exclamationmark")
                        .font(typography.detailFont)
                        .foregroundStyle(QuartetTheme.failed)
                }
                .buttonStyle(.plain)
            } else {
                ProgressView()
                    .frame(maxWidth: .infinity)
                    .frame(height: 80)
                    .accessibilityLabel("正在加载图片".localizedForApp)
            }
        }
        .task(id: path) {
            image = nil
            error = nil
            do {
                let client = try appModel.apiClient()
                image = try await ChatImageLoader.shared.image(
                    path: path,
                    namespace: appModel.serverAddress,
                    maxPixelSize: 280
                ) {
                    try await client.fileData(path: path)
                }
            } catch let apiError as APIError {
                error = apiError.detail
            } catch {
                self.error = String(describing: error)
            }
        }
    }
}

/// 全屏图片查看器。手势状态与已提交状态分开保存，缩放过程连续，手势结束后再
/// 把比例和位移收敛到合法范围，避免图片被拖到再也找不回来的位置。
private struct MessageImageViewer: View {
    @EnvironmentObject private var appModel: AppModel
    @Environment(\.dismiss) private var dismiss
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    let path: String

    @State private var displayedImage: UIImage
    @State private var scale: CGFloat = 1
    @State private var offset: CGSize = .zero
    @State private var chromeVisible = true
    @State private var loadingOriginal = true
    @State private var loadError: PresentedError?
    @State private var presentsErrorDetails = false
    @GestureState private var gestureMagnification: CGFloat = 1
    @GestureState private var gestureTranslation: CGSize = .zero

    private let minimumScale: CGFloat = 0.25
    private let maximumScale: CGFloat = 5
    private let zoomStep: CGFloat = 0.25

    init(path: String, thumbnail: UIImage) {
        self.path = path
        _displayedImage = State(initialValue: thumbnail)
    }

    var body: some View {
        GeometryReader { proxy in
            ZStack {
                Color(red: 7.0 / 255.0, green: 10.0 / 255.0, blue: 16.0 / 255.0)
                    .ignoresSafeArea()

                imageCanvas(in: proxy.size)

                if chromeVisible {
                    chrome(safeAreaInsets: proxy.safeAreaInsets, containerSize: proxy.size)
                        .transition(.opacity)
                }
            }
        }
        .preferredColorScheme(.dark)
        .statusBarHidden(!chromeVisible)
        .task(id: path) {
            await loadOriginalImage()
        }
        .sheet(isPresented: $presentsErrorDetails) {
            if let loadError {
                ErrorDetailView(error: loadError)
            }
        }
    }

    private func imageCanvas(in containerSize: CGSize) -> some View {
        let visibleScale = clampedScale(scale * gestureMagnification)
        let visibleOffset = clampedOffset(
            CGSize(
                width: offset.width + gestureTranslation.width,
                height: offset.height + gestureTranslation.height
            ),
            scale: visibleScale,
            containerSize: containerSize
        )

        return ZStack {
            Color.clear
            Image(uiImage: displayedImage)
                .resizable()
                .scaledToFit()
                .frame(width: containerSize.width, height: containerSize.height)
                .scaleEffect(visibleScale)
                .offset(visibleOffset)
                .shadow(color: .black.opacity(0.42), radius: 24, y: 12)
                .allowsHitTesting(false)
                .accessibilityLabel("图片".localizedForApp)
        }
        .contentShape(Rectangle())
        .gesture(tapGesture(in: containerSize))
        .simultaneousGesture(zoomAndPanGesture(in: containerSize))
    }

    private func chrome(safeAreaInsets: EdgeInsets, containerSize: CGSize) -> some View {
        VStack(spacing: 0) {
            HStack(spacing: 12) {
                viewerButton(
                    systemImage: "xmark",
                    accessibilityLabel: "关闭图片查看器".localizedForApp,
                    action: { dismiss() }
                )

                Spacer(minLength: 0)

                HStack(spacing: 3) {
                    viewerButton(
                        systemImage: "minus",
                        accessibilityLabel: "缩小".localizedForApp,
                        disabled: scale <= minimumScale,
                        action: { setScale(scale - zoomStep, in: containerSize) }
                    )

                    Button {
                        resetView()
                    } label: {
                        Text("\(Int((scale * 100).rounded()))%")
                            .font(.quartet(.detail, weight: .semibold, design: .monospaced))
                            .frame(minWidth: 58, minHeight: 44)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("恢复适合屏幕".localizedForApp)

                    viewerButton(
                        systemImage: "plus",
                        accessibilityLabel: "放大".localizedForApp,
                        disabled: scale >= maximumScale,
                        action: { setScale(scale + zoomStep, in: containerSize) }
                    )
                }
                .padding(3)
                .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 11, style: .continuous))

                Spacer(minLength: 0)

                viewerButton(
                    systemImage: "arrow.counterclockwise",
                    accessibilityLabel: "恢复适合屏幕".localizedForApp,
                    disabled: scale == 1 && offset == .zero,
                    action: resetView
                )
            }
            .foregroundStyle(.white)
            .padding(.horizontal, 14)
            .padding(.top, max(safeAreaInsets.top, 10))

            Spacer()

            VStack(spacing: 8) {
                if loadingOriginal {
                    Label {
                        Text("正在载入原图…".localizedForApp)
                    } icon: {
                        ProgressView().controlSize(.small).tint(.white)
                    }
                    .font(.quartet(.detail))
                    .foregroundStyle(.white.opacity(0.72))
                } else if loadError != nil {
                    Button {
                        presentsErrorDetails = true
                    } label: {
                        Label("原图加载失败，查看详情".localizedForApp, systemImage: "exclamationmark.triangle.fill")
                            .font(.quartet(.detail))
                            .foregroundStyle(.white)
                    }
                    .buttonStyle(.plain)
                }

                Text("双指缩放 · 放大后拖动 · 双击切换".localizedForApp)
                    .font(.quartet(.compact))
                    .foregroundStyle(.white.opacity(0.56))
            }
            .padding(.horizontal, 14)
            .padding(.bottom, max(safeAreaInsets.bottom, 12))
        }
    }

    private func viewerButton(
        systemImage: String,
        accessibilityLabel: String,
        disabled: Bool = false,
        action: @escaping () -> Void
    ) -> some View {
        Button(action: action) {
            Image(systemName: systemImage)
                .font(.quartet(.control, weight: .semibold))
                .frame(width: 44, height: 44)
                .background(.ultraThinMaterial, in: Circle())
                .contentShape(Circle())
        }
        .buttonStyle(.plain)
        .disabled(disabled)
        .opacity(disabled ? 0.34 : 1)
        .accessibilityLabel(accessibilityLabel)
    }

    private func zoomAndPanGesture(in containerSize: CGSize) -> some Gesture {
        let magnify = MagnifyGesture(minimumScaleDelta: 0)
            .updating($gestureMagnification) { value, state, _ in
                state = value.magnification
            }
            .onEnded { value in
                let nextScale = clampedScale(scale * value.magnification)
                scale = nextScale
                offset = clampedOffset(offset, scale: nextScale, containerSize: containerSize)
            }

        let drag = DragGesture(minimumDistance: 0)
            .updating($gestureTranslation) { value, state, _ in
                if clampedScale(scale * gestureMagnification) > 1 {
                    state = value.translation
                }
            }
            .onEnded { value in
                guard scale > 1 else {
                    offset = .zero
                    return
                }
                offset = clampedOffset(
                    CGSize(width: offset.width + value.translation.width, height: offset.height + value.translation.height),
                    scale: scale,
                    containerSize: containerSize
                )
            }

        return magnify.simultaneously(with: drag)
    }

    private func tapGesture(in containerSize: CGSize) -> some Gesture {
        TapGesture(count: 2)
            .exclusively(before: TapGesture(count: 1))
            .onEnded { value in
                switch value {
                case .first:
                    if scale == 1 {
                        setScale(2, in: containerSize)
                    } else {
                        resetView()
                    }
                case .second:
                    withAnimation(reduceMotion ? nil : .easeInOut(duration: 0.16)) {
                        chromeVisible.toggle()
                    }
                }
            }
    }

    private func setScale(_ proposed: CGFloat, in containerSize: CGSize) {
        let nextScale = clampedScale(proposed)
        withAnimation(reduceMotion ? nil : .easeOut(duration: 0.18)) {
            scale = nextScale
            offset = clampedOffset(offset, scale: nextScale, containerSize: containerSize)
        }
    }

    private func resetView() {
        withAnimation(reduceMotion ? nil : .easeOut(duration: 0.2)) {
            scale = 1
            offset = .zero
        }
    }

    private func clampedScale(_ value: CGFloat) -> CGFloat {
        min(maximumScale, max(minimumScale, value))
    }

    private func clampedOffset(_ proposed: CGSize, scale: CGFloat, containerSize: CGSize) -> CGSize {
        guard scale > 1 else { return .zero }
        let fitted = aspectFitSize(imageSize: displayedImage.size, containerSize: containerSize)
        let horizontalLimit = max(0, (fitted.width * scale - containerSize.width) / 2)
        let verticalLimit = max(0, (fitted.height * scale - containerSize.height) / 2)
        return CGSize(
            width: min(horizontalLimit, max(-horizontalLimit, proposed.width)),
            height: min(verticalLimit, max(-verticalLimit, proposed.height))
        )
    }

    private func aspectFitSize(imageSize: CGSize, containerSize: CGSize) -> CGSize {
        guard imageSize.width > 0, imageSize.height > 0,
              containerSize.width > 0, containerSize.height > 0 else { return .zero }
        let ratio = min(containerSize.width / imageSize.width, containerSize.height / imageSize.height)
        return CGSize(width: imageSize.width * ratio, height: imageSize.height * ratio)
    }

    @MainActor
    private func loadOriginalImage() async {
        loadingOriginal = true
        loadError = nil
        do {
            let client = try appModel.apiClient()
            let original = try await ChatImageLoader.shared.image(
                path: path,
                namespace: appModel.serverAddress,
                maxPixelSize: nil
            ) {
                try await client.fileData(path: path)
            }
            guard !Task.isCancelled else { return }
            displayedImage = original
        } catch {
            guard !Task.isCancelled else { return }
            if let apiError = error as? APIError {
                loadError = PresentedError(title: apiError.summary, detail: apiError.detail)
            } else {
                loadError = PresentedError(
                    title: "图片加载失败",
                    detail: String(describing: error)
                )
            }
        }
        loadingOriginal = false
    }
}
