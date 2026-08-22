import SwiftUI

struct SettingsView: View {
    @EnvironmentObject private var model: AppModel
    @State private var confirmsClear = false

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

                    VStack(spacing: 0) {
                        Button { model.editConnection() } label: {
                            settingsRow("重新配置连接", icon: "network")
                        }
                        .accessibilityIdentifier("settings-edit-connection")
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
}
