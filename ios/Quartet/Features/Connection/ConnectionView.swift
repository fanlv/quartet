import SwiftUI

struct ConnectionView: View {
    @EnvironmentObject private var model: AppModel
    @State private var revealsPassword = false
    @State private var confirmsHTTP = false
    @State private var showsServerSwitcher = false

    private var usesPlainHTTP: Bool {
        URLComponents(string: model.serverAddress.trimmingCharacters(in: .whitespacesAndNewlines))?.scheme?.lowercased() == "http"
    }

    var body: some View {
        ZStack {
            QuartetTheme.canvas.ignoresSafeArea()
            Circle()
                .fill(QuartetTheme.accent.opacity(0.10))
                .frame(width: 320, height: 320)
                .blur(radius: 80)
                .offset(x: 170, y: -300)
                .accessibilityHidden(true)
            ScrollView {
                VStack(alignment: .leading, spacing: 24) {
                    Spacer(minLength: 24)
                    brand
                    connectionForm
                    boundaryNote
                    Spacer(minLength: 24)
                }
                .padding(.horizontal, 24)
                .frame(maxWidth: 620)
                .frame(maxWidth: .infinity)
            }
            .scrollDismissesKeyboard(.interactively)
        }
        .alert("使用未加密连接？", isPresented: $confirmsHTTP) {
            Button("关闭", role: .cancel) {}
            Button("继续连接", role: .destructive) {
                Task { await model.connect() }
            }
        } message: {
            Text("HTTP 会让登录凭证和对话内容在局域网中以明文传输。只应连接你信任的网络。")
        }
        .sheet(isPresented: $showsServerSwitcher) {
            ServerSwitcherSheet { bookmark in
                Task { await model.switchServer(to: bookmark) }
            }
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
        }
    }

    private var brand: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 14) {
                PulseMark()
                Text("QUARTET / IOS")
                    .font(.quartet(.compact, weight: .bold, design: .monospaced))
                    .tracking(2.4)
                    .foregroundStyle(QuartetTheme.accent)
            }
            Text("连接运行工作台")
                .font(.quartet(.display, weight: .bold, design: .rounded))
                .foregroundStyle(QuartetTheme.primaryText)
                .fixedSize(horizontal: false, vertical: true)
            Text("随时查看 Job、继续对话、处理等待中的工作流。")
                .font(.quartet(.regular))
                .foregroundStyle(QuartetTheme.secondaryText)
                .lineSpacing(4)
        }
    }

    private var connectionForm: some View {
        VStack(alignment: .leading, spacing: 18) {
            VStack(alignment: .leading, spacing: 8) {
                fieldLabel("服务地址", index: "01")
                TextField("https://devbox.fanlv.fun/", text: $model.serverAddress)
                    .textInputAutocapitalization(.never)
                    .keyboardType(.URL)
                    .autocorrectionDisabled()
                    .font(.quartet(.regular, design: .monospaced))
                    .submitLabel(.next)
                    .accessibilityIdentifier("connection-server")
                    .padding(14)
                    .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12))

                if !model.serverBookmarks.isEmpty {
                    Button { showsServerSwitcher = true } label: {
                        HStack(spacing: 6) {
                            Image(systemName: "server.rack")
                            Text(AppLanguage.localizedFormat("已保存的服务器（%d）", model.serverBookmarks.count))
                            Image(systemName: "chevron.right")
                                .font(.quartet(.compact, weight: .bold))
                        }
                        .font(.quartet(.detail, weight: .semibold))
                        .foregroundStyle(QuartetTheme.accentDeep)
                        .frame(minHeight: 30)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("connection-saved-servers")
                }
            }

            VStack(alignment: .leading, spacing: 8) {
                fieldLabel("用户名", index: "02")
                TextField("请输入用户名", text: $model.username)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .font(.quartet(.regular, design: .monospaced))
                    .submitLabel(.next)
                    .accessibilityIdentifier("connection-username")
                    .padding(14)
                    .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12))
            }

            VStack(alignment: .leading, spacing: 8) {
                fieldLabel("密码", index: "03")
                HStack {
                    Group {
                        if revealsPassword {
                            TextField("请输入密码", text: $model.password)
                        } else {
                            SecureField("请输入密码", text: $model.password)
                        }
                    }
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .font(.quartet(.regular, design: .monospaced))
                    .submitLabel(.go)
                    .onSubmit(startConnection)
                    .accessibilityIdentifier("connection-password")

                    Button { revealsPassword.toggle() } label: {
                        Image(systemName: revealsPassword ? "eye.slash" : "eye")
                            .foregroundStyle(QuartetTheme.secondaryText)
                    }
                    .accessibilityLabel(revealsPassword ? "隐藏密码" : "显示密码")
                }
                .padding(14)
                .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12))
            }

            Button(action: startConnection) {
                HStack {
                    if model.phase == .connecting { ProgressView().tint(QuartetTheme.onAccent) }
                    Text(model.phase == .connecting ? "正在验证…" : "连接 Quartet")
                    Spacer()
                    Image(systemName: "arrow.up.right")
                }
                .font(.quartet(.regular, weight: .semibold))
                .foregroundStyle(QuartetTheme.onAccent)
                .padding(.horizontal, 18)
                .frame(height: 54)
                .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 14))
            }
            .disabled(model.phase == .connecting || model.serverAddress.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            .accessibilityIdentifier("connection-submit")
        }
        .padding(20)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 20))
        .overlay(RoundedRectangle(cornerRadius: 20).stroke(QuartetTheme.divider, lineWidth: 1))
    }

    private var boundaryNote: some View {
        Label("密码不会保存在设备上；登录状态由系统 Cookie 管理。应用不会在 iPhone 上运行 Agent。", systemImage: "lock.shield")
            .font(.quartet(.detail))
            .foregroundStyle(QuartetTheme.secondaryText)
    }

    private func fieldLabel(_ text: String, index: String) -> some View {
        HStack(spacing: 8) {
            Text(index).font(.quartet(.compact, weight: .bold, design: .monospaced)).foregroundStyle(QuartetTheme.accent)
            Text(text.localizedForApp).font(.quartet(.control, weight: .semibold)).foregroundStyle(QuartetTheme.primaryText)
        }
    }

    private func startConnection() {
        if usesPlainHTTP {
            confirmsHTTP = true
        } else {
            Task { await model.connect() }
        }
    }
}
