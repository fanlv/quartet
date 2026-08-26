import SwiftUI

struct SettingsView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.mainTabBarInset) private var mainTabBarInset
    @Environment(\.locale) private var locale
    @State private var confirmsClear = false
    @State private var confirmsRestartWeb = false
    @State private var showsRestartSuccess = false
    @State private var showsLanguagePicker = false

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 24) {
                    VStack(alignment: .leading, spacing: 10) {
                        Text("服务连接")
                            .font(.quartet(.detail, weight: .bold))
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .padding(.horizontal, 4)

                        VStack(spacing: 0) {
                            connectionStatusRow
                            serviceInfoDivider
                            serverAddressRow
                            serviceInfoDivider
                            serviceInfoRow(
                                title: "服务端编译时间",
                                value: formattedServerBuildTime,
                                identifier: "settings-server-build-time"
                            )
                            serviceInfoDivider
                            serviceInfoRow(
                                title: "最后成功同步",
                                value: formattedLastSuccessfulSyncTime,
                                identifier: "settings-last-successful-sync"
                            )
                        }
                        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                        .overlay {
                            RoundedRectangle(cornerRadius: 18, style: .continuous)
                                .stroke(QuartetTheme.divider.opacity(0.8))
                        }
                    }

                    if model.can("agent.read") {
                        VStack(alignment: .leading, spacing: 10) {
                            Text("Agent 管理")
                                .font(.quartet(.detail, weight: .bold))
                                .foregroundStyle(QuartetTheme.secondaryText)
                                .padding(.horizontal, 4)

                            VStack(spacing: 0) {
                                agentManagementLink(
                                    title: "安装与升级",
                                    icon: "arrow.down.circle",
                                    identifier: "settings-agent-catalog"
                                ) { AgentCatalogSettingsView() }
                                if model.can("config.read") {
                                    settingsDivider
                                    agentManagementLink(
                                        title: "环境变量",
                                        icon: "key",
                                        identifier: "settings-agent-environment"
                                    ) { AgentEnvSettingsView() }
                                    settingsDivider
                                    agentManagementLink(
                                        title: "默认参数",
                                        icon: "slider.horizontal.3",
                                        identifier: "settings-agent-defaults"
                                    ) { AgentDefaultsSettingsView() }
                                    settingsDivider
                                    agentManagementLink(
                                        title: "角色分工",
                                        icon: "person.2.badge.gearshape",
                                        identifier: "settings-agent-roles"
                                    ) { AgentRoleSettingsView() }
                                }
                            }
                            .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18))
                            .overlay(RoundedRectangle(cornerRadius: 18).stroke(QuartetTheme.divider))
                        }
                    }

                    VStack(spacing: 0) {
                        Button { showsLanguagePicker = true } label: {
                            HStack(spacing: 14) {
                                Image(systemName: "globe").frame(width: 22)
                                Text("显示语言")
                                Spacer()
                                Text(LocalizedStringKey(model.appLanguage.localizationKey))
                                    .foregroundStyle(QuartetTheme.secondaryText)
                                Image(systemName: "chevron.right")
                                    .font(.caption.weight(.bold))
                                    .foregroundStyle(QuartetTheme.secondaryText)
                            }
                            .foregroundStyle(QuartetTheme.primaryText)
                            .padding(.horizontal, 16)
                            .frame(height: 54)
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("settings-language")
                    }
                    .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18))
                    .overlay(RoundedRectangle(cornerRadius: 18).stroke(QuartetTheme.divider))

                    VStack(spacing: 0) {
                        Button { confirmsRestartWeb = true } label: {
                            settingsRow(
                                model.isRestartingWeb ? "正在重启 Web..." : "重启 Web",
                                icon: "arrow.clockwise",
                                loading: model.isRestartingWeb
                            )
                        }
                        .disabled(model.isRestartingWeb)
                        .accessibilityIdentifier("settings-restart-web")
                        Divider().overlay(QuartetTheme.divider).padding(.leading, 54)
                        Button { model.editConnection() } label: {
                            settingsRow("重新配置连接", icon: "network")
                        }
                        .accessibilityIdentifier("settings-edit-connection")
                        Divider().overlay(QuartetTheme.divider).padding(.leading, 54)
                        Button(role: .destructive) { confirmsClear = true } label: {
                            settingsRow("退出并清除连接", icon: "trash", destructive: true)
                        }
                    }
                    .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18))
                    .overlay(RoundedRectangle(cornerRadius: 18).stroke(QuartetTheme.divider))

                    Text("Sophia 仅在可访问后端的局域网内工作。应用进入后台后不保证持续接收运行事件，重新打开时会同步最新状态。")
                        .font(.footnote)
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .lineSpacing(3)
                }
                .padding(20)
            }
            .background(QuartetTheme.canvas)
            .mainTabBarBottomInset(mainTabBarInset)
            .navigationTitle("设置")
        }
        .toolbarBackground(QuartetTheme.canvas, for: .navigationBar)
        .toolbarBackground(.visible, for: .navigationBar)
        .sheet(isPresented: $showsLanguagePicker) {
            LanguagePickerSheet(selection: model.appLanguage) { language in
                model.setAppLanguage(language)
                showsLanguagePicker = false
            }
            .presentationDetents([.height(270)])
            .quartetSheetStyle()
        }
        .alert("清除当前连接？", isPresented: $confirmsClear) {
            Button("关闭", role: .cancel) {}
            Button("清除连接", role: .destructive) { model.clearConnection() }
        } message: {
            Text("服务地址和本机 Cookie 登录状态都会被删除。")
        }
        .alert("重启 Web？", isPresented: $confirmsRestartWeb) {
            Button("关闭", role: .cancel) {}
            Button("重启 Web", role: .destructive) {
                Task {
                    do {
                        try await model.restartWeb()
                        showsRestartSuccess = true
                    } catch is CancellationError {
                        return
                    } catch {
                        model.present(error)
                    }
                }
            }
        } message: {
            Text("将执行 make web，当前连接会短暂断开。")
        }
        .alert("Web 重启完成", isPresented: $showsRestartSuccess) {
            Button("好", role: .cancel) {}
        } message: {
            Text("新的 Web 服务已就绪。")
        }
    }

    private var connectionStatusRow: some View {
        HStack(spacing: 12) {
            Text("连接状态")
                .foregroundStyle(QuartetTheme.secondaryText)
            Spacer(minLength: 8)
            HStack(spacing: 7) {
                Circle()
                    .fill(connectionStatusColor)
                    .frame(width: 8, height: 8)
                    .accessibilityHidden(true)
                Text(connectionStatusText)
                    .fontWeight(.semibold)
                    .foregroundStyle(QuartetTheme.primaryText)
            }
        }
        .font(.quartet(.detail))
        .padding(.horizontal, 16)
        .frame(minHeight: 52)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("settings-connection-status")
    }

    private var serverAddressRow: some View {
        VStack(alignment: .leading, spacing: 5) {
            Text("服务地址")
                .font(.quartet(.detail))
                .foregroundStyle(QuartetTheme.secondaryText)
            Text(model.serverAddress)
                .font(.quartet(.detail, design: .monospaced))
                .foregroundStyle(QuartetTheme.primaryText)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("settings-server-address")
    }

    private var serviceInfoDivider: some View {
        Divider()
            .overlay(QuartetTheme.divider)
            .padding(.leading, 16)
    }

    private func serviceInfoRow(title: String, value: String, identifier: String) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 12) {
            Text(LocalizedStringKey(title))
                .foregroundStyle(QuartetTheme.secondaryText)
                .layoutPriority(1)
            Spacer(minLength: 8)
            Text(value)
                .font(.quartet(.detail, design: .monospaced))
                .foregroundStyle(QuartetTheme.primaryText)
                .multilineTextAlignment(.trailing)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
        }
        .font(.quartet(.detail))
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
        .frame(minHeight: 52)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier(identifier)
    }

    private var connectionStatusText: String {
        (model.connectionState.isConnected
            ? (model.connectionState.isStale ? "缓存中" : "已连接")
            : "未连接").localizedForApp
    }

    private var connectionStatusColor: Color {
        guard model.connectionState.isConnected else { return QuartetTheme.failed }
        return model.connectionState.isStale ? QuartetTheme.warning : QuartetTheme.terminalGreen
    }

    private var formattedServerBuildTime: String {
        guard let rawValue = model.health?.buildTime?.trimmingCharacters(in: .whitespacesAndNewlines),
              !rawValue.isEmpty else {
            return "未知".localizedForApp
        }
        guard rawValue.lowercased() != "unknown" else {
            return "未知".localizedForApp
        }

        let parser = ISO8601DateFormatter()
        let date = parser.date(from: rawValue)
        guard let date else { return rawValue }

        return formattedDate(date)
    }

    private var formattedLastSuccessfulSyncTime: String {
        guard let date = model.connectionState.lastSuccessfulSyncAt else {
            return "尚未同步".localizedForApp
        }
        return formattedDate(date)
    }

    private func formattedDate(_ date: Date) -> String {
        let formatter = DateFormatter()
        formatter.locale = locale
        formatter.timeZone = .autoupdatingCurrent
        formatter.dateStyle = .short
        formatter.timeStyle = .short
        return formatter.string(from: date)
    }

    private func settingsRow(
        _ title: String,
        icon: String,
        destructive: Bool = false,
        loading: Bool = false
    ) -> some View {
        HStack(spacing: 14) {
            Image(systemName: icon).frame(width: 22)
            Text(LocalizedStringKey(title))
            Spacer()
            if loading {
                ProgressView()
            } else {
                Image(systemName: "chevron.right").font(.caption.weight(.bold))
            }
        }
        .foregroundStyle(destructive ? QuartetTheme.failed : QuartetTheme.primaryText)
        .padding(.horizontal, 16)
        .frame(height: 54)
        .contentShape(Rectangle())
    }

    private var settingsDivider: some View {
        Divider().overlay(QuartetTheme.divider).padding(.leading, 54)
    }

    private func agentManagementLink<Destination: View>(
        title: String,
        icon: String,
        identifier: String,
        @ViewBuilder destination: () -> Destination
    ) -> some View {
        NavigationLink {
            AgentSettingsDestination(title: title, content: destination)
        } label: {
            settingsRow(title, icon: icon)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier(identifier)
    }
}

private struct LanguagePickerSheet: View {
    let selection: AppLanguage
    let onSelect: (AppLanguage) -> Void

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                ForEach(Array(AppLanguage.allCases.enumerated()), id: \.element.id) { index, language in
                    if index > 0 {
                        Divider().overlay(QuartetTheme.divider).padding(.leading, 54)
                    }
                    Button { onSelect(language) } label: {
                        HStack(spacing: 12) {
                            Image(systemName: selection == language ? "checkmark.circle.fill" : "circle")
                                .foregroundStyle(selection == language ? QuartetTheme.accent : QuartetTheme.secondaryText)
                                .frame(width: 28)
                            Text(LocalizedStringKey(language.localizationKey))
                                .font(.quartet(.control, weight: .semibold))
                                .foregroundStyle(QuartetTheme.primaryText)
                            Spacer()
                        }
                        .padding(.horizontal, 14)
                        .frame(minHeight: 60)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityAddTraits(selection == language ? .isSelected : [])
                    .accessibilityIdentifier("settings-language-\(language.rawValue)")
                }
            }
            .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            .overlay {
                RoundedRectangle(cornerRadius: 18, style: .continuous)
                    .stroke(QuartetTheme.divider.opacity(0.8), lineWidth: 1)
            }
            .padding(.horizontal, 20)
            .padding(.top, 8)
            .frame(maxHeight: .infinity, alignment: .top)
            .background(QuartetTheme.canvas)
            .navigationTitle("显示语言")
            .navigationBarTitleDisplayMode(.inline)
        }
    }
}
