import SwiftUI
import UIKit

/// Agent 管理的共用页面壳。iOS 在设置页直接展示四个入口，不再增加一层横向标签。
struct AgentSettingsDestination<Content: View>: View {
    @Environment(\.mainTabBarInset) private var mainTabBarInset

    let title: String
    let content: Content

    init(title: String, @ViewBuilder content: () -> Content) {
        self.title = title
        self.content = content()
    }

    var body: some View {
        content
            .mainTabBarBottomInset(mainTabBarInset)
            .navigationTitle(LocalizedStringKey(title))
            .navigationBarTitleDisplayMode(.inline)
            .quartetPlainNavigationBackButton()
            .toolbarBackground(QuartetTheme.canvas, for: .navigationBar)
            .toolbarBackground(.visible, for: .navigationBar)
    }
}

// MARK: - 共用组件

/// 卡片容器。键盘收起由 RootView 的全局手势统一处理，避免卡片手势抢占输入控件。
struct AgentSettingsCard<Content: View>: View {
    private let title: String?
    private let systemImage: String?
    private let content: Content

    init(_ title: String? = nil, systemImage: String? = nil, @ViewBuilder content: () -> Content) {
        self.title = title
        self.systemImage = systemImage
        self.content = content()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            if let title {
                if let systemImage {
                    Label(title.localizedForApp, systemImage: systemImage)
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                } else {
                    Text(title.localizedForApp)
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                }
            }
            content
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(RoundedRectangle(cornerRadius: 18, style: .continuous).fill(QuartetTheme.surface))
        .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(QuartetTheme.divider.opacity(0.8)))
    }
}

func agentSettingsFieldLabel(_ title: String) -> some View {
    Text(title.localizedForApp)
        .font(.quartet(.detail, weight: .semibold))
        .foregroundStyle(QuartetTheme.secondaryText)
        .frame(maxWidth: .infinity, alignment: .leading)
}

func agentSettingsHint(_ text: String) -> some View {
    Text(text.localizedForApp)
        .font(.quartet(.detail))
        .foregroundStyle(QuartetTheme.secondaryText)
        .fixedSize(horizontal: false, vertical: true)
        .frame(maxWidth: .infinity, alignment: .leading)
}

func agentSettingsDivider() -> some View {
    Divider().overlay(QuartetTheme.divider)
}

/// 只读的等宽文本行，用来展示 AgentID、启动命令和安装命令。
struct AgentSettingsMonoRow: View {
    let label: String
    let value: String

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            agentSettingsFieldLabel(label)
            Text(value)
                .font(.quartet(.detail, design: .monospaced))
                .foregroundStyle(QuartetTheme.primaryText)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

/// “选一个”入口行：左边标题、右边当前值，点击后交给调用方打开选择弹窗。
struct AgentSettingsSelectionRow: View {
    let title: String
    let value: String
    let placeholder: Bool
    let identifier: String
    let action: () -> Void

    init(
        title: String,
        value: String,
        placeholder: Bool = false,
        identifier: String,
        action: @escaping () -> Void
    ) {
        self.title = title
        self.value = value
        self.placeholder = placeholder
        self.identifier = identifier
        self.action = action
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            agentSettingsFieldLabel(title)
            Button {
                quartetDismissKeyboard()
                action()
            } label: {
                HStack {
                    Text(value.localizedForApp)
                        .font(.quartet(.control, weight: .medium))
                        .foregroundStyle(placeholder ? QuartetTheme.secondaryText : QuartetTheme.primaryText)
                        .lineLimit(1)
                    Spacer(minLength: 8)
                    Image(systemName: "chevron.up.chevron.down")
                        .font(.quartet(.compact, weight: .bold))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                .padding(.horizontal, 14)
                .frame(height: 48)
                .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier(identifier)
        }
    }
}

struct AgentSettingsTextField: View {
    let title: String
    @Binding var text: String
    let identifier: String
    var placeholder: String = ""
    var monospaced: Bool = false

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            agentSettingsFieldLabel(title)
            TextField(LocalizedStringKey(placeholder.isEmpty ? title : placeholder), text: $text)
                .font(monospaced ? .quartet(.control, design: .monospaced) : .quartet(.control))
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .padding(.horizontal, 14)
                .frame(height: 48)
                .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                .accessibilityIdentifier(identifier)
        }
    }
}

struct AgentSettingsTextEditor: View {
    let title: String
    @Binding var text: String
    let identifier: String
    var hint: String = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            agentSettingsFieldLabel(title)
            TextEditor(text: $text)
                .font(.quartet(.control, design: .monospaced))
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .scrollContentBackground(.hidden)
                .padding(10)
                .frame(minHeight: 110)
                .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                .accessibilityIdentifier(identifier)
            if !hint.isEmpty { agentSettingsHint(hint) }
        }
    }
}

/// 页面内的成功 / 失败提示。失败文案保留后端返回的全文，并允许长按复制。
struct AgentSettingsMessage: Equatable {
    enum Kind {
        case info
        case update
        case success
        case failure
    }

    let kind: Kind
    let text: String

    static func success(_ text: String) -> AgentSettingsMessage {
        AgentSettingsMessage(kind: .success, text: text)
    }

    static func failure(_ text: String) -> AgentSettingsMessage {
        AgentSettingsMessage(kind: .failure, text: text)
    }
}

struct AgentSettingsMessageView: View {
    typealias Kind = AgentSettingsMessage.Kind

    let kind: Kind
    let text: String

    init(kind: Kind, text: String) {
        self.kind = kind
        self.text = text
    }

    init(_ message: AgentSettingsMessage) {
        kind = message.kind
        text = message.text
    }

    var body: some View {
        let tint: Color = switch kind {
        case .info: QuartetTheme.accent
        case .update: QuartetTheme.softwareUpdate
        case .success: QuartetTheme.success
        case .failure: QuartetTheme.failed
        }
        let icon = switch kind {
        case .info: "arrow.triangle.2.circlepath"
        case .update: "arrow.up.circle.fill"
        case .success: "checkmark.circle.fill"
        case .failure: "exclamationmark.triangle.fill"
        }
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: icon)
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(tint)
                .accessibilityHidden(true)
            Text(text)
                .font(kind == .failure ? .quartet(.detail, design: .monospaced) : .quartet(.detail, weight: .semibold))
                .foregroundStyle(tint)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            tint.opacity(0.08),
            in: RoundedRectangle(cornerRadius: 14, style: .continuous)
        )
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(kind == .failure ? .isStaticText : [])
    }
}

/// 加载失败时的整页占位，保留完整错误正文并提供重试。
struct AgentSettingsLoadFailure: View {
    let detail: String
    let onRetry: () -> Void

    var body: some View {
        ScrollView {
            VStack(spacing: 14) {
                AgentSettingsMessageView(kind: .failure, text: detail)
                HStack(spacing: 10) {
                    Button("复制错误") { UIPasteboard.general.string = detail }
                        .foregroundStyle(QuartetTheme.primaryText)
                        .frame(maxWidth: .infinity)
                        .frame(height: 48)
                        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                    Button("重试", action: onRetry)
                        .foregroundStyle(QuartetTheme.onAccent)
                        .frame(maxWidth: .infinity)
                        .frame(height: 48)
                        .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                        .accessibilityIdentifier("agent-settings-retry")
                }
                .font(.quartet(.control, weight: .semibold))
                .buttonStyle(.plain)
            }
            .padding(18)
        }
        .background(QuartetTheme.canvas)
    }
}

struct AgentSettingsLoadingView: View {
    let title: String

    var body: some View {
        VStack(spacing: 12) {
            ProgressView().controlSize(.large).tint(QuartetTheme.accent)
            Text(LocalizedStringKey(title))
                .font(.quartet(.control))
                .foregroundStyle(QuartetTheme.secondaryText)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(QuartetTheme.canvas)
        .accessibilityIdentifier("agent-settings-loading")
    }
}

/// 底部保存条，样式与定时任务编辑页保持一致。
struct AgentSettingsSaveBar: View {
    let title: String
    let savingTitle: String
    let isSaving: Bool
    let isEnabled: Bool
    let identifier: String
    let action: () -> Void

    var body: some View {
        Button {
            quartetDismissKeyboard()
            action()
        } label: {
            HStack(spacing: 8) {
                if isSaving { ProgressView().tint(QuartetTheme.onAccent) }
                Text(LocalizedStringKey(isSaving ? savingTitle : title))
                    .font(.quartet(.control, weight: .semibold))
            }
            .foregroundStyle(QuartetTheme.onAccent)
            .frame(maxWidth: .infinity)
            .frame(height: 50)
            .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
        }
        .buttonStyle(.plain)
        .disabled(isSaving || !isEnabled)
        .opacity(isSaving || !isEnabled ? 0.45 : 1)
        .accessibilityIdentifier(identifier)
        .padding(.horizontal, 18)
        .padding(.vertical, 10)
        .background(.ultraThinMaterial)
    }
}

/// 全量展示错误正文的弹窗，保留请求方法、URL、状态码并支持复制。
struct AgentSettingsErrorSheet: View {
    @Environment(\.dismiss) private var dismiss
    let error: PresentedError

    var body: some View {
        NavigationStack {
            VStack(spacing: 18) {
                HStack(alignment: .top, spacing: 14) {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.failed)
                        .frame(width: 44, height: 44)
                        .background(QuartetTheme.failed.opacity(0.12), in: Circle())
                    ScrollView {
                        Text(error.detail)
                            .font(.system(.caption, design: .monospaced))
                            .foregroundStyle(QuartetTheme.primaryText)
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
                .padding(16)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(QuartetTheme.failed.opacity(0.07), in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(QuartetTheme.failed.opacity(0.2)))

                HStack(spacing: 10) {
                    Button("复制错误") { UIPasteboard.general.string = error.detail }
                        .foregroundStyle(QuartetTheme.primaryText)
                        .frame(maxWidth: .infinity).frame(height: 50)
                        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                    Button("关闭") { dismiss() }
                        .foregroundStyle(QuartetTheme.onAccent)
                        .frame(maxWidth: .infinity).frame(height: 50)
                        .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                }
                .font(.quartet(.control, weight: .semibold))
                .buttonStyle(.plain)
                Spacer(minLength: 0)
            }
            .padding(.horizontal, 20)
            .padding(.top, 8)
            .background(QuartetTheme.canvas)
            .navigationTitle(error.title.localizedForApp)
            .navigationBarTitleDisplayMode(.inline)
        }
    }
}

// MARK: - 思考等级联动

/// 思考等级随“Agent + 模型”变化，必须重新选一次模型才能拿到当前对的可选列表。
/// 默认参数页和 Agent 角色页都要用，所以把请求与缓存放在一处。
@MainActor
final class AgentThoughtLevelStore: ObservableObject {
    @Published private(set) var states: [String: AgentThoughtLevelState] = [:]
    @Published private(set) var loadingKeys: Set<String> = []
    @Published private(set) var errors: [String: String] = [:]

    static func key(agentType: String, modelID: String) -> String {
        "\(agentType)::\(modelID)"
    }

    func state(agentType: String, modelID: String) -> AgentThoughtLevelState? {
        states[Self.key(agentType: agentType, modelID: modelID)]
    }

    func isLoading(agentType: String, modelID: String) -> Bool {
        loadingKeys.contains(Self.key(agentType: agentType, modelID: modelID))
    }

    func error(agentType: String, modelID: String) -> String? {
        errors[Self.key(agentType: agentType, modelID: modelID)]
    }

    /// 已经有结果或正在请求的组合不会重复触发；`fallback` 是 Agent 列表里带回来的
    /// 缓存值，用于在请求返回前先渲染出一个列表。
    func load(
        agentType: String,
        modelID: String,
        fallback: AgentThoughtLevelState?,
        using model: AppModel
    ) {
        guard !agentType.isEmpty, !modelID.isEmpty else { return }
        let key = Self.key(agentType: agentType, modelID: modelID)
        guard states[key] == nil, !loadingKeys.contains(key) else { return }
        if let fallback { states[key] = fallback }
        loadingKeys.insert(key)
        errors[key] = nil
        Task { @MainActor in
            do {
                let state = try await model.relinkACPThoughtLevels(agentType: agentType, modelID: modelID)
                states[key] = state
            } catch {
                if let apiError = error as? APIError {
                    errors[key] = apiError.detail
                } else {
                    errors[key] = String(describing: error)
                }
            }
            loadingKeys.remove(key)
        }
    }
}

// MARK: - 工具

func agentSettingsPresentedError(_ caught: Error, summary: String) -> PresentedError {
    if let apiError = caught as? APIError {
        return PresentedError(title: apiError.summary, detail: apiError.detail)
    }
    return PresentedError(title: summary, detail: String(describing: caught))
}

func agentSettingsErrorDetail(_ caught: Error) -> String {
    if let apiError = caught as? APIError {
        return "\(apiError.summary)\n\(apiError.detail)"
    }
    return String(describing: caught)
}

@MainActor
func agentSettingsTimestamp(_ milliseconds: Int64) -> String {
    milliseconds.quartetDate.formatted(date: .abbreviated, time: .shortened)
}

func agentSettingsListValue(_ values: [String]) -> String {
    values.isEmpty ? "无".localizedForApp : values.joined(separator: "、")
}
