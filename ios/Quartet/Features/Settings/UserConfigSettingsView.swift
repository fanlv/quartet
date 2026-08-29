import SwiftUI

/// 与 Web 端设置的“用户配置”对齐：显示语言、头像链接、任务结束默认 Hook。
/// 显示语言是本机偏好，另外两项存在后端全局 settings 里。
@MainActor
struct UserConfigSettingsView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.scenePhase) private var scenePhase

    @State private var config = UserConfig(avatarURL: "", endHookScript: "", snapshot: [:])
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
                    endHookCard
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
                agentSettingsHint("当前账号没有 config.read 权限，无法查看头像和任务结束 Hook 配置。")
            } else if !canWrite {
                agentSettingsHint("当前账号没有 config.write 权限，只能查看头像和任务结束 Hook 配置。")
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

    private var endHookCard: some View {
        AgentSettingsCard("结束 Hook", systemImage: "bell.badge") {
            AgentSettingsTextEditor(
                title: "任务结束默认 Hook（Shell）",
                text: binding(\.endHookScript),
                identifier: "user-config-end-hook",
                hint: "任务结束时执行的默认副作用脚本，一般用来发通知。输出会被忽略，失败只打日志，不影响任务本身。"
            )
            .disabled(!canWrite)
            agentSettingsDivider()
            VStack(alignment: .leading, spacing: 6) {
                Toggle("仅在无人实时查看该任务时执行（对话）".localizedForApp, isOn: binding(\.endHookSkipWhenWatched))
                    .font(.quartet(.control, weight: .medium))
                    .foregroundStyle(QuartetTheme.primaryText)
                    .toggleStyle(QuartetCheckmarkToggleStyle())
                    .disabled(!canWrite)
                    .accessibilityIdentifier("user-config-end-hook-skip-watched")
                agentSettingsHint("对话轮次结束时，如果有页面正在实时看这个任务的输出（Web 前台标签页、iOS 前台对话页、图运行页），就不执行脚本；页面关掉、切走或退到后台后才通知。图工作流终点 Hook 不受影响。")
            }
            agentSettingsDivider()
            VStack(alignment: .leading, spacing: 6) {
                agentSettingsFieldLabel("触发点")
                agentSettingsHint("· 图工作流走到“使用全局默认脚本”的终点结点")
                agentSettingsHint("· 对话每一轮结束——完成 / 失败 / 停止都会触发")
            }
            VStack(alignment: .leading, spacing: 8) {
                agentSettingsFieldLabel("Hook 环境变量")
                AgentSettingsMonoRow(
                    label: "公共",
                    value: "$QUARTET_HOOK_SOURCE\n$QUARTET_JOB_TITLE\n$QUARTET_JOB_ID\n$QUARTET_LAST_ASSISTANT"
                )
                AgentSettingsMonoRow(
                    label: "仅图工作流",
                    value: "$QUARTET_RUN_ID\n$QUARTET_NODE_ID\n$QUARTET_NODE_TITLE\n$QUARTET_NODE_TYPE"
                )
                AgentSettingsMonoRow(
                    label: "仅对话",
                    value: "$QUARTET_SESSION_ID\n$QUARTET_JOB_MODE\n$QUARTET_JOB_STATUS\n$QUARTET_RUN_OUTCOME\n$QUARTET_ERROR_MESSAGE\n$QUARTET_JOB_WATCHED"
                )
                agentSettingsHint("$QUARTET_HOOK_SOURCE 取值：end（工作流终点）/ prompt（结点 Hook）/ interactive（对话轮次）；图工作流还会注入结点可见的业务变量。$QUARTET_JOB_WATCHED 为 1 表示本轮结束时有人正在实时查看。")
            }
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

    /// 布尔项走同一套脏值维护逻辑。
    private func binding(_ keyPath: WritableKeyPath<UserConfig, Bool>) -> Binding<Bool> {
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
                    config.endHookScript = saved.endHookScript
                    config.endHookSkipWhenWatched = saved.endHookSkipWhenWatched
                    isDirty = false
                    message = .success("用户配置已保存".localizedForApp)
                } else {
                    // 网络请求进行时仍允许继续输入；这部分不能被保存结果覆盖。
                    let currentAvatar = config.avatarURL.trimmingCharacters(in: .whitespacesAndNewlines)
                    isDirty = currentAvatar != saved.avatarURL
                        || config.endHookScript != saved.endHookScript
                        || config.endHookSkipWhenWatched != saved.endHookSkipWhenWatched
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
