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
    /// 软件更新专用语义色。正文在深色界面提亮，实心按钮保持较深橘色以承载白字。
    static let softwareUpdate = dynamic(light: 0xC2410C, dark: 0xFB923C)
    static let softwareUpdateAction = Color(red: 194 / 255, green: 65 / 255, blue: 12 / 255)
    static let softwareUpdateActionDisabled = Color(red: 124 / 255, green: 45 / 255, blue: 18 / 255)
    static let onSoftwareUpdate = Color.white
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

/// 同一套档位在不同页面上的整体大小。聊天页文字密度远高于其它页面，全局梯放在那里偏大，
/// 所以给它单独一档整体缩小的刻度 —— 想再调聊天页字号，只改 `chatReduction` 一个数字，
/// 不必逐个调用点改档，也不会波及任务列表、设置和统计页。
enum QuartetTypeScale {
    /// 除聊天页以外的全部页面。
    case app
    /// 聊天页。
    case chat

    /// 聊天页相对全局梯收掉的磅值。
    private static let chatReduction: CGFloat = 2.5
    /// 收缩后的下限。再小时间戳和角标就不可读了，`.compact` 会撞到这个下限。
    private static let chatFloor: CGFloat = 9

    fileprivate func pointSize(for size: QuartetFontSize) -> CGFloat {
        switch self {
        case .app: size.pointSize
        case .chat: max(size.pointSize - Self.chatReduction, Self.chatFloor)
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

    /// 聊天页的字体入口：档位语义与 `quartet` 完全一致，只是整体小一号。
    static func chat(
        _ size: QuartetFontSize,
        weight: UIFont.Weight = .regular,
        design: UIFontDescriptor.SystemDesign = .default
    ) -> Font {
        Font(QuartetTypeface.uiFont(size, weight: weight, design: design, scale: .chat))
    }
}

/// 中英混排的字体栈。
///
/// 混排“不好看”的根因不是某一种字体丑，而是拉丁和汉字来自两套互不相干的设计：SF Pro 的
/// x 高度、字重曲线和竖直居中都跟苹方对不上，同一段里英文显得偏小偏高、中文偏大偏低；
/// 实测纯拉丁行的自然行高只有汉字行的 84%（1.18em vs 1.40em），中英交替的段落基线一直在跳。
/// 所以正文换成中英同源的字族：思源黑体 SC（Noto Sans SC，SIL OFL）的拉丁与汉字出自同一
/// 套设计，整段的 x 高度、字重和行高是一条线，混排的节奏才对得上。
///
/// 想换字体只改 `family` 一行。字族没装上（打包漏文件、文件损坏）时自动退回系统的
/// SF Pro + 苹方，不会出现豆腐块。
///
/// 注意：给字体描述符追加符号特征（`.bold`/`.italic`）会丢掉 cascade list，汉字随即
/// 退回系统回退。所以需要加粗的地方要在这里按字重取字体，而不是在 `Text` 上套 `.bold()`。
enum QuartetTypeface {
    /// 正文字族。
    private static let family: Family = .notoSansSC

    enum Family {
        /// SF Pro + 苹方，系统默认栈。
        case system
        /// 思源黑体 SC，随 App 打包；GB2312 子集，约 2.4MB/字重。
        case notoSansSC

        /// 该字族在此字重下的字面名，`nil` 表示走系统栈。
        fileprivate func face(for weight: UIFont.Weight) -> String? {
            switch self {
            case .system:
                nil
            case .notoSansSC:
                // 只打包了 400/500/700 三个字重：常规、强调（标题与 semibold）、加粗。
                switch weight.rawValue {
                case ..<UIFont.Weight.medium.rawValue: "NotoSansSC-Regular"
                case ..<UIFont.Weight.bold.rawValue: "NotoSansSC-Medium"
                default: "NotoSansSC-Bold"
                }
            }
        }
    }

    static func uiFont(
        _ size: QuartetFontSize,
        weight: UIFont.Weight = .regular,
        design: UIFontDescriptor.SystemDesign = .default,
        scale: QuartetTypeScale = .app
    ) -> UIFont {
        // 每次求值都要重建描述符的话，一屏聊天要造上百个字体；档位组合本身很少，缓存住。
        // 缓存键里存的是缩放后的磅值，所以两套刻度天然不会互相命中。
        let pointSize = UIFontMetrics(forTextStyle: size.textStyle)
            .scaledValue(for: scale.pointSize(for: size))
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
        // `.default` 之外的设计（等宽、圆体）是拉丁字形本身的诉求，正文字族没有对应字面，
        // 所以拉丁保留 SF Mono / SF Rounded，只把汉字接到正文字族上。
        guard design == .default, let face = family.face(for: weight), cache.isInstalled(face) else {
            let latin = UIFont.systemFont(ofSize: pointSize, weight: weight)
            // withDesign 会保留字重，`.default` 时等价于原描述符。
            let base = latin.fontDescriptor.withDesign(design) ?? latin.fontDescriptor
            return cascading(base, to: hanCascade(for: weight), pointSize: pointSize)
        }
        // 字族已经同时覆盖中英文，cascade 只用来兜住子集里没有的生僻字和繁体。
        let base = UIFontDescriptor(fontAttributes: [.name: face])
        return cascading(base, to: [pingFangFace(for: weight)], pointSize: pointSize)
    }

    /// 汉字的回退顺序：先用正文字族，字族取不到或子集缺字时落到苹方。
    private static func hanCascade(for weight: UIFont.Weight) -> [String] {
        [family.face(for: weight), pingFangFace(for: weight)].compactMap { $0 }
    }

    /// 只保留确认装机的字面。名字匹配不上时 CoreText 会替换成 Helvetica 之类完全没有
    /// 汉字的字体，所以宁可丢掉这一级、把汉字交回系统回退。
    private static func cascading(
        _ base: UIFontDescriptor,
        to faces: [String],
        pointSize: CGFloat
    ) -> UIFont {
        let installed = faces.filter { cache.isInstalled($0) }
        guard !installed.isEmpty else { return UIFont(descriptor: base, size: pointSize) }
        let descriptor = base.addingAttributes([
            .cascadeList: installed.map { UIFontDescriptor(fontAttributes: [.name: $0]) }
        ])
        return UIFont(descriptor: descriptor, size: pointSize)
    }

    /// 最后一级汉字兜底。苹方一定装机，但公开家族里最重的只有 Semibold ——
    /// `PingFangSC-Bold` 并不存在，直接引用会被静默替换成 Helvetica。
    private static func pingFangFace(for weight: UIFont.Weight) -> String {
        switch weight.rawValue {
        case ..<UIFont.Weight.thin.rawValue: "PingFangSC-Ultralight"
        case ..<UIFont.Weight.light.rawValue: "PingFangSC-Thin"
        case ..<UIFont.Weight.regular.rawValue: "PingFangSC-Light"
        case ..<UIFont.Weight.medium.rawValue: "PingFangSC-Regular"
        // medium 与 semibold 都落到 Medium：中文的“强调”字重就是它。
        case ..<UIFont.Weight.bold.rawValue: "PingFangSC-Medium"
        default: "PingFangSC-Semibold"
        }
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
    /// 补充信息（如 Agent 的版本号与用量摘要），与 `detail` 在同一条副标题中显示。
    /// 内容由调用方拼好并本地化，弹窗不再二次查表。
    let footnote: String?
    /// `footnote` 承载的是失败原因，按警示色渲染。
    let footnoteIsFailure: Bool
    /// `footnote` 背后的完整错误原文。非空时行尾出现警示按钮，点开原样展示、可复制。
    let footnoteDetail: String?
    /// 重新读取 `footnote` 的动作。非空时行尾出现刷新按钮，供读取失败后重试。
    let footnoteRetry: (() -> Void)?
    let disabled: Bool

    init(
        id: String,
        title: String,
        detail: String? = nil,
        footnote: String? = nil,
        footnoteIsFailure: Bool = false,
        footnoteDetail: String? = nil,
        footnoteRetry: (() -> Void)? = nil,
        disabled: Bool = false
    ) {
        self.id = id
        self.title = title
        self.detail = detail
        self.footnote = footnote
        self.footnoteIsFailure = footnoteIsFailure
        self.footnoteDetail = footnoteDetail
        self.footnoteRetry = footnoteRetry
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
    let favoriteIDs: Set<String>

    /// 行内 `footnote` 对应的完整错误。用弹窗内的二级 sheet 展示，
    /// 避免和根视图的错误 sheet 抢同一个 presentation。
    @State private var footnoteError: PresentedError?

    init(
        title: String,
        choices: [QuartetChoice],
        selection: Binding<String>,
        accessibilityPrefix: String,
        favoriteIDs: Set<String> = []
    ) {
        self.title = title
        self.choices = choices
        _selection = selection
        self.accessibilityPrefix = accessibilityPrefix
        self.favoriteIDs = favoriteIDs
    }

    private var favoriteChoices: [QuartetChoice] {
        choices.filter { favoriteIDs.contains($0.id) }
    }

    private var otherChoices: [QuartetChoice] {
        choices.filter { !favoriteIDs.contains($0.id) }
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    if favoriteChoices.isEmpty {
                        choiceGroup(choices)
                    } else {
                        choiceGroup(favoriteChoices, title: "收藏".localizedForApp)
                        if !otherChoices.isEmpty {
                            choiceGroup(otherChoices, title: "其他模型".localizedForApp)
                        }
                    }
                }
                .padding(.horizontal, 20)
                .padding(.top, 8)
                .padding(.bottom, 24)
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
        .sheet(item: $footnoteError) { error in
            ErrorDetailView(error: error)
        }
    }

    @ViewBuilder
    private func choiceGroup(_ groupChoices: [QuartetChoice], title: String? = nil) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            if let title {
                Text(title)
                    .font(.quartet(.detail, weight: .semibold))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .padding(.horizontal, 4)
            }

            LazyVStack(spacing: 0) {
                ForEach(Array(groupChoices.enumerated()), id: \.element.id) { index, choice in
                    choiceRow(choice)
                    if index < groupChoices.count - 1 {
                        Divider()
                            .overlay(QuartetTheme.divider)
                            .padding(.leading, 54)
                    }
                }
            }
            .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            .overlay {
                RoundedRectangle(cornerRadius: 18, style: .continuous)
                    .stroke(QuartetTheme.divider.opacity(0.8), lineWidth: 1)
            }
        }
    }

    private func choiceRow(_ choice: QuartetChoice) -> some View {
        let selected = choice.id == selection
        let detail = resolvedDetail(choice)
        let footnote = resolvedFootnote(choice)
        // 选中按钮和错误入口必须是兄弟节点：嵌在 Button label 里的按钮收不到点击。
        return HStack(spacing: 0) {
            Button {
                selection = choice.id
                dismiss()
            } label: {
                HStack(spacing: 12) {
                    Image(systemName: choiceIcon(choice, selected: selected))
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(
                            selected || favoriteIDs.contains(choice.id)
                                ? QuartetTheme.accent
                                : QuartetTheme.secondaryText
                        )
                        .frame(width: 28)
                        .accessibilityHidden(true)

                    VStack(alignment: .leading, spacing: 3) {
                        Text(choice.title.localizedForApp)
                            .font(.quartet(.control, weight: .semibold))
                            .foregroundStyle(QuartetTheme.primaryText)
                            .frame(maxWidth: .infinity, alignment: .leading)
                        if detail != nil || footnote != nil {
                            HStack(spacing: 4) {
                                if let detail {
                                    Text(detail)
                                        .font(.quartet(.detail))
                                        .foregroundStyle(QuartetTheme.secondaryText)
                                }
                                if detail != nil, footnote != nil {
                                    Text("·")
                                        .font(.quartet(.compact))
                                        .foregroundStyle(QuartetTheme.secondaryText.opacity(0.78))
                                }
                                if let footnote {
                                    Text(footnote)
                                        .font(.quartet(.compact, design: .monospaced))
                                        .foregroundStyle(
                                            choice.footnoteIsFailure
                                                ? QuartetTheme.failed
                                                : QuartetTheme.secondaryText.opacity(0.78)
                                        )
                                }
                            }
                            .lineLimit(1)
                            .truncationMode(.tail)
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
            .accessibilityLabel(choiceLabel(choice))
            .accessibilityAddTraits(selected ? .isSelected : [])
            .accessibilityHint("选择此项并关闭弹窗".localizedForApp)
            .accessibilityIdentifier("\(accessibilityPrefix)-\(choice.id)")

            if let footnoteDetail = choice.footnoteDetail?.trimmingCharacters(in: .whitespacesAndNewlines),
               !footnoteDetail.isEmpty {
                Button {
                    footnoteError = PresentedError(
                        title: choice.title.localizedForApp,
                        detail: footnoteDetail
                    )
                } label: {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .font(.quartet(.detail, weight: .semibold))
                        .foregroundStyle(QuartetTheme.failed)
                        .frame(width: 30, height: 44)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                // 重试按钮在更外侧时由它负责右边距，避免两颗按钮之间被撑开。
                .padding(.trailing, choice.footnoteRetry == nil ? 8 : 0)
                .accessibilityLabel("查看错误详情".localizedForApp)
                // 故意不带 accessibilityPrefix：按前缀枚举选项的 UI 测试不该把它当成一行选项。
                .accessibilityIdentifier("quartet-choice-error-\(accessibilityPrefix)-\(choice.id)")
            }

            if let retry = choice.footnoteRetry {
                Button {
                    retry()
                } label: {
                    Image(systemName: "arrow.clockwise")
                        .font(.quartet(.detail, weight: .semibold))
                        .foregroundStyle(QuartetTheme.accent)
                        .frame(width: 30, height: 44)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .padding(.trailing, 8)
                .accessibilityLabel("重试".localizedForApp)
                .accessibilityIdentifier("quartet-choice-retry-\(accessibilityPrefix)-\(choice.id)")
            }
        }
    }

    /// 和主标题一字不差的副标题没有信息量，直接不显示。
    private func resolvedDetail(_ choice: QuartetChoice) -> String? {
        guard let detail = choice.detail?.trimmingCharacters(in: .whitespacesAndNewlines),
              !detail.isEmpty else { return nil }
        let localized = detail.localizedForApp
        let title = choice.title.localizedForApp.trimmingCharacters(in: .whitespacesAndNewlines)
        return localized.trimmingCharacters(in: .whitespacesAndNewlines) == title ? nil : localized
    }

    private func resolvedFootnote(_ choice: QuartetChoice) -> String? {
        guard let footnote = choice.footnote?.trimmingCharacters(in: .whitespacesAndNewlines),
              !footnote.isEmpty else { return nil }
        return footnote
    }

    private func choiceLabel(_ choice: QuartetChoice) -> String {
        [choice.title.localizedForApp, resolvedDetail(choice), choice.footnote]
            .compactMap { $0?.trimmingCharacters(in: .whitespacesAndNewlines) }
            .filter { !$0.isEmpty }
            .joined(separator: ", ")
    }

    private func choiceIcon(_ choice: QuartetChoice, selected: Bool) -> String {
        if selected { return "checkmark.circle.fill" }
        if favoriteIDs.contains(choice.id) { return "star.fill" }
        return "circle"
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
