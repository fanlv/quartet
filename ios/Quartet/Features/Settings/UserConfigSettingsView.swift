import SwiftUI

/// 与 Web 端设置的“用户配置”对齐：显示语言、头像链接、图工作流终点默认 Hook。
/// 显示语言是本机偏好，另外两项存在后端全局 settings 里。
@MainActor
struct UserConfigSettingsView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.scenePhase) private var scenePhase

    @State private var config = UserConfig(avatarURL: "", graphEndHookScript: "", snapshot: [:])
    @State private var isLoading = true
    @State private var hasLoaded = false
    @State private var loadError = ""
    @State private var isSaving = false
    @State private var isDirty = false
    @State private var message: AgentSettingsMessage?
    @State private var showsLanguagePicker = false
    @State private var editRevision: UInt64 = 0
    @State private var loadRevision: UInt64 = 0

    private var canRead: Bool { model.can("config.read") }
    private var canWrite: Bool { model.can("config.write") }

    var body: some View {
        Group {
            if canRead && isLoading && !hasLoaded {
                AgentSettingsLoadingView(title: "正在加载用户配置…")
            } else if canRead && !hasLoaded && !loadError.isEmpty {
                AgentSettingsLoadFailure(detail: loadError) { Task { await load() } }
            } else {
                editor
            }
        }
        .background(QuartetTheme.canvas)
        .task { await initialLoad() }
        .onChange(of: scenePhase) { _, phase in
            guard phase == .active, hasLoaded, !isDirty, !isSaving else { return }
            Task { await load() }
        }
        .sheet(isPresented: $showsLanguagePicker) {
            LanguagePickerSheet(selection: model.appLanguage) { language in
                model.setAppLanguage(language)
                showsLanguagePicker = false
            }
            .presentationDetents([.height(270)])
            .quartetSheetStyle()
        }
    }

    private var editor: some View {
        ScrollView {
            LazyVStack(spacing: 12) {
                userConfigCard
                if !loadError.isEmpty {
                    AgentSettingsMessageView(kind: .failure, text: loadError)
                }
                if canRead {
                    avatarCard
                    graphEndHookCard
                }
            }
            .padding(.horizontal, 18)
            .padding(.vertical, 12)
        }
        .scrollDismissesKeyboard(.interactively)
        .safeAreaInset(edge: .bottom, spacing: 0) {
            if canRead && canWrite {
                AgentSettingsSaveBar(
                    title: "保存用户配置",
                    savingTitle: "正在保存…",
                    isSaving: isSaving,
                    isEnabled: isDirty && !isLoading,
                    message: message,
                    identifier: "user-config-save",
                    action: { save() }
                )
            } else if let message {
                AgentSettingsMessageView(message)
                    .padding(.horizontal, 18)
                    .padding(.vertical, 10)
                    .background(.ultraThinMaterial)
                    .accessibilityIdentifier("user-config-feedback")
            }
        }
    }

    private var userConfigCard: some View {
        AgentSettingsCard("用户配置", systemImage: "person.crop.circle") {
            agentSettingsHint("与 Web 端设置的“用户配置”是同一份配置。显示语言只作用于本机，头像和 Hook 脚本对所有客户端生效。")
            AgentSettingsSelectionRow(
                title: "显示语言",
                value: model.appLanguage.localizationKey,
                identifier: "user-config-language"
            ) { showsLanguagePicker = true }
            if !canRead {
                agentSettingsHint("当前账号没有 config.read 权限，无法查看头像和图工作流 Hook 配置。")
            } else if !canWrite {
                agentSettingsHint("当前账号没有 config.write 权限，只能查看头像和图工作流 Hook 配置。")
            }
        }
    }

    private var avatarCard: some View {
        AgentSettingsCard("头像", systemImage: "person.crop.square") {
            AgentSettingsTextField(
                title: "头像链接",
                text: binding(\.avatarURL),
                identifier: "user-config-avatar-url",
                placeholder: "https://example.com/avatar.png",
                monospaced: true
            )
            .disabled(!canWrite)
            agentSettingsHint("用户头像的图片链接，留空则使用默认头像。")
        }
    }

    private var graphEndHookCard: some View {
        AgentSettingsCard("图工作流", systemImage: "point.3.connected.trianglepath.dotted") {
            AgentSettingsTextEditor(
                title: "图工作流终点默认 Hook（Shell）",
                text: binding(\.graphEndHookScript),
                identifier: "user-config-graph-end-hook",
                hint: "图工作流到达“使用全局默认脚本”的终点结点时执行的默认副作用脚本。输出会被忽略，失败只打日志。可用环境变量：$QUARTET_JOB_TITLE、$QUARTET_JOB_ID、$QUARTET_RUN_ID、$QUARTET_NODE_ID、$QUARTET_NODE_TITLE、$QUARTET_NODE_TYPE、$QUARTET_LAST_ASSISTANT。"
            )
            .disabled(!canWrite)
        }
    }

    // MARK: - 编辑

    /// 编辑走自定义 Binding，顺手维护 `isDirty` 并清掉上一次的保存提示；
    /// 读取时直接改 `config`，不会被误判成脏数据。
    private func binding(_ keyPath: WritableKeyPath<UserConfig, String>) -> Binding<String> {
        Binding(
            get: { config[keyPath: keyPath] },
            set: { newValue in
                guard config[keyPath: keyPath] != newValue else { return }
                config[keyPath: keyPath] = newValue
                editRevision &+= 1
                isDirty = true
                message = nil
            }
        )
    }

    // MARK: - 读取与保存

    private func initialLoad() async {
        guard canRead, !hasLoaded else {
            isLoading = false
            return
        }
        await load()
    }

    private func load() async {
        loadRevision &+= 1
        let requestRevision = loadRevision
        let startingEditRevision = editRevision
        isLoading = true
        loadError = ""
        message = nil
        do {
            let loaded = try await model.userConfig()
            guard requestRevision == loadRevision else { return }
            if editRevision == startingEditRevision, !isDirty {
                config = loaded
                isDirty = false
            } else {
                // 刷新期间用户可能已经继续输入。只更新保存基线，保留屏幕上的新草稿。
                config.snapshot = loaded.snapshot
            }
            hasLoaded = true
        } catch {
            guard requestRevision == loadRevision else { return }
            loadError = agentSettingsErrorDetail(error)
        }
        if requestRevision == loadRevision {
            isLoading = false
        }
    }

    private func save() {
        guard canWrite else { return }
        // 头像链接按 URL 处理，去掉首尾空白；Hook 脚本原样保存，空白在 shell 里有意义。
        var payload = config
        payload.avatarURL = payload.avatarURL.trimmingCharacters(in: .whitespacesAndNewlines)
        let submittedEditRevision = editRevision
        isSaving = true
        message = nil
        Task { @MainActor in
            do {
                let saved = try await model.saveUserConfig(payload)
                config.snapshot = saved.snapshot

                if editRevision == submittedEditRevision {
                    config.avatarURL = saved.avatarURL
                    config.graphEndHookScript = saved.graphEndHookScript
                    isDirty = false
                    message = .success("用户配置已保存".localizedForApp)
                } else {
                    // 网络请求进行时仍允许继续输入；这部分不能被保存结果覆盖。
                    let currentAvatar = config.avatarURL.trimmingCharacters(in: .whitespacesAndNewlines)
                    isDirty = currentAvatar != saved.avatarURL
                        || config.graphEndHookScript != saved.graphEndHookScript
                    message = .success(
                        isDirty
                            ? "用户配置已保存；保存期间的新修改尚未保存".localizedForApp
                            : "用户配置已保存".localizedForApp
                    )
                }
                loadError = ""
            } catch {
                message = .failure(agentSettingsErrorDetail(error))
            }
            isSaving = false
        }
    }
}
