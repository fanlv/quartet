import SwiftUI
import UniformTypeIdentifiers
import UIKit

@MainActor
struct CodexAccountsSettingsView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var snapshot: CodexAccountsResponse?
    @State private var scrollPosition = ScrollPosition()
    @State private var isLoading = true
    @State private var isBusy = false
    @State private var generation = 0
    @State private var errorDetail = ""
    @State private var message: AgentSettingsMessage?
    @State private var page: SheetPage?
    @State private var name = ""
    @State private var note = ""
    @State private var authText: String?
    @State private var configText: String?
    @State private var authFileName = ""
    @State private var configFileName = ""
    @State private var removeConfig = false
    @State private var applyConfig = false
    @State private var intentRevision = ""
    @State private var intentLiveRevision = ""
    @State private var showsFileImporter = false
    @State private var importingConfig = false
    @State private var fileGeneration = 0
    @State private var deleteAccount: CodexAccount?
    @State private var confirmsDelete = false
    @State private var confirmsRestore = false
    @State private var login: CodexAccountLogin?
    @State private var loginRetry = 0

    private enum SheetPage: Identifiable {
        case currentDetails(CodexAccountCurrent)
        case actions(CodexAccount)
        case form(String, CodexAccount?)
        case activate(CodexAccount, from: CodexAccountCurrent, appliesCurrentConfig: Bool)
        case login
        case error

        // Steps share one presentation, including transitions from actions to confirmation.
        var id: Int { 0 }
    }

    var body: some View {
        Group {
            if isLoading && snapshot == nil {
                AgentSettingsLoadingView(title: "正在加载 Codex 账号…")
            } else if snapshot == nil && !errorDetail.isEmpty {
                AgentSettingsLoadFailure(detail: errorDetail) { Task { await load() } }
            } else {
                content
            }
        }
        .background(QuartetTheme.canvas)
        .task(id: model.serverAddress) {
            generation += 1
            snapshot = nil
            login = nil
            message = nil
            errorDetail = ""
            clearFiles()
            page = nil
            isLoading = true
            isBusy = false
            await load()
        }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active { Task { await load() } }
        }
        .task(id: "\(login?.id ?? ""):\(scenePhase):\(loginRetry)") { await pollLogin() }
        .task(id: snapshot?.current.availability) {
            guard let availability = snapshot?.current.availability,
                  availability == "pending_validation" || availability == "validating" else { return }
            for _ in 0..<12 {
                do { try await Task.sleep(for: .seconds(2)) } catch { return }
                if scenePhase == .active { await load() }
            }
        }
        .sheet(item: $page, onDismiss: { clearFiles() }) { presentedPage in
            sheet(for: page ?? presentedPage)
        }
        .alert("恢复登录态", isPresented: $confirmsRestore) {
            Button("关闭", role: .cancel) {}
            Button("恢复登录态", role: .destructive) { Task { await mutate("restore") } }
        } message: {
            Text("将恢复最近一次切换前的登录态和配置。正在运行的 Codex 任务会阻止恢复。继续恢复？")
        }
    }

    private var content: some View {
        ScrollView {
            VStack(spacing: 16) {
                if let message { AgentSettingsMessageView(message) }
                if let current = snapshot?.current { currentCard(current) }
                if let snapshot { savedAccounts(snapshot) }
                AgentSettingsCard {
                    action("导入账号", icon: "square.and.arrow.down", id: "codex-account-import") { openForm("import") }
                    agentSettingsDivider()
                    action(login?.status == "running" ? "设备码登录" : "登录新账号", icon: "person.badge.plus", id: "codex-account-login") {
                        if login?.status == "running" { page = .login; loginRetry += 1 }
                        else { openForm("login") }
                    }
                    agentSettingsDivider()
                    action("保存本机当前账号", icon: "tray.and.arrow.down", id: "codex-account-capture") { openForm("capture") }
                        .disabled(snapshot?.current.hasAuth != true)
                    if snapshot?.hasBackup == true {
                        agentSettingsDivider()
                        action("恢复上次切换前状态", icon: "arrow.uturn.backward", id: "codex-account-restore") { freezeIntent(); confirmsRestore = true }
                    }
                }
            }
            .padding(18)
        }
        .scrollPosition($scrollPosition)
        .refreshable { await load() }
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button { Task { await load() } } label: { Image(systemName: "arrow.clockwise") }
                    .buttonStyle(.plain)
                    .font(.quartet(.control))
                    .frame(minHeight: 44)
                    .foregroundStyle(QuartetTheme.accent)
                    .disabled(isBusy)
                    .accessibilityLabel("刷新状态".localizedForApp)
                    .accessibilityIdentifier("codex-account-refresh")
            }
            .sharedBackgroundVisibility(.hidden)
        }
    }

    private func currentCard(_ current: CodexAccountCurrent) -> some View {
        let errors = [current.error, current.blockedReason, current.validationError].compactMap { $0 }
        return AgentSettingsCard("当前 Codex 账号", systemImage: "person.crop.circle") {
            HStack(spacing: 8) {
                VStack(alignment: .leading, spacing: 4) {
                    currentIdentity(current)
                    if current.status == "external" { agentSettingsHint(statusTitle(current.status)) }
                    if !current.availability.isEmpty {
                        agentSettingsHint(availabilityTitle(current.availability))
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                iconButton("info.circle", label: "状态详情".localizedForApp, id: "codex-account-current-details") {
                    page = .currentDetails(current)
                }
            }
            if !errors.isEmpty {
                errorButton(errors.joined(separator: "\n\n"), title: "查看错误详情".localizedForApp)
            }
        }
    }

    private func savedAccounts(_ snapshot: CodexAccountsResponse) -> some View {
        AgentSettingsCard("已保存的账号") {
            if snapshot.accounts.isEmpty {
                agentSettingsHint("登录、导入或保存当前账号后，可在这里切换。")
            } else {
                VStack(spacing: 6) {
                    ForEach(Array(snapshot.accounts.enumerated()), id: \.element.id) { index, account in
                        if index > 0 { agentSettingsDivider() }
                        accountRow(account, snapshot: snapshot)
                    }
                }
            }
        }
    }

    private func accountRow(_ account: CodexAccount, snapshot: CodexAccountsResponse) -> some View {
        let isCurrent = account.id == snapshot.current.managedId
        let sharesNameAndEmail = snapshot.accounts.contains {
            $0.id != account.id && $0.name == account.name && $0.email == account.email
        }
        let layout = dynamicTypeSize.isAccessibilitySize
            ? AnyLayout(VStackLayout(alignment: .leading, spacing: 0))
            : AnyLayout(HStackLayout(spacing: 8))

        return layout {
            accountIdentity(account, showsWorkspace: sharesNameAndEmail)
                .frame(maxWidth: .infinity, alignment: .leading)
            HStack(spacing: 4) {
                if isCurrent {
                    Label("当前使用", systemImage: "checkmark")
                        .font(.quartet(.detail, weight: .semibold))
                        .foregroundStyle(QuartetTheme.accent)
                        .accessibilityIdentifier("codex-account-active-\(account.id)")
                } else {
                    Button {
                        openActivation(account, from: snapshot.current)
                    } label: {
                        Text("切换")
                            .font(.quartet(.control, weight: .semibold))
                            .foregroundStyle(QuartetTheme.accent)
                            .padding(.horizontal, 12)
                            .padding(.vertical, 6)
                            .background(QuartetTheme.accent.opacity(0.12), in: RoundedRectangle(cornerRadius: 10))
                            .frame(minWidth: 44, minHeight: 44)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .disabled(isBusy)
                    .accessibilityLabel(AppLanguage.localizedFormat("切换到 %@", accountTitle(account)))
                    .accessibilityIdentifier("codex-account-switch-\(account.id)")
                }
                iconButton(
                    "ellipsis",
                    label: AppLanguage.localizedFormat("%@ 的账号操作", accountTitle(account)),
                    id: "codex-account-actions-\(account.id)"
                ) {
                    freezeIntent()
                    page = .actions(account)
                }
            }
            .fixedSize(horizontal: true, vertical: false)
        }
    }

    @ViewBuilder
    private func currentIdentity(_ current: CodexAccountCurrent, showsWorkspace: Bool = false) -> some View {
        if let account = current.account {
            accountIdentity(account, showsWorkspace: showsWorkspace)
        } else {
            identityTitle(statusTitle(current.status).localizedForApp)
        }
    }

    private func accountIdentity(_ account: CodexAccount, showsWorkspace: Bool = false) -> some View {
        let title = accountTitle(account)
        return VStack(alignment: .leading, spacing: 4) {
            identityTitle(title)
            if let email = account.email, !email.isEmpty, email.caseInsensitiveCompare(title) != .orderedSame {
                agentSettingsHint(email)
            }
            if showsWorkspace {
                if !account.note.isEmpty { agentSettingsHint(account.note) }
                if let workspace = account.workspaceId { AgentSettingsMonoRow(label: "ChatGPT 工作空间", value: workspace) }
            }
        }
    }

    private func accountTitle(_ account: CodexAccount) -> String {
        if !account.name.isEmpty { return account.name }
        if let email = account.email, !email.isEmpty { return email }
        return (account.authType == "api_key" ? "API Key 登录" : "ChatGPT 登录").localizedForApp
    }

    private func identityTitle(_ title: String) -> some View {
        Text(title)
            .font(.quartet(.control, weight: .semibold))
            .foregroundStyle(QuartetTheme.primaryText)
            .fixedSize(horizontal: false, vertical: true)
    }

    private func currentDetails(_ current: CodexAccountCurrent) -> some View {
        AgentSettingsCard {
            currentIdentity(current)
            if current.account != nil { agentSettingsHint(statusTitle(current.status)) }
            AgentSettingsMonoRow(label: "目录", value: current.home)
            detail("auth.json: \((current.hasAuth ? "已存在" : "缺失").localizedForApp) · config.toml: \((current.hasConfig ? "已存在" : "缺失").localizedForApp)")
            detail("\("Agent 可用性".localizedForApp): \(availabilityTitle(current.availability).localizedForApp)")
            agentSettingsHint("账号库由此 Quartet 后端共享。切换影响后续 Codex 执行；请先退出使用同一目录的外部 Codex CLI 或 IDE 会话。")
        }
    }

    private func accountDetails(_ account: CodexAccount) -> some View {
        AgentSettingsCard("账号详情") {
            accountIdentity(account, showsWorkspace: true)
            agentSettingsHint(account.authType == "api_key" ? "API Key 登录" : "ChatGPT 登录")
            agentSettingsHint(account.hasConfig ? "附带 config.toml" : "仅登录态")
            if let selectedModel = account.model { detail(selectedModel) }
            if let provider = account.provider { detail(provider) }
            detail("\("更新时间".localizedForApp): \(formattedDate(account.updatedAt))")
            detail("\("最近使用".localizedForApp): \(account.lastUsedAt.map(formattedDate) ?? "尚未使用".localizedForApp)")
        }
    }

    private func activationContent(_ account: CodexAccount, from current: CodexAccountCurrent, appliesCurrentConfig: Bool) -> some View {
        let showsWorkspace = current.account.map {
            accountTitle($0) == accountTitle(account) || ($0.email != nil && $0.email == account.email)
        } ?? false

        return AgentSettingsCard {
            if appliesCurrentConfig {
                accountIdentity(account)
            } else {
                VStack(alignment: .leading, spacing: 4) {
                    agentSettingsFieldLabel("当前账号")
                    currentIdentity(current, showsWorkspace: showsWorkspace)
                }
                agentSettingsDivider()
                VStack(alignment: .leading, spacing: 4) {
                    agentSettingsFieldLabel("目标账号")
                    accountIdentity(account, showsWorkspace: showsWorkspace)
                }
                agentSettingsHint("切换后，此服务器新启动的 Codex 任务会使用所选账号。")
            }
            if account.hasConfig && !appliesCurrentConfig {
                agentSettingsDivider()
                Toggle("同时应用此账号的配置", isOn: $applyConfig)
                    .toggleStyle(QuartetCheckmarkToggleStyle())
                    .font(.quartet(.control))
                    .frame(minHeight: 44)
                    .disabled(isBusy)
                    .accessibilityIdentifier("codex-account-apply-config")
            }
            agentSettingsHint(applyConfig ? "将整份替换用户级配置，包括模型、MCP 和权限设置。" : "仅应用此账号的登录状态。")
            primaryButton(
                appliesCurrentConfig ? "应用账号配置" : "确认切换",
                id: "codex-account-confirm-switch",
                busyTitle: appliesCurrentConfig ? "正在处理…" : "正在切换账号…"
            ) {
                Task { await mutate("activate", account: account, appliesCurrentConfig: appliesCurrentConfig) }
            }
        }
    }

    @ViewBuilder private func sheet(for page: SheetPage) -> some View {
        if case .error = page {
            AgentSettingsErrorSheet(error: PresentedError(title: "操作失败", detail: errorDetail))
                .quartetSheetStyle()
        } else {
            let title = sheetTitle(for: page)
            NavigationStack {
                ScrollView {
                    VStack(spacing: 16) {
                        switch page {
                        case .currentDetails(let current):
                            currentDetails(current)
                        case .actions(let account):
                            AgentSettingsCard {
                                if let current = snapshot?.current, current.managedId == account.id, account.hasConfig {
                                    action("应用账号配置", icon: "slider.horizontal.3", id: "codex-account-activate") {
                                        openActivation(account, from: current, appliesCurrentConfig: true)
                                    }
                                    agentSettingsDivider()
                                }
                                action("编辑与更新文件", icon: "square.and.pencil", id: "codex-account-edit") { openForm("update", account: account) }
                                agentSettingsDivider()
                                action("删除", icon: "trash", id: "codex-account-delete") { deleteAccount = account; confirmsDelete = true }
                            }
                            accountDetails(account)
                        case .form(let mode, let account):
                            formContent(mode, account: account)
                        case .activate(let account, let current, let appliesCurrentConfig):
                            activationContent(account, from: current, appliesCurrentConfig: appliesCurrentConfig)
                        case .login: loginContent
                        case .error: EmptyView()
                        }
                    }
                    .padding(18)
                    .animation(reduceMotion ? nil : .snappy, value: title)
                }
                .background(QuartetTheme.canvas)
                .quartetNavigationTitle(title)
                .toolbar {
                    ToolbarItem(placement: .topBarTrailing) {
                        Button { self.page = nil } label: { Image(systemName: "xmark") }
                            .buttonStyle(.plain).font(.quartet(.control)).foregroundStyle(QuartetTheme.accent)
                            .frame(minHeight: 44).disabled(isBusy)
                            .accessibilityLabel("关闭".localizedForApp)
                            .accessibilityIdentifier("codex-account-close")
                    }
                    .sharedBackgroundVisibility(.hidden)
                }
            }
            .quartetSheetStyle()
            .interactiveDismissDisabled(isBusy)
            .alert("删除", isPresented: $confirmsDelete) {
                Button("关闭", role: .cancel) {}
                Button("删除", role: .destructive) {
                    if let account = deleteAccount { Task { await mutate("delete", account: account) } }
                }
            } message: {
                Text(LocalizedStringKey(snapshot?.current.managedId == deleteAccount?.id
                     ? "从管理中移除此账号？本机登录将保留，状态变为未纳管。"
                     : "删除此账号记录及保存的文件？本机当前登录不会改变。"))
            }
            .fileImporter(isPresented: $showsFileImporter, allowedContentTypes: [.item], allowsMultipleSelection: false) { result in
                Task { await readFile(result) }
            }
        }
    }

    private func formContent(_ mode: String, account: CodexAccount?) -> some View {
        AgentSettingsCard {
            AgentSettingsTextField(title: "账号名称", text: $name, identifier: "codex-account-name")
            AgentSettingsTextField(title: "备注", text: $note, identifier: "codex-account-note")
            if mode == "import" || mode == "update" {
                action("选择 auth.json", icon: "doc", id: "codex-account-auth-file") { importingConfig = false; showsFileImporter = true }
                if !authFileName.isEmpty { detail(authFileName) }
                action("选择 config.toml", icon: "doc.text", id: "codex-account-config-file") { importingConfig = true; showsFileImporter = true }
                if !configFileName.isEmpty { detail(configFileName) }
                if mode == "update", account?.hasConfig == true {
                    Toggle("从此账号移除保存的 config.toml", isOn: $removeConfig)
                        .toggleStyle(QuartetCheckmarkToggleStyle())
                        .font(.quartet(.detail))
                        .disabled(configText != nil)
                        .accessibilityIdentifier("codex-account-remove-config")
                }
                agentSettingsHint(mode == "import" ? "文件仅保存在后端账号库，导入不会切换当前账号。" : "未选择的文件保持原样。当前账号的文件更新会先检查占用，并按当前生效方式应用。")
            } else if mode == "capture" {
                agentSettingsHint("读取当前连接的 Quartet 后端机器中的登录态与配置，不读取浏览器或手机的本地目录。")
            } else {
                agentSettingsHint("将在独立目录启动 Codex 官方设备码登录。完成后自动保存新账号，不影响当前登录。")
            }
            primaryButton(mode == "login" ? "开始设备码登录" : "保存账号", id: "codex-account-save") {
                Task { if mode == "login" { await startLogin() } else { await mutate(mode, account: account) } }
            }
            .disabled(name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || mode == "import" && authText == nil)
        }
        .disabled(isBusy)
    }

    private var loginContent: some View {
        AgentSettingsCard("设备码登录", systemImage: "key") {
            if let login {
                agentSettingsHint(login.status == "running" ? "关闭后可稍后返回继续登录" : login.status == "saved" ? "登录完成，新账号已保存。" : login.status == "cancelled" ? "本次设备码登录已取消。" : "登录失败，完整错误如下。")
                Text(login.output).font(.quartet(.detail, design: .monospaced)).foregroundStyle(QuartetTheme.primaryText).textSelection(.enabled)
                ForEach(loginLinks(login.output), id: \.absoluteString) { url in
                    Link(destination: url) {
                        Text(url.absoluteString).font(.quartet(.detail)).foregroundStyle(QuartetTheme.accent)
                    }.frame(minHeight: 44).accessibilityIdentifier("codex-account-login-link")
                }
                if let error = login.error { errorButton(error) }
                action("复制登录输出", icon: "doc.on.doc", id: "codex-account-copy-login") { UIPasteboard.general.string = login.output }
                if login.status == "running" {
                    action("取消本次登录", icon: "stop.circle", id: "codex-account-cancel-login") { Task { await cancelLogin() } }
                    action("重试", icon: "arrow.clockwise", id: "codex-account-retry-login") { loginRetry += 1 }
                }
            }
        }
    }

    private func sheetTitle(for page: SheetPage) -> String {
        switch page {
        case .currentDetails: "状态详情"
        case .actions: "账号操作"
        case .activate(_, _, let appliesCurrentConfig): appliesCurrentConfig ? "应用账号配置" : "切换账号"
        case .login: "设备码登录"
        case .error: "操作失败"
        case .form(let mode, _): mode == "login" ? "登录新账号" : mode == "capture" ? "保存本机当前账号" : mode == "update" ? "编辑账号" : "导入账号"
        }
    }

    private func detail(_ value: String) -> some View {
        Text(value).font(.quartet(.detail)).foregroundStyle(QuartetTheme.secondaryText).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading)
    }

    private func action(_ title: String, icon: String, id: String, perform: @escaping () -> Void) -> some View {
        Button(action: perform) {
            Label(title.localizedForApp, systemImage: icon)
                .font(.quartet(.control, weight: .medium)).foregroundStyle(QuartetTheme.accent)
                .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading).contentShape(Rectangle())
        }.buttonStyle(.plain).disabled(isBusy).accessibilityIdentifier(id)
    }

    private func iconButton(_ systemImage: String, label: String, id: String, perform: @escaping () -> Void) -> some View {
        Button(action: perform) {
            Image(systemName: systemImage)
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(QuartetTheme.secondaryText)
                .frame(minWidth: 44, minHeight: 44)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(isBusy)
        .accessibilityLabel(label)
        .accessibilityIdentifier(id)
    }

    private func primaryButton(_ title: String, id: String, busyTitle: String = "正在处理…", perform: @escaping () -> Void) -> some View {
        Button(action: perform) {
            HStack {
                if isBusy { ProgressView().tint(QuartetTheme.onAccent) }
                Text(LocalizedStringKey(isBusy ? busyTitle : title))
            }
            .font(.quartet(.control, weight: .semibold))
            .foregroundStyle(QuartetTheme.onAccent)
            .frame(maxWidth: .infinity, minHeight: 48)
            .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 14))
        }.buttonStyle(.plain).disabled(isBusy).accessibilityIdentifier(id)
    }

    private func errorButton(_ detail: String, title: String? = nil) -> some View {
        Button { showError(detail) } label: {
            Label(title ?? detail, systemImage: "exclamationmark.triangle")
                .font(.quartet(.detail))
                .foregroundStyle(QuartetTheme.failed)
                .multilineTextAlignment(.leading)
        }.buttonStyle(.plain).frame(minHeight: 44).accessibilityIdentifier("codex-account-error")
    }

    private func showError(_ detail: String) {
        if case .error? = page {
            if !errorDetail.contains(detail) { errorDetail += "\n\n" + detail }
        } else {
            errorDetail = detail
        }
        page = .error
    }

    private func freezeIntent() {
        intentRevision = snapshot?.revision ?? ""
        intentLiveRevision = snapshot?.liveRevision ?? ""
    }

    private func openForm(_ mode: String, account: CodexAccount? = nil) {
        clearFiles(); freezeIntent()
        name = account?.name ?? ""; note = account?.note ?? ""
        page = .form(mode, account)
    }

    private func openActivation(_ account: CodexAccount, from current: CodexAccountCurrent, appliesCurrentConfig: Bool = false) {
        clearFiles()
        freezeIntent()
        applyConfig = appliesCurrentConfig
        page = .activate(account, from: current, appliesCurrentConfig: appliesCurrentConfig)
    }

    private func clearFiles() {
        fileGeneration += 1
        authText = nil; configText = nil; authFileName = ""; configFileName = ""; removeConfig = false
    }

    private func load() async {
        guard !isBusy else { return }
        generation += 1
        let requestGeneration = generation
        let server = model.serverAddress
        do {
            let result = try await model.apiClient().codexAccounts()
            guard requestGeneration == generation, server == model.serverAddress, !Task.isCancelled else { return }
            if let activeLogin = result.activeLogin { login = activeLogin }
            snapshot = result; isLoading = false
        } catch {
            guard requestGeneration == generation, server == model.serverAddress, !Task.isCancelled else { return }
            isLoading = false
            let detail = (error as? APIError)?.detail ?? String(describing: error)
            if snapshot != nil { showError(detail) }
            else { errorDetail = detail }
        }
    }

    private func mutate(_ action: String, account: CodexAccount? = nil, appliesCurrentConfig: Bool = false) async {
        guard !isBusy, let snapshot else { return }
        isBusy = true; generation += 1
        let server = model.serverAddress
        let operationGeneration = generation
        let body = CodexAccountMutation(
            revision: intentRevision.isEmpty ? snapshot.revision : intentRevision,
            liveRevision: intentLiveRevision.isEmpty ? snapshot.liveRevision : intentLiveRevision,
            name: name, note: note, authJson: authText, configToml: configText,
            removeConfig: removeConfig, applyConfig: action == "activate" && applyConfig
        )
        do {
            let result = try await model.apiClient().mutateCodexAccount(action: action, accountID: account?.id, body: body)
            guard server == model.serverAddress, operationGeneration == generation else { return }
            self.snapshot = result
            if action == "activate" || action == "restore" || action == "update" && snapshot.current.managedId == account?.id && (authText != nil || configText != nil || removeConfig) {
                model.codexAccountDidChange()
                Task { await model.refreshAgentCatalog() }
            }
            if action == "activate", let account, !appliesCurrentConfig {
                let confirmation = AppLanguage.localizedFormat("已切换到 %@。", accountTitle(account))
                message = .success([confirmation, result.warning].compactMap { $0 }.joined(separator: "\n"))
            } else {
                message = .success(result.warning ?? (action == "activate" || action == "restore" ? "文件已应用，Agent 可用性将重新检查。" : "账号记录已保存。").localizedForApp)
            }
            page = nil
            if action == "activate" || action == "restore" {
                scrollPosition.scrollTo(edge: .top)
            }
            clearFiles()
        } catch {
            guard server == model.serverAddress, operationGeneration == generation else { return }
            showError((error as? APIError)?.detail ?? String(describing: error))
        }
        guard server == model.serverAddress, operationGeneration == generation else { return }
        isBusy = false
        await load()
    }

    private func readFile(_ result: Result<[URL], Error>) async {
        let server = model.serverAddress
        let isConfig = importingConfig
        let requestFileGeneration = fileGeneration
        do {
            guard let url = try result.get().first else { return }
            let value = try await Task.detached(priority: .userInitiated) {
                let scoped = url.startAccessingSecurityScopedResource()
                defer { if scoped { url.stopAccessingSecurityScopedResource() } }
                let data = try Data(contentsOf: url)
                guard let text = String(data: data, encoding: .utf8) else {
                    throw APIError(summary: "文件不是有效的 UTF-8 文本", detail: "\("文件不是有效的 UTF-8 文本".localizedForApp)\n\n\(url.path)")
                }
                return text
            }.value
            guard server == model.serverAddress, requestFileGeneration == fileGeneration, page != nil else { return }
            if isConfig { configText = value; configFileName = url.lastPathComponent; removeConfig = false }
            else { authText = value; authFileName = url.lastPathComponent }
        } catch {
            guard server == model.serverAddress, requestFileGeneration == fileGeneration, page != nil else { return }
            showError((error as? APIError)?.detail ?? String(describing: error))
        }
    }

    private func startLogin() async {
        guard !isBusy else { return }
        let server = model.serverAddress
        isBusy = true
        do {
            let result = try await model.apiClient().startCodexAccountLogin(CodexAccountMutation(name: name, note: note))
            guard server == model.serverAddress else { return }
            login = result.login
            if page != nil { page = .login }
            clearFiles()
        } catch {
            guard server == model.serverAddress else { return }
            showError((error as? APIError)?.detail ?? String(describing: error))
        }
        if server == model.serverAddress { isBusy = false }
    }

    private func pollLogin() async {
        guard let id = login?.id, scenePhase == .active else { return }
        let server = model.serverAddress
        while !Task.isCancelled, server == model.serverAddress, login?.status == "running" {
            do {
                try await Task.sleep(for: .seconds(1.2))
                guard scenePhase == .active else { continue }
                let result = try await model.apiClient().codexAccountLogin(id: id)
                guard server == model.serverAddress, !Task.isCancelled else { return }
                login = result.login
                if result.login.status == "saved" { message = .success("新账号已保存，可在列表中选择切换。".localizedForApp); await load() }
            } catch {
                guard server == model.serverAddress, !Task.isCancelled else { return }
                showError((error as? APIError)?.detail ?? String(describing: error))
                return
            }
        }
    }

    private func cancelLogin() async {
        guard let login, !isBusy else { return }
        let server = model.serverAddress
        do { try await model.apiClient().cancelCodexAccountLogin(id: login.id) }
        catch {
            guard server == model.serverAddress else { return }
            showError((error as? APIError)?.detail ?? String(describing: error))
        }
    }

    private func formattedDate(_ value: Int64) -> String {
        Date(timeIntervalSince1970: Double(value) / 1000).formatted(date: .abbreviated, time: .shortened)
    }

    private func loginLinks(_ output: String) -> [URL] {
        guard let detector = try? NSDataDetector(types: NSTextCheckingResult.CheckingType.link.rawValue) else { return [] }
        var seen = Set<String>()
        return detector.matches(in: output, range: NSRange(output.startIndex..., in: output)).compactMap { match in
            guard let url = match.url, ["http", "https"].contains(url.scheme?.lowercased() ?? ""), seen.insert(url.absoluteString).inserted else { return nil }
            return url
        }
    }

    private func statusTitle(_ status: String) -> String {
        switch status {
        case "managed": "已纳管"
        case "external": "本机登录已在外部变更"
        case "invalid": "本机文件无效"
        case "missing": "未登录或文件缺失"
        default: "未纳管"
        }
    }

    private func availabilityTitle(_ value: String) -> String {
        switch value {
        case "available": "可用"
        case "validating": "检查中"
        case "needs_authentication": "需要登录"
        case "unavailable": "不可用"
        default: "待检查"
        }
    }
}
