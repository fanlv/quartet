import SwiftUI

struct ConnectionView: View {
    @EnvironmentObject private var model: AppModel
    @State private var revealsToken = false
    @State private var confirmsHTTP = false

    private var usesPlainHTTP: Bool {
        URLComponents(string: model.serverAddress.trimmingCharacters(in: .whitespacesAndNewlines))?.scheme?.lowercased() == "http"
    }

    var body: some View {
        ZStack {
            QuartetTheme.canvas.ignoresSafeArea()
            ScrollView {
                VStack(alignment: .leading, spacing: 28) {
                    Spacer(minLength: 52)
                    brand
                    connectionForm
                    boundaryNote
                    Spacer(minLength: 24)
                }
                .padding(.horizontal, 24)
            }
        }
        .alert("使用未加密连接？", isPresented: $confirmsHTTP) {
            Button("取消", role: .cancel) {}
            Button("继续连接", role: .destructive) {
                Task { await model.connect() }
            }
        } message: {
            Text("HTTP 会让 Token 和对话内容在局域网中以明文传输。只应连接你信任的网络。")
        }
    }

    private var brand: some View {
        VStack(alignment: .leading, spacing: 14) {
            PulseMark()
            Text("QUARTET / IOS")
                .font(.system(size: 12, weight: .semibold, design: .monospaced))
                .tracking(3)
                .foregroundStyle(QuartetTheme.accent)
            Text("连接你的\n运行工作台")
                .font(.system(size: 42, weight: .bold, design: .rounded))
                .foregroundStyle(QuartetTheme.primaryText)
                .fixedSize(horizontal: false, vertical: true)
            Text("在同一局域网中查看 Job、掌握运行状态，并在需要时立即停止。")
                .font(.body)
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
                    .font(.system(.body, design: .monospaced))
                    .padding(14)
                    .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12))
            }

            VStack(alignment: .leading, spacing: 8) {
                fieldLabel("访问 Token", index: "02")
                HStack {
                    Group {
                        if revealsToken {
                            TextField("未启用鉴权时可留空", text: $model.token)
                        } else {
                            SecureField("未启用鉴权时可留空", text: $model.token)
                        }
                    }
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .font(.system(.body, design: .monospaced))

                    Button { revealsToken.toggle() } label: {
                        Image(systemName: revealsToken ? "eye.slash" : "eye")
                            .foregroundStyle(QuartetTheme.secondaryText)
                    }
                    .accessibilityLabel(revealsToken ? "隐藏 Token" : "显示 Token")
                }
                .padding(14)
                .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12))
            }

            Button(action: startConnection) {
                HStack {
                    if model.phase == .connecting { ProgressView().tint(.black) }
                    Text(model.phase == .connecting ? "正在验证…" : "连接 Quartet")
                    Spacer()
                    Image(systemName: "arrow.up.right")
                }
                .font(.headline)
                .foregroundStyle(.black)
                .padding(.horizontal, 18)
                .frame(height: 54)
                .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 14))
            }
            .disabled(model.phase == .connecting || model.serverAddress.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        }
        .padding(20)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 20))
        .overlay(RoundedRectangle(cornerRadius: 20).stroke(QuartetTheme.divider, lineWidth: 1))
    }

    private var boundaryNote: some View {
        Label("Token 仅保存在本机 Keychain。应用不会在 iPhone 上运行 Agent。", systemImage: "lock.shield")
            .font(.footnote)
            .foregroundStyle(QuartetTheme.secondaryText)
    }

    private func fieldLabel(_ text: String, index: String) -> some View {
        HStack(spacing: 8) {
            Text(index).font(.system(.caption, design: .monospaced)).foregroundStyle(QuartetTheme.accent)
            Text(text).font(.subheadline.weight(.semibold)).foregroundStyle(QuartetTheme.primaryText)
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
