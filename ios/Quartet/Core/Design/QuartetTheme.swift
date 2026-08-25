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

/// 排版档位。每一档同时给出基准磅值和动态字体的缩放基准；磅值刻意等于对应 text style
/// 在默认字号档下的系统尺寸，所以从 `.system(textStyle)` 换成 `UIFontMetrics` 缩放之后，
/// 各档在任何辅助功能字号下的实际大小都与过去保持一致。
enum QuartetFontSize {
    /// 卡片与弹窗标题。
    case large
    /// 段落标题，对应 Markdown `###`。
    case headline
    /// 聊天正文。中英混排的阅读档，也是用户气泡与 Agent 气泡唯一的正文尺寸 ——
    /// 两侧此前分别用 `.control` 和 `.regular`，同一条会话里一大一小。
    case reading
    /// 常规正文与列表主标题。
    case regular
    /// 控件与次级标题。
    case control
    /// 辅助说明。
    case detail
    /// 角标与时间戳。
    case compact

    fileprivate var pointSize: CGFloat {
        switch self {
        case .large: 20
        case .headline: 18
        case .reading: 16.5
        case .regular: 17
        case .control: 15
        case .detail: 13
        case .compact: 11
        }
    }

    fileprivate var textStyle: UIFont.TextStyle {
        switch self {
        case .large, .headline: .title3
        case .reading, .regular: .body
        case .control: .subheadline
        case .detail: .footnote
        case .compact: .caption2
        }
    }
}

extension Font {
    /// 全 App 的字体入口。`weight`/`design` 收的是 UIKit 类型，调用点照旧写
    /// `.semibold`、`.monospaced` 这样的字面量。
    static func quartet(
        _ size: QuartetFontSize,
        weight: UIFont.Weight = .regular,
        design: UIFontDescriptor.SystemDesign = .default
    ) -> Font {
        Font(QuartetTypeface.uiFont(size, weight: weight, design: design))
    }
}

/// 中英混排的字体栈：拉丁字形取 SF Pro / SF Mono，汉字经 cascade list 显式指向苹方。
///
/// 系统默认回退最终也会落到苹方，但有两件事必须自己接管：
/// 1. 回退结果取决于设备的语言偏好顺序 —— 日文优先的设备会把汉字渲染成 Hiragino 的
///    字形，同一段中文在不同人手机上长相不同；
/// 2. 回退把拉丁字重原样套给汉字，`semibold` 的中文在手机尺寸下明显发胖。这里让
///    semibold 落到苹方 Medium、bold 落到苹方 Semibold —— 公开家族里最重的就是
///    Semibold，`PingFangSC-Bold` 并不存在，直接引用会被静默替换成 Helvetica。
///
/// 注意：给字体描述符追加符号特征（`.bold`/`.italic`）会丢掉 cascade list，汉字随即
/// 退回系统回退。所以需要加粗的地方要在这里按字重取字体，而不是在 `Text` 上套 `.bold()`。
enum QuartetTypeface {
    static func uiFont(
        _ size: QuartetFontSize,
        weight: UIFont.Weight = .regular,
        design: UIFontDescriptor.SystemDesign = .default
    ) -> UIFont {
        // 每次求值都要重建描述符的话，一屏聊天要造上百个字体；档位组合本身很少，缓存住。
        let pointSize = UIFontMetrics(forTextStyle: size.textStyle).scaledValue(for: size.pointSize)
        let key = Key(pointSize: pointSize, weight: weight.rawValue, design: design.rawValue)
        if let hit = cache.font(for: key) { return hit }
        let font = build(pointSize: pointSize, weight: weight, design: design)
        cache.store(font, for: key)
        return font
    }

    private static func build(
        pointSize: CGFloat,
        weight: UIFont.Weight,
        design: UIFontDescriptor.SystemDesign
    ) -> UIFont {
        let latin = UIFont.systemFont(ofSize: pointSize, weight: weight)
        // withDesign 会保留字重，`.default` 时等价于原描述符。
        let base = latin.fontDescriptor.withDesign(design) ?? latin.fontDescriptor
        guard let han = hanFace(for: weight) else { return UIFont(descriptor: base, size: pointSize) }
        let descriptor = base.addingAttributes([
            .cascadeList: [UIFontDescriptor(fontAttributes: [.name: han])]
        ])
        return UIFont(descriptor: descriptor, size: pointSize)
    }

    /// 只返回确认装机的字面。名字匹配不上时 CoreText 会替换成 Helvetica 之类完全没有
    /// 汉字的字体，所以宁可返回 nil、把汉字交回系统回退。
    private static func hanFace(for weight: UIFont.Weight) -> String? {
        let face: String = switch weight.rawValue {
        case ..<UIFont.Weight.thin.rawValue: "PingFangSC-Ultralight"
        case ..<UIFont.Weight.light.rawValue: "PingFangSC-Thin"
        case ..<UIFont.Weight.regular.rawValue: "PingFangSC-Light"
        case ..<UIFont.Weight.medium.rawValue: "PingFangSC-Regular"
        // medium 与 semibold 都落到 Medium：中文的“强调”字重就是它。
        case ..<UIFont.Weight.bold.rawValue: "PingFangSC-Medium"
        default: "PingFangSC-Semibold"
        }
        return cache.isInstalled(face) ? face : nil
    }

    fileprivate struct Key: Hashable {
        let pointSize: CGFloat
        let weight: CGFloat
        let design: String
    }

    private static let cache = Cache()
}

/// `Font.quartet` 是非隔离的静态方法，聊天时间线里 Markdown 解析也会在主线程之外的
/// 上下文里取字体，所以缓存自己上锁。
private final class Cache: @unchecked Sendable {
    private let lock = NSLock()
    private var fonts: [QuartetTypeface.Key: UIFont] = [:]
    private var installed: [String: Bool] = [:]

    func font(for key: QuartetTypeface.Key) -> UIFont? {
        lock.lock()
        defer { lock.unlock() }
        return fonts[key]
    }

    func store(_ font: UIFont, for key: QuartetTypeface.Key) {
        lock.lock()
        defer { lock.unlock() }
        fonts[key] = font
    }

    func isInstalled(_ face: String) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if let known = installed[face] { return known }
        // UIFont(name:) 对不存在的字面返回 nil，正好用来做装机校验。
        let known = UIFont(name: face, size: 12) != nil
        installed[face] = known
        return known
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

/// 收起键盘：卡片式表单里点击输入控件以外的任意位置都调用它。
@MainActor
func quartetDismissKeyboard() {
    UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
}

/// 标准“选一个”弹窗里的一行。`disabled` 用于当前不可选、但仍需要让用户看到的选项。
struct QuartetChoice: Identifiable {
    let id: String
    let title: String
    let detail: String?
    let disabled: Bool

    init(id: String, title: String, detail: String? = nil, disabled: Bool = false) {
        self.id = id
        self.title = title
        self.detail = detail
        self.disabled = disabled
    }
}

/// 全局统一的“选一个”弹窗，样式、布局与间距跟随首页“任务操作”弹窗。
struct QuartetChoiceSheet: View {
    @Environment(\.dismiss) private var dismiss

    let title: String
    let choices: [QuartetChoice]
    @Binding var selection: String
    let accessibilityPrefix: String

    var body: some View {
        NavigationStack {
            ScrollView {
                LazyVStack(spacing: 0) {
                    ForEach(Array(choices.enumerated()), id: \.element.id) { index, choice in
                        if index > 0 {
                            Divider()
                                .overlay(QuartetTheme.divider)
                                .padding(.leading, 54)
                        }
                        choiceRow(choice)
                    }
                }
                .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                .overlay {
                    RoundedRectangle(cornerRadius: 18, style: .continuous)
                        .stroke(QuartetTheme.divider.opacity(0.8), lineWidth: 1)
                }
                .padding(.horizontal, 20)
                .padding(.top, 8)
                .padding(.bottom, 20)
            }
            .background(QuartetTheme.canvas)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .principal) {
                    Text(title.localizedForApp)
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .accessibilityAddTraits(.isHeader)
                }
            }
        }
    }

    private func choiceRow(_ choice: QuartetChoice) -> some View {
        let selected = choice.id == selection
        return Button {
            selection = choice.id
            dismiss()
        } label: {
            HStack(spacing: 12) {
                Image(systemName: selected ? "checkmark.circle.fill" : "circle")
                    .font(.quartet(.regular, weight: .semibold))
                    .foregroundStyle(selected ? QuartetTheme.accent : QuartetTheme.secondaryText)
                    .frame(width: 28)
                    .accessibilityHidden(true)

                VStack(alignment: .leading, spacing: 3) {
                    Text(choice.title.localizedForApp)
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    if let detail = choice.detail?.trimmingCharacters(in: .whitespacesAndNewlines),
                       !detail.isEmpty {
                        Text(detail.localizedForApp)
                            .font(.quartet(.detail))
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }

                Spacer(minLength: 8)
            }
            .padding(.horizontal, 14)
            .frame(minHeight: 60)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(choice.disabled)
        .opacity(choice.disabled ? 0.45 : 1)
        .accessibilityLabel(choice.detail.map { "\(choice.title.localizedForApp), \($0.localizedForApp)" } ?? choice.title.localizedForApp)
        .accessibilityAddTraits(selected ? .isSelected : [])
        .accessibilityHint("选择此项并关闭弹窗".localizedForApp)
        .accessibilityIdentifier("\(accessibilityPrefix)-\(choice.id)")
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
                    Text(title.localizedForApp)
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                    Text(detail.localizedForApp)
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
        .accessibilityLabel(title.localizedForApp)
        .accessibilityHint(detail.localizedForApp)
    }
}
