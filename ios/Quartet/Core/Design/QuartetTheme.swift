import SwiftUI
import UIKit

enum QuartetTheme {
    // Cool botanical neutrals keep the green hierarchy crisp in both appearances.
    static let canvas = dynamic(light: 0xF3F7F4, dark: 0x070D09)
    static let surface = dynamic(light: 0xFCFEFC, dark: 0x0F1712)
    static let elevated = dynamic(light: 0xE7EEE9, dark: 0x18221B)
    static let primaryText = dynamic(light: 0x142019, dark: 0xF0F7F2)
    static let secondaryText = dynamic(light: 0x5D6C62, dark: 0xA3B2A7)
    static let divider = dynamic(light: 0xCDD9D0, dark: 0x2C3A30)

    // Forest green owns brand and primary actions; amber remains semantic warning.
    static let accent = dynamic(light: 0x16A34A, dark: 0x4ADE80)
    static let accentDeep = dynamic(light: 0x047857, dark: 0x22C55E)
    static let accentSoft = dynamic(light: 0x22C55E, dark: 0x16A34A)
    static let onAccent = dynamic(light: 0xFFFFFF, dark: 0x052E16)
    static let terminalGreen = dynamic(light: 0x059669, dark: 0x34D399)
    static let terminalGreenMuted = dynamic(light: 0x4D7C0F, dark: 0xA3E635)
    static let success = dynamic(light: 0x16A34A, dark: 0x4ADE80)
    static let running = terminalGreen
    static let warning = dynamic(light: 0xA16207, dark: 0xFACC15)
    static let failed = dynamic(light: 0xB62435, dark: 0xFF5364)
    static let chatStop = Color(red: 239 / 255, green: 68 / 255, blue: 68 / 255)
    static let onDanger = dynamic(light: 0xFFFFFF, dark: 0x190205)
    static let stopped = dynamic(light: 0x686159, dark: 0xA39B91)

    // Code surfaces deliberately stay dark: this is the focused hacker-console moment.
    static let terminalBackground = dynamic(light: 0x10130F, dark: 0x040704)
    static let terminalText = dynamic(light: 0x42E978, dark: 0x62FF98)
    static let terminalBorder = dynamic(light: 0x285F39, dark: 0x245D36)

    // Charts stay within the botanical palette; line shape and labels remain the primary differentiators.
    static let chartPrimary = accent
    static let chartForest = accentDeep
    static let chartMint = accentSoft
    static let chartGreen = terminalGreen
    static let chartMutedGreen = terminalGreenMuted
    static let chartRed = failed
    static let chartGraphite = dynamic(light: 0x4F4942, dark: 0xC4BBB0)

    static func statusColor(_ status: String) -> Color {
        switch status.lowercased() {
        case "running": running
        case "pending", "awaitinginput", "stepstopping": warning
        case "completed": success
        case "failed", "timedout": failed
        default: stopped
        }
    }

    static func workspaceTint(_ seed: String?) -> Color {
        guard let seed, !seed.isEmpty else { return accent }
        let checksum = seed.unicodeScalars.reduce(0) { ($0 &* 31 &+ Int($1.value)) & 0x7fffffff }
        let palette = [chartPrimary, chartGreen, chartForest, chartMutedGreen, chartGraphite]
        return palette[checksum % palette.count]
    }

    static func workspaceTint(_ workspace: WorkspaceSummary) -> Color {
        guard let configuredColor = workspace.color,
              let color = color(hex: configuredColor) else {
            return workspaceTint(workspace.id)
        }
        return color
    }

    private static func color(hex value: String) -> Color? {
        let value = value.trimmingCharacters(in: .whitespacesAndNewlines)
        let digits = value.hasPrefix("#") ? String(value.dropFirst()) : value
        guard digits.count == 6, let rgb = UInt32(digits, radix: 16) else { return nil }
        return Color(
            .sRGB,
            red: Double((rgb >> 16) & 0xff) / 255,
            green: Double((rgb >> 8) & 0xff) / 255,
            blue: Double(rgb & 0xff) / 255,
            opacity: 1
        )
    }

    private static func dynamic(light: UInt32, dark: UInt32) -> Color {
        Color(uiColor: UIColor { traits in
            UIColor(rgb: traits.userInterfaceStyle == .dark ? dark : light)
        })
    }
}

enum QuartetFontSize {
    case large
    case regular
    case control
    case detail
    case compact

    fileprivate var textStyle: Font.TextStyle {
        switch self {
        case .large: .title3
        case .regular: .body
        case .control: .subheadline
        case .detail: .footnote
        case .compact: .caption2
        }
    }
}

extension Font {
    static func quartet(
        _ size: QuartetFontSize,
        weight: Font.Weight? = nil,
        design: Font.Design = .default
    ) -> Font {
        .system(size.textStyle, design: design, weight: weight)
    }
}

private extension UIColor {
    convenience init(rgb: UInt32) {
        self.init(
            red: CGFloat((rgb >> 16) & 0xff) / 255,
            green: CGFloat((rgb >> 8) & 0xff) / 255,
            blue: CGFloat(rgb & 0xff) / 255,
            alpha: 1
        )
    }
}

struct PulseMark: View {
    var body: some View {
        HStack(spacing: 5) {
            ForEach(0..<4, id: \.self) { index in
                RoundedRectangle(cornerRadius: 2)
                    .fill(index == 2 ? QuartetTheme.accent : QuartetTheme.terminalGreenMuted.opacity(0.58))
                    .frame(width: 4, height: [12, 24, 36, 18][index])
            }
        }
        .accessibilityHidden(true)
    }
}

struct RunningPulseLine: View {
    let active: Bool
    @State private var moving = false

    var body: some View {
        GeometryReader { proxy in
            ZStack(alignment: .leading) {
                Rectangle().fill(QuartetTheme.divider)
                if active {
                    Capsule()
                        .fill(QuartetTheme.running)
                        .frame(width: max(48, proxy.size.width * 0.25))
                        .offset(x: moving ? proxy.size.width : -proxy.size.width * 0.25)
                }
            }
            .clipShape(Capsule())
        }
        .frame(height: 2)
        .onAppear { moving = true }
        .animation(active ? .linear(duration: 1.8).repeatForever(autoreverses: false) : .default, value: moving)
        .accessibilityHidden(true)
    }
}

extension View {
    func quartetSheetStyle() -> some View {
        modifier(QuartetSheetStyleModifier())
    }

    func quartetPlainNavigationBackButton() -> some View {
        modifier(QuartetPlainNavigationBackButtonModifier())
    }
}

private struct QuartetPlainNavigationBackButtonModifier: ViewModifier {
    @Environment(\.dismiss) private var dismiss

    func body(content: Content) -> some View {
        content
            .navigationBarBackButtonHidden(true)
            .background(QuartetNavigationSwipeBackEnabler())
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button { dismiss() } label: {
                        Image(systemName: "chevron.left")
                            .font(.body.weight(.semibold))
                    }
                    .accessibilityLabel("返回")
                }
                .sharedBackgroundVisibility(.hidden)
            }
    }
}

private struct QuartetSheetStyleModifier: ViewModifier {
    @Environment(\.dismiss) private var dismiss

    func body(content: Content) -> some View {
        content
            .presentationDragIndicator(.visible)
            .presentationCornerRadius(28)
            .presentationBackground(QuartetTheme.canvas)
            .background(QuartetSheetSwipeBackInstaller { dismiss() })
    }
}

private struct QuartetSheetSwipeBackInstaller: UIViewControllerRepresentable {
    let onDismiss: () -> Void

    func makeUIViewController(context: Context) -> ResolverViewController {
        ResolverViewController(onDismiss: onDismiss)
    }

    func updateUIViewController(_ uiViewController: ResolverViewController, context: Context) {
        uiViewController.onDismiss = onDismiss
        uiViewController.installGestureRecognizerIfPossible()
    }

    static func dismantleUIViewController(_ uiViewController: ResolverViewController, coordinator: Void) {
        uiViewController.removeGestureRecognizer()
    }

    final class ResolverViewController: UIViewController, UIGestureRecognizerDelegate {
        fileprivate var onDismiss: () -> Void
        private weak var gestureContainer: UIView?
        private lazy var gestureRecognizer: UIScreenEdgePanGestureRecognizer = {
            let recognizer = UIScreenEdgePanGestureRecognizer(target: self, action: #selector(handleSwipeBack(_:)))
            recognizer.edges = .left
            recognizer.delegate = self
            return recognizer
        }()

        init(onDismiss: @escaping () -> Void) {
            self.onDismiss = onDismiss
            super.init(nibName: nil, bundle: nil)
        }

        @available(*, unavailable)
        required init?(coder: NSCoder) {
            fatalError("init(coder:) has not been implemented")
        }

        override func didMove(toParent parent: UIViewController?) {
            super.didMove(toParent: parent)
            installGestureRecognizerIfPossible()
        }

        override func viewDidAppear(_ animated: Bool) {
            super.viewDidAppear(animated)
            installGestureRecognizerIfPossible()
        }

        fileprivate func installGestureRecognizerIfPossible() {
            var containerController: UIViewController = self
            while let parent = containerController.parent {
                containerController = parent
            }
            let container = containerController.view
            guard gestureContainer !== container else { return }
            removeGestureRecognizer()
            container?.addGestureRecognizer(gestureRecognizer)
            gestureContainer = container
        }

        fileprivate func removeGestureRecognizer() {
            gestureContainer?.removeGestureRecognizer(gestureRecognizer)
            gestureContainer = nil
        }

        func gestureRecognizerShouldBegin(_ gestureRecognizer: UIGestureRecognizer) -> Bool {
            guard let gestureRecognizer = gestureRecognizer as? UIScreenEdgePanGestureRecognizer else {
                return false
            }
            let velocity = gestureRecognizer.velocity(in: gestureRecognizer.view)
            return velocity.x > abs(velocity.y)
        }

        @objc private func handleSwipeBack(_ gestureRecognizer: UIScreenEdgePanGestureRecognizer) {
            guard gestureRecognizer.state == .ended else { return }
            let translation = gestureRecognizer.translation(in: gestureRecognizer.view)
            let velocity = gestureRecognizer.velocity(in: gestureRecognizer.view)
            guard translation.x >= 72 || velocity.x >= 700 else { return }
            onDismiss()
        }
    }
}

private struct QuartetNavigationSwipeBackEnabler: UIViewControllerRepresentable {
    func makeUIViewController(context: Context) -> ResolverViewController {
        ResolverViewController()
    }

    func updateUIViewController(_ uiViewController: ResolverViewController, context: Context) {
        uiViewController.enableSwipeBackIfPossible()
    }

    final class ResolverViewController: UIViewController {
        override func didMove(toParent parent: UIViewController?) {
            super.didMove(toParent: parent)
            enableSwipeBackIfPossible()
        }

        override func viewWillAppear(_ animated: Bool) {
            super.viewWillAppear(animated)
            enableSwipeBackIfPossible()
        }

        fileprivate func enableSwipeBackIfPossible() {
            guard let navigationController,
                  let gestureRecognizer = navigationController.interactivePopGestureRecognizer else {
                return
            }
            gestureRecognizer.delegate = QuartetNavigationSwipeBackGestureDelegate.shared
            gestureRecognizer.isEnabled = navigationController.viewControllers.count > 1
        }
    }
}

private final class QuartetNavigationSwipeBackGestureDelegate: NSObject, UIGestureRecognizerDelegate {
    static let shared = QuartetNavigationSwipeBackGestureDelegate()

    func gestureRecognizerShouldBegin(_ gestureRecognizer: UIGestureRecognizer) -> Bool {
        guard let navigationController = gestureRecognizer.view?.next as? UINavigationController else {
            return false
        }
        return navigationController.viewControllers.count > 1
            && navigationController.transitionCoordinator == nil
    }
}

struct AttachmentSourcePopover: View {
    let onPhotoLibrary: () -> Void
    let onCamera: () -> Void
    let onFile: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            VStack(alignment: .leading, spacing: 4) {
                Text("添加附件")
                    .font(.quartet(.regular, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                Text("从相册、相机或系统文件中添加图片。")
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .fixedSize(horizontal: false, vertical: true)
            }

            VStack(spacing: 0) {
                actionRow(
                    title: "相册",
                    detail: "从照片图库中选择",
                    systemImage: "photo.on.rectangle.angled",
                    action: onPhotoLibrary
                )

                Divider()
                    .overlay(QuartetTheme.divider)
                    .padding(.leading, 52)

                actionRow(
                    title: "相机",
                    detail: "拍摄一张新照片",
                    systemImage: "camera.fill",
                    action: onCamera
                )

                Divider()
                    .overlay(QuartetTheme.divider)
                    .padding(.leading, 52)

                actionRow(
                    title: "文件",
                    detail: "支持最大 10MB 的图片",
                    systemImage: "folder.fill",
                    action: onFile
                )
            }
            .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        }
        .padding(16)
        .frame(width: 286)
        .background(QuartetTheme.surface)
        .presentationCompactAdaptation(.popover)
        .presentationBackground(QuartetTheme.surface)
    }

    private func actionRow(
        title: String,
        detail: String,
        systemImage: String,
        action: @escaping () -> Void
    ) -> some View {
        Button(action: action) {
            HStack(spacing: 12) {
                Image(systemName: systemImage)
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.accent)
                    .frame(width: 34, height: 34)
                    .background(QuartetTheme.accent.opacity(0.12), in: Circle())

                VStack(alignment: .leading, spacing: 2) {
                    Text(title)
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                    Text(detail)
                        .font(.quartet(.compact))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }

                Spacer(minLength: 8)

                Image(systemName: "chevron.right")
                    .font(.quartet(.compact, weight: .bold))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
            .padding(.horizontal, 10)
            .frame(minHeight: 58)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(title)
        .accessibilityHint(detail)
    }
}
