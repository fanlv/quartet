import SwiftUI

/// 服务器切换弹窗。样式与首页「任务操作」弹窗一致：所有步骤（列表、条目动作、
/// 新增/重命名表单）都在同一个浮层内切换，不逐级叠加新的 Sheet。
struct ServerSwitcherSheet: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.dismiss) private var dismiss
    @FocusState private var focusedField: Field?

    /// 选中一台服务器。弹窗只负责关掉自己，真正的切换由调用方发起。
    let onSelect: (ServerBookmark) -> Void

    @State private var step: Step = .list
    @State private var addressDraft = ""
    @State private var nameDraft = ""
    @State private var editingBookmarkID: String?
    @State private var formError = ""
    @State private var deletingBookmark: ServerBookmark?
    @State private var plainHTTPBookmark: ServerBookmark?
    /// 弹窗内触发的错误用自己的浮层展示：根视图的错误浮层被本弹窗挡住，present 上去会被丢掉。
    @State private var sheetError: PresentedError?

    private enum Field: Hashable {
        case address
        case name
    }

    private enum Step: Equatable {
        case list
        case actions(ServerBookmark)
        case editor
    }

    private var currentServerID: String? {
        model.currentServerBookmark?.id
    }

    /// 清单里没有当前服务器时（首次连接前的手工地址）也要把它显示出来，避免列表里没有「当前」。
    /// 地址为空或非法时 `currentServerBookmark` 为 nil，此时不合成条目，否则会多出一行点了必然失败的空白项。
    private var bookmarks: [ServerBookmark] {
        let saved = model.serverBookmarks
        guard let current = model.currentServerBookmark,
              !saved.contains(where: { $0.id == current.id }) else { return saved }
        return [current] + saved
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 20) {
                    Group {
                        switch step {
                        case .list:
                            list
                        case .actions(let bookmark):
                            actions(for: bookmark)
                        case .editor:
                            editor
                        }
                    }
                    .transition(.opacity.combined(with: .move(edge: .bottom)))
                }
                .padding(.horizontal, 20)
                .padding(.top, 8)
                .padding(.bottom, 24)
            }
            .background(QuartetTheme.canvas)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .principal) {
                    Text(navigationTitle)
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .lineLimit(1)
                        .truncationMode(.tail)
                        .accessibilityAddTraits(.isHeader)
                }
                if step == .list {
                    ToolbarItem(placement: .topBarTrailing) {
                        Button { beginAdding() } label: {
                            Image(systemName: "plus")
                                .font(.quartet(.regular, weight: .semibold))
                                .foregroundStyle(QuartetTheme.accent)
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("添加服务器")
                        .accessibilityIdentifier("server-switcher-add")
                    }
                    .sharedBackgroundVisibility(.hidden)
                }
            }
        }
        .animation(.snappy(duration: 0.28), value: step)
        .alert("删除服务器？", isPresented: deleteAlertBinding) {
            Button("关闭", role: .cancel) { deletingBookmark = nil }
            Button("删除", role: .destructive) { confirmDelete() }
        } message: {
            if let deletingBookmark {
                Text(AppLanguage.localizedFormat(
                    "只会移除本机保存的“%@”和它在本机的登录状态，不影响该服务器上的任何数据。",
                    deletingBookmark.displayName
                ))
            }
        }
        .alert("使用未加密连接？", isPresented: plainHTTPAlertBinding) {
            Button("关闭", role: .cancel) { plainHTTPBookmark = nil }
            Button("继续连接", role: .destructive) { confirmPlainHTTPSwitch() }
        } message: {
            Text("HTTP 会让登录凭证和对话内容在局域网中以明文传输。只应连接你信任的网络。")
        }
        .sheet(item: $sheetError) { error in
            ErrorDetailView(error: error)
        }
    }

    private var navigationTitle: String {
        switch step {
        case .list:
            return "切换服务器".localizedForApp
        case .actions(let bookmark):
            return bookmark.displayName
        case .editor:
            return (editingBookmarkID == nil ? "添加服务器" : "重命名服务器").localizedForApp
        }
    }

    // MARK: - 列表

    private var list: some View {
        let currentID = currentServerID
        return VStack(spacing: 12) {
            VStack(spacing: 0) {
                ForEach(Array(bookmarks.enumerated()), id: \.element.id) { index, bookmark in
                    if index > 0 {
                        Divider()
                            .overlay(QuartetTheme.divider)
                            .padding(.leading, 54)
                    }
                    row(bookmark, isCurrent: bookmark.id == currentID)
                }
            }
            .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            .overlay {
                RoundedRectangle(cornerRadius: 18, style: .continuous)
                    .stroke(QuartetTheme.divider.opacity(0.8), lineWidth: 1)
            }

            Text("切换不会退出其他服务器的登录状态；只要它的登录仍然有效，切回去就不用再输密码。")
                .font(.quartet(.detail))
                .foregroundStyle(QuartetTheme.secondaryText)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 4)
        }
    }

    private func row(_ bookmark: ServerBookmark, isCurrent: Bool) -> some View {
        // 选中和「更多」必须是兄弟节点：嵌在 Button label 里的按钮收不到点击。
        HStack(spacing: 0) {
            Button {
                select(bookmark)
            } label: {
                HStack(spacing: 12) {
                    Image(systemName: isCurrent ? "checkmark.circle.fill" : "circle")
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(isCurrent ? QuartetTheme.accent : QuartetTheme.secondaryText)
                        .frame(width: 28)
                        .accessibilityHidden(true)

                    VStack(alignment: .leading, spacing: 3) {
                        Text(bookmark.displayName)
                            .font(.quartet(.control, weight: .semibold))
                            .foregroundStyle(QuartetTheme.primaryText)
                            .lineLimit(1)
                            .frame(maxWidth: .infinity, alignment: .leading)
                        Text(bookmark.id)
                            .font(.quartet(.detail, design: .monospaced))
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .lineLimit(1)
                            .truncationMode(.middle)
                            .frame(maxWidth: .infinity, alignment: .leading)
                        Text(subtitle(for: bookmark, isCurrent: isCurrent))
                            .font(.quartet(.compact))
                            .foregroundStyle(QuartetTheme.secondaryText.opacity(0.78))
                            .lineLimit(1)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }

                    Spacer(minLength: 8)
                }
                .padding(.horizontal, 14)
                .frame(minHeight: 74)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel("\(bookmark.displayName)，\(bookmark.id)")
            .accessibilityAddTraits(isCurrent ? .isSelected : [])
            .accessibilityHint("切换到该服务器".localizedForApp)
            .accessibilityIdentifier("server-switcher-item-\(bookmark.id)")

            Button {
                step = .actions(bookmark)
            } label: {
                Image(systemName: "ellipsis")
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .frame(width: 34, height: 44)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .padding(.trailing, 8)
            .accessibilityLabel(AppLanguage.localizedFormat("%@ 的服务器操作", bookmark.displayName))
            .accessibilityIdentifier("server-switcher-more-\(bookmark.id)")
        }
    }

    private func subtitle(for bookmark: ServerBookmark, isCurrent: Bool) -> String {
        var parts: [String] = []
        if isCurrent {
            parts.append("当前使用中".localizedForApp)
        }
        let username = bookmark.username.trimmingCharacters(in: .whitespacesAndNewlines)
        parts.append(
            username.isEmpty
                ? "尚未登录过".localizedForApp
                : AppLanguage.localizedFormat("上次登录：%@", username)
        )
        return parts.joined(separator: " · ")
    }

    // MARK: - 条目动作

    private func actions(for bookmark: ServerBookmark) -> some View {
        VStack(spacing: 12) {
            actionRow(
                title: "重命名",
                detail: "给这台服务器起一个容易识别的备注名",
                systemImage: "pencil",
                tint: QuartetTheme.accentDeep,
                identifier: "server-switcher-action-rename"
            ) {
                beginRenaming(bookmark)
            }
            .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            .overlay {
                RoundedRectangle(cornerRadius: 18, style: .continuous)
                    .stroke(QuartetTheme.divider.opacity(0.8), lineWidth: 1)
            }

            if bookmark.id != currentServerID {
                actionRow(
                    title: "删除",
                    detail: "移除本机保存的地址和登录状态",
                    systemImage: "trash.fill",
                    tint: QuartetTheme.failed,
                    isDestructive: true,
                    identifier: "server-switcher-action-delete"
                ) {
                    deletingBookmark = bookmark
                }
                .background(QuartetTheme.failed.opacity(0.07), in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                .overlay {
                    RoundedRectangle(cornerRadius: 18, style: .continuous)
                        .stroke(QuartetTheme.failed.opacity(0.18), lineWidth: 1)
                }
            }

            secondaryButton("返回") { step = .list }
        }
    }

    private func actionRow(
        title: String,
        detail: String,
        systemImage: String,
        tint: Color,
        isDestructive: Bool = false,
        identifier: String,
        action: @escaping () -> Void
    ) -> some View {
        Button(role: isDestructive ? .destructive : nil, action: action) {
            HStack(spacing: 12) {
                Image(systemName: systemImage)
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(tint)
                    .frame(width: 38, height: 38)
                    .background(tint.opacity(0.11), in: Circle())

                VStack(alignment: .leading, spacing: 3) {
                    Text(title.localizedForApp)
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(isDestructive ? QuartetTheme.failed : QuartetTheme.primaryText)
                    Text(detail.localizedForApp)
                        .font(.quartet(.detail))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .lineLimit(1)
                }

                Spacer(minLength: 8)

                Image(systemName: "chevron.right")
                    .font(.quartet(.compact, weight: .bold))
                    .foregroundStyle(QuartetTheme.secondaryText.opacity(0.7))
            }
            .padding(.horizontal, 13)
            .frame(minHeight: 64)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier(identifier)
    }

    // MARK: - 新增 / 重命名表单

    private var editor: some View {
        VStack(alignment: .leading, spacing: 14) {
            if editingBookmarkID == nil {
                field(
                    title: "服务地址",
                    placeholder: "https://devbox.fanlv.fun/",
                    text: $addressDraft,
                    focus: .address,
                    isURL: true,
                    identifier: "server-switcher-address-field"
                )
            }

            field(
                title: "备注名（可选）",
                placeholder: "例如：家里的 Mac",
                text: $nameDraft,
                focus: .name,
                isURL: false,
                identifier: "server-switcher-name-field"
            )

            if !formError.isEmpty {
                Text(formError)
                    .font(.quartet(.detail, design: .monospaced))
                    .foregroundStyle(QuartetTheme.failed)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(12)
                    .background(QuartetTheme.failed.opacity(0.07), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                    .accessibilityIdentifier("server-switcher-form-error")
            }

            HStack(spacing: 10) {
                secondaryButton("返回") {
                    focusedField = nil
                    formError = ""
                    step = .list
                }

                Button("保存", action: save)
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.onAccent)
                    .frame(maxWidth: .infinity)
                    .frame(height: 50)
                    .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                    .disabled(!canSave)
                    .opacity(canSave ? 1 : 0.45)
                    .accessibilityIdentifier("server-switcher-save")
            }
        }
        // 表单是在同一浮层里切进来的，焦点要等它真正出现之后再给。
        .task { focusedField = editingBookmarkID == nil ? .address : .name }
    }

    private func field(
        title: String,
        placeholder: String,
        text: Binding<String>,
        focus: Field,
        isURL: Bool,
        identifier: String
    ) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(LocalizedStringKey(title))
                .font(.quartet(.detail, weight: .semibold))
                .foregroundStyle(QuartetTheme.secondaryText)

            TextField(LocalizedStringKey(placeholder), text: text)
                .font(.quartet(.regular, design: isURL ? .monospaced : .default))
                .foregroundStyle(QuartetTheme.primaryText)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .keyboardType(isURL ? .URL : .default)
                .submitLabel(isURL ? .next : .done)
                .focused($focusedField, equals: focus)
                .onSubmit {
                    if isURL {
                        focusedField = .name
                    } else {
                        save()
                    }
                }
                .padding(.horizontal, 15)
                .frame(minHeight: 52)
                .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                .overlay {
                    RoundedRectangle(cornerRadius: 14, style: .continuous)
                        .stroke(focusedField == focus ? QuartetTheme.accent : QuartetTheme.divider, lineWidth: 1)
                }
                .accessibilityIdentifier(identifier)
        }
    }

    private func secondaryButton(_ title: String, action: @escaping () -> Void) -> some View {
        Button(LocalizedStringKey(title), action: action)
            .font(.quartet(.control, weight: .semibold))
            .foregroundStyle(QuartetTheme.primaryText)
            .frame(maxWidth: .infinity)
            .frame(height: 50)
            .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
    }

    // MARK: - 动作

    /// 明文 HTTP 的目标要先确认，和连接页手工输入 http 地址时的提示保持一致。
    private func select(_ bookmark: ServerBookmark) {
        // 点已连上的当前服务器不会触发重连，别为一次空操作弹明文连接警告。
        let isNoOp = bookmark.id == currentServerID && model.connectionState.isConnected
        guard !isNoOp, URLComponents(string: bookmark.id)?.scheme?.lowercased() == "http" else {
            dismiss()
            onSelect(bookmark)
            return
        }
        plainHTTPBookmark = bookmark
    }

    private var plainHTTPAlertBinding: Binding<Bool> {
        Binding(
            get: { plainHTTPBookmark != nil },
            set: { if !$0 { plainHTTPBookmark = nil } }
        )
    }

    private func confirmPlainHTTPSwitch() {
        guard let bookmark = plainHTTPBookmark else { return }
        plainHTTPBookmark = nil
        dismiss()
        onSelect(bookmark)
    }

    private var canSave: Bool {
        editingBookmarkID != nil
            || !addressDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    private func beginAdding() {
        editingBookmarkID = nil
        addressDraft = ""
        nameDraft = ""
        formError = ""
        step = .editor
    }

    private func beginRenaming(_ bookmark: ServerBookmark) {
        editingBookmarkID = bookmark.id
        addressDraft = bookmark.id
        nameDraft = bookmark.name
        formError = ""
        step = .editor
    }

    private func save() {
        guard canSave else { return }
        focusedField = nil
        do {
            if let editingBookmarkID {
                try model.renameServerBookmark(id: editingBookmarkID, name: nameDraft)
            } else {
                try model.saveServerBookmark(address: addressDraft, name: nameDraft)
            }
            formError = ""
            step = .list
        } catch let error as APIError {
            formError = "\(error.summary.localizedForApp)\n\n\(error.detail)"
        } catch {
            formError = String(describing: error)
        }
    }

    private var deleteAlertBinding: Binding<Bool> {
        Binding(
            get: { deletingBookmark != nil },
            set: { if !$0 { deletingBookmark = nil } }
        )
    }

    private func confirmDelete() {
        guard let bookmark = deletingBookmark else { return }
        deletingBookmark = nil
        do {
            try model.removeServerBookmark(id: bookmark.id)
            step = .list
        } catch let error as APIError {
            sheetError = PresentedError(title: error.summary, detail: error.detail)
        } catch {
            sheetError = PresentedError(title: "删除服务器失败", detail: String(describing: error))
        }
    }
}
