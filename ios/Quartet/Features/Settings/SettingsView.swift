import SwiftUI

struct SettingsView: View {
    @EnvironmentObject private var model: AppModel
    @State private var confirmsClear = false
    @State private var presentsNotifications = false

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 24) {
                    VStack(alignment: .leading, spacing: 10) {
                        Text("ACTIVE ENDPOINT")
                            .font(.system(.caption, design: .monospaced).weight(.bold))
                            .foregroundStyle(QuartetTheme.accent)
                        Text(model.serverAddress)
                            .font(.system(.body, design: .monospaced))
                            .foregroundStyle(QuartetTheme.primaryText)
                            .textSelection(.enabled)
                        HStack(spacing: 7) {
                            Circle().fill(model.connectionState.isConnected ? QuartetTheme.accent : QuartetTheme.failed).frame(width: 8, height: 8)
                            Text(model.connectionState.isConnected ? (model.connectionState.isStale ? "缓存中" : "已连接") : "未连接")
                            if let buildTime = model.health?.buildTime { Text("· \(buildTime)") }
                        }
                        .font(.caption)
                        .foregroundStyle(QuartetTheme.secondaryText)
                        if let lastSuccessfulSyncAt = model.connectionState.lastSuccessfulSyncAt {
                            Text("最后成功同步：\(lastSuccessfulSyncAt.formatted(date: .omitted, time: .shortened))")
                                .font(.caption)
                                .foregroundStyle(QuartetTheme.secondaryText)
                        }
                    }
                    .padding(20)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18))
                    .overlay(RoundedRectangle(cornerRadius: 18).stroke(QuartetTheme.divider))

                    Button { presentsNotifications = true } label: {
                        HStack(alignment: .center, spacing: 14) {
                            Image(systemName: "bell.badge")
                                .frame(width: 22)
                            VStack(alignment: .leading, spacing: 4) {
                                Text("通知中心")
                                    .foregroundStyle(QuartetTheme.primaryText)
                                Text(notificationSummary)
                                    .font(.caption)
                                    .foregroundStyle(QuartetTheme.secondaryText)
                            }
                            Spacer()
                            if !model.notifications.filter(\.isUnread).isEmpty {
                                Text("\(model.notifications.filter(\.isUnread).count)")
                                    .font(.system(.caption, design: .monospaced).weight(.bold))
                                    .padding(.horizontal, 8)
                                    .padding(.vertical, 4)
                                    .background(QuartetTheme.failed.opacity(0.12), in: Capsule())
                                    .foregroundStyle(QuartetTheme.failed)
                            }
                            Image(systemName: "chevron.right").font(.caption.weight(.bold))
                        }
                        .padding(.horizontal, 16)
                        .frame(height: 72)
                    }
                    .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18))
                    .overlay(RoundedRectangle(cornerRadius: 18).stroke(QuartetTheme.divider))

                    VStack(spacing: 0) {
                        Button { model.editConnection() } label: {
                            settingsRow("重新配置连接", icon: "network")
                        }
                        Divider().overlay(QuartetTheme.divider).padding(.leading, 54)
                        Button(role: .destructive) { confirmsClear = true } label: {
                            settingsRow("清除地址与 Token", icon: "trash", destructive: true)
                        }
                    }
                    .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18))
                    .overlay(RoundedRectangle(cornerRadius: 18).stroke(QuartetTheme.divider))

                    Text("Quartet iOS 仅在可访问后端的局域网内工作。应用进入后台后不保证持续接收运行事件，重新打开时会同步最新状态。")
                        .font(.footnote)
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .lineSpacing(3)
                }
                .padding(20)
            }
            .background(QuartetTheme.canvas)
            .navigationTitle("设置")
        }
        .confirmationDialog("清除当前连接？", isPresented: $confirmsClear, titleVisibility: .visible) {
            Button("清除连接", role: .destructive) { model.clearConnection() }
            Button("取消", role: .cancel) {}
        } message: {
            Text("服务地址和 Keychain 中的 Token 都会被删除。")
        }
        .sheet(isPresented: $presentsNotifications) {
            NavigationStack {
                NotificationsView()
                    .environmentObject(model)
            }
        }
    }

    private func settingsRow(_ title: String, icon: String, destructive: Bool = false) -> some View {
        HStack(spacing: 14) {
            Image(systemName: icon).frame(width: 22)
            Text(title)
            Spacer()
            Image(systemName: "chevron.right").font(.caption.weight(.bold))
        }
        .foregroundStyle(destructive ? QuartetTheme.failed : QuartetTheme.primaryText)
        .padding(.horizontal, 16)
        .frame(height: 54)
        .contentShape(Rectangle())
    }

    private var notificationSummary: String {
        switch model.notificationAuthorizationStatus {
        case .authorized, .provisional, .ephemeral:
            return "已启用本地通知，\(model.notifications.filter(\.isUnread).count) 条未读。"
        case .denied:
            return "系统通知权限已关闭，仍可在应用内查看事件。"
        case .notDetermined:
            return "尚未请求系统通知权限。"
        default:
            return "通知权限受限，应用内通知仍可用。"
        }
    }
}
