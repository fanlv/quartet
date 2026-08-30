import Foundation
import SwiftUI
import UIKit

/// 目录浏览打开的目标文件：绝对路径加后端 Web 预览页地址。
struct WorkspaceFileDestination: Identifiable {
    let id = UUID()
    let path: String
    let previewURL: URL

    var name: String {
        URL(fileURLWithPath: path).lastPathComponent
    }
}

/// 目录浏览打开单个文件时的全屏浮层。同一层浮层里在「网页预览」和「编辑」之间切换：
/// 预览沿用后端的 Web 预览页（Markdown 渲染、语法高亮、分享），编辑是 App 内的纯文本
/// 编辑器，保存直接写回后端。两种模式各自持有自己的导航栏动作，不再叠加第二层浮层；
/// 从编辑切回预览时 WebView 会重建并重新加载，所以看到的是保存后的内容。
struct WorkspaceFilePage: View {
    private enum Mode {
        case preview
        case edit
    }

    let destination: WorkspaceFileDestination
    let onError: (APIError) -> Void
    /// 保存成功后通知目录列表重新读取，让文件大小和修改时间跟上。
    let onSaved: () -> Void

    @State private var mode: Mode = .preview

    private var webDestination: ChatWebDestination {
        ChatWebDestination(
            url: destination.previewURL,
            title: destination.name,
            copyTarget: .filePath(destination.path)
        )
    }

    var body: some View {
        Group {
            switch mode {
            case .preview:
                ChatWebViewPage(
                    destination: webDestination,
                    onError: onError,
                    onEdit: { mode = .edit }
                )
            case .edit:
                WorkspaceFileEditorView(
                    path: destination.path,
                    onShowPreview: { mode = .preview },
                    onSaved: onSaved
                )
            }
        }
        .animation(.snappy(duration: 0.24), value: mode)
    }
}

/// App 内的纯文本文件编辑器。内容取自 `read-file`，保存走 `write-file`。
///
/// 后端对这两个接口都设了 1MB 上限，并且只接受完整的 UTF-8 正文：二进制文件和超限文件
/// 拿到的是一段说明文本而不是真正的内容，把它写回去等于清空文件，所以这类文件一律只读。
private struct WorkspaceFileEditorView: View {
    /// 正文的四种状态。`text` 之外都不允许写回。
    private enum Content {
        case loading
        case failed(PresentedError)
        case text
        /// 后端没有返回可写回的完整正文，附带不能编辑的原因。
        case readOnly(String)
    }

    /// 有未保存修改时被拦下的动作。确认放弃后再执行。
    private enum DiscardIntent: Identifiable {
        case close
        case preview

        var id: String {
            switch self {
            case .close: "close"
            case .preview: "preview"
            }
        }

        var message: String {
            switch self {
            case .close: "关闭后当前的修改会丢失。"
            case .preview: "切换到网页预览会丢弃当前的修改。"
            }
        }
    }

    @EnvironmentObject private var model: AppModel
    @Environment(\.dismiss) private var dismiss

    let path: String
    let onShowPreview: () -> Void
    let onSaved: () -> Void

    @State private var content: Content = .loading
    @State private var draft = ""
    /// 打开时（以及每次保存成功后）磁盘上的内容。既用来判断有没有未保存的修改，
    /// 也用来在保存前确认文件没有被 Agent 或其他客户端改过。
    @State private var baseline = ""
    @State private var loadedSize: Int64 = 0
    @State private var loadedLineCount = 0
    @State private var isSaving = false
    @State private var saveError: PresentedError?
    @State private var justSaved = false
    @State private var saveFeedbackTask: Task<Void, Never>?
    @State private var pendingDiscard: DiscardIntent?
    @State private var overwriteDetail: String?
    @State private var loadGeneration = 0

    private var fileName: String {
        URL(fileURLWithPath: path).lastPathComponent
    }

    private var isTextContent: Bool {
        if case .text = content { return true }
        return false
    }

    private var canWrite: Bool {
        model.can("file.write")
    }

    private var canEdit: Bool {
        isTextContent && canWrite
    }

    private var isDirty: Bool {
        canEdit && draft != baseline
    }

    private var headerDetail: String? {
        guard isTextContent else { return nil }
        return AppLanguage.localizedFormat(
            "%@ · %lld 行",
            ByteCountFormatter.string(fromByteCount: loadedSize, countStyle: .file),
            Int64(loadedLineCount)
        )
    }

    var body: some View {
        VStack(spacing: 0) {
            WorkspaceBrowserLocationHeader(
                path: path,
                title: "当前文件",
                systemImage: "doc.text.fill",
                pathKind: .file,
                detail: headerDetail
            )
            .accessibilityIdentifier("file-editor-location-card")

            if isTextContent, !canWrite {
                noticeBar("当前账号缺少 file.write 权限，只能查看内容。")
            }

            editorBody
        }
        .background(QuartetTheme.canvas)
        .quartetNavigationTitle(fileName)
        .toolbar {
            ToolbarItem(placement: .topBarLeading) {
                Button("关闭") { request(.close) }
            }
            .sharedBackgroundVisibility(.hidden)
            ToolbarItem(placement: .topBarTrailing) {
                // 与聊天页一致：相邻的两颗按钮放进同一个 ToolbarItem 并走 plain style，
                // 工具栏默认样式会在每个标签外再补一圈内边距，把居中标题挤掉。
                HStack(spacing: 0) {
                    toolbarIconButton(
                        systemImage: "safari",
                        accessibilityLabel: "网页预览",
                        accessibilityHint: "切换回网页预览查看渲染后的文件",
                        identifier: "file-editor-preview"
                    ) {
                        request(.preview)
                    }

                    saveAction
                }
            }
            .sharedBackgroundVisibility(.hidden)
        }
        .task { await load() }
        .onDisappear { saveFeedbackTask?.cancel() }
        .alert(
            "放弃未保存的修改？",
            isPresented: Binding(
                get: { pendingDiscard != nil },
                set: { if !$0 { pendingDiscard = nil } }
            ),
            presenting: pendingDiscard
        ) { intent in
            Button("关闭", role: .cancel) { pendingDiscard = nil }
            Button("放弃修改", role: .destructive) {
                pendingDiscard = nil
                perform(intent)
            }
        } message: { intent in
            Text(LocalizedStringKey(intent.message))
        }
        .alert(
            "覆盖保存？",
            isPresented: Binding(
                get: { overwriteDetail != nil },
                set: { if !$0 { overwriteDetail = nil } }
            ),
            presenting: overwriteDetail
        ) { _ in
            Button("关闭", role: .cancel) { overwriteDetail = nil }
            Button("覆盖保存", role: .destructive) {
                overwriteDetail = nil
                Task { await save(overwritingExternalChanges: true) }
            }
        } message: { detail in
            Text(detail)
        }
    }

    @ViewBuilder
    private var saveAction: some View {
        if isSaving {
            ProgressView()
                .tint(QuartetTheme.accent)
                .frame(width: 30, height: 44)
                .accessibilityLabel("正在保存")
        } else if canEdit {
            toolbarIconButton(
                systemImage: justSaved ? "checkmark" : "square.and.arrow.down",
                accessibilityLabel: justSaved ? "已保存" : "保存",
                accessibilityHint: "把当前内容写回文件",
                identifier: "file-editor-save",
                disabled: !isDirty
            ) {
                Task { await save(overwritingExternalChanges: false) }
            }
        }
    }

    /// plain style 之后 `disabled` 不再自动变灰，前景色要自己给。
    private func toolbarIconButton(
        systemImage: String,
        accessibilityLabel: String,
        accessibilityHint: String,
        identifier: String,
        disabled: Bool = false,
        action: @escaping () -> Void
    ) -> some View {
        Button(action: action) {
            Image(systemName: systemImage)
                .font(.quartet(.regular, weight: .semibold))
                .foregroundStyle(disabled ? QuartetTheme.secondaryText.opacity(0.5) : QuartetTheme.accent)
                .frame(width: 30, height: 44)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(disabled)
        .accessibilityLabel(accessibilityLabel.localizedForApp)
        .accessibilityHint(accessibilityHint.localizedForApp)
        .accessibilityIdentifier(identifier)
    }

    @ViewBuilder
    private var editorBody: some View {
        switch content {
        case .loading:
            loadingState
        case let .failed(error):
            ScrollView {
                WorkspaceBrowserErrorCard(error: error) {
                    Task { await load() }
                }
                .padding(.horizontal, 18)
                .padding(.top, 12)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        case let .readOnly(reason):
            readOnlyState(reason)
        case .text:
            VStack(spacing: 0) {
                if let saveError {
                    ScrollView {
                        WorkspaceBrowserErrorCard(error: saveError) {
                            Task { await save(overwritingExternalChanges: false) }
                        }
                        .padding(.horizontal, 18)
                        .padding(.vertical, 12)
                    }
                    .frame(maxHeight: 220)
                }

                WorkspacePlainTextEditor(
                    text: $draft,
                    isEditable: canEdit,
                    accessibilityLabel: "文件内容",
                    identifier: "file-editor-text"
                )
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(QuartetTheme.surface)
            }
        }
    }

    private var loadingState: some View {
        HStack(spacing: 10) {
            ProgressView().tint(QuartetTheme.accent)
            Text("正在读取文件…")
                .font(.quartet(.detail))
                .foregroundStyle(QuartetTheme.secondaryText)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .accessibilityIdentifier("file-editor-loading")
    }

    private func readOnlyState(_ reason: String) -> some View {
        ContentUnavailableView {
            Label("无法在 App 内编辑".localizedForApp, systemImage: "doc.badge.ellipsis")
                .font(.quartet(.control, weight: .semibold))
        } description: {
            Text(reason.localizedForApp)
                .font(.quartet(.detail))
        } actions: {
            Button("用网页预览打开") { onShowPreview() }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .accessibilityIdentifier("file-editor-read-only")
    }

    private func noticeBar(_ text: String) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: "exclamationmark.triangle.fill")
                .font(.quartet(.compact, weight: .semibold))
                .accessibilityHidden(true)
            Text(text.localizedForApp)
                .font(.quartet(.compact, weight: .medium))
                .fixedSize(horizontal: false, vertical: true)
        }
        .foregroundStyle(QuartetTheme.warning)
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(QuartetTheme.warning.opacity(0.1))
        .accessibilityIdentifier("file-editor-notice")
    }

    /// 有未保存修改时先确认，否则直接执行。
    private func request(_ intent: DiscardIntent) {
        guard isDirty else {
            perform(intent)
            return
        }
        pendingDiscard = intent
    }

    private func perform(_ intent: DiscardIntent) {
        switch intent {
        case .close: dismiss()
        case .preview: onShowPreview()
        }
    }

    private func load() async {
        loadGeneration += 1
        let generation = loadGeneration
        content = .loading
        saveError = nil

        do {
            let response = try await model.apiClient().readFileContent(path: path)
            guard generation == loadGeneration else { return }
            if response.binary == true {
                content = .readOnly("这是二进制文件，App 内只支持编辑 UTF-8 文本文件。")
            } else if !response.isCompleteText {
                content = .readOnly("文件超过 1MB，接口没有返回完整内容，改在电脑上编辑。")
            } else {
                baseline = response.content
                draft = response.content
                loadedSize = response.size ?? Int64(response.content.utf8.count)
                loadedLineCount = Self.lineCount(of: response.content)
                content = .text
            }
        } catch is CancellationError {
            return
        } catch let caught {
            guard generation == loadGeneration else { return }
            content = .failed(Self.presented(caught, fallbackTitle: "文件读取失败"))
        }
    }

    /// 保存。默认先确认磁盘上的内容仍然是打开时读到的那份，避免把 Agent 刚写出来的
    /// 结果直接覆盖掉；确认不了就交给用户决定，不静默写入。
    private func save(overwritingExternalChanges force: Bool) async {
        guard canEdit, !isSaving else { return }
        isSaving = true
        saveError = nil
        defer { isSaving = false }

        let pending = draft
        do {
            let client = try model.apiClient()
            if !force, let conflict = await externalChangeDetail(client: client) {
                overwriteDetail = conflict
                return
            }
            try await client.writeFileContent(path: path, content: pending)
            baseline = pending
            loadedSize = Int64(pending.utf8.count)
            loadedLineCount = Self.lineCount(of: pending)
            flashSavedFeedback()
            onSaved()
        } catch is CancellationError {
            return
        } catch let caught {
            saveError = Self.presented(caught, fallbackTitle: "文件保存失败")
        }
    }

    /// 返回需要用户确认覆盖的原因；`nil` 表示磁盘内容与打开时一致，可以直接写。
    private func externalChangeDetail(client: APIClient) async -> String? {
        do {
            let response = try await client.readFileContent(path: path)
            guard response.isCompleteText else {
                return AppLanguage.localizedFormat(
                    "文件现在无法按文本读取，可能已经被替换成二进制内容或超过 1MB。\n继续保存会用当前编辑器里的内容覆盖它。\n文件路径：\n%@",
                    path
                )
            }
            guard response.content != baseline else { return nil }
            return AppLanguage.localizedFormat(
                "打开之后这个文件已经被改动过。继续保存会丢掉那些改动。\n文件路径：\n%@",
                path
            )
        } catch {
            let detail = Self.presented(error, fallbackTitle: "无法读取文件当前内容")
            return AppLanguage.localizedFormat(
                "无法确认文件当前的内容，继续保存可能覆盖别处的改动。\n%@\n\n%@",
                detail.title.localizedForApp,
                detail.detail
            )
        }
    }

    private func flashSavedFeedback() {
        justSaved = true
        UIAccessibility.post(notification: .announcement, argument: "文件已保存".localizedForApp)
        saveFeedbackTask?.cancel()
        saveFeedbackTask = Task { @MainActor in
            try? await Task.sleep(for: .seconds(2))
            guard !Task.isCancelled else { return }
            justSaved = false
        }
    }

    private static func presented(_ error: Error, fallbackTitle: String) -> PresentedError {
        if let apiError = error as? APIError {
            return PresentedError(title: apiError.summary, detail: apiError.detail)
        }
        return PresentedError(title: fallbackTitle, detail: String(describing: error))
    }

    private static func lineCount(of text: String) -> Int {
        guard !text.isEmpty else { return 0 }
        return text.utf8.reduce(into: 1) { count, byte in
            if byte == 0x0A { count += 1 }
        }
    }
}

/// 文件编辑用的纯文本输入区。
///
/// 这里不用 `TextEditor`：SwiftUI 关不掉智能标点，输入法会把代码和配置里的直引号、
/// 连字符换成全角形式，屏幕上几乎看不出来，保存之后文件就坏了。UITextView 能逐项关掉
/// 自动大写、拼写检查和全部智能替换。
private struct WorkspacePlainTextEditor: UIViewRepresentable {
    @Binding var text: String
    let isEditable: Bool
    let accessibilityLabel: String
    let identifier: String

    func makeCoordinator() -> Coordinator {
        Coordinator(text: $text)
    }

    func makeUIView(context: Context) -> UITextView {
        let view = UITextView()
        view.delegate = context.coordinator
        view.backgroundColor = .clear
        view.textColor = UIColor(QuartetTheme.primaryText)
        view.tintColor = UIColor(QuartetTheme.accent)
        view.autocapitalizationType = .none
        view.autocorrectionType = .no
        view.spellCheckingType = .no
        view.smartQuotesType = .no
        view.smartDashesType = .no
        view.smartInsertDeleteType = .no
        view.textContainerInset = UIEdgeInsets(top: 14, left: 12, bottom: 28, right: 12)
        view.textContainer.lineFragmentPadding = 2
        view.alwaysBounceVertical = true
        view.keyboardDismissMode = .interactive
        view.font = Self.font
        view.text = text
        view.isEditable = isEditable
        // 无障碍标签交给 UITextView 自己带，不在外面再套一层 SwiftUI 无障碍元素：
        // 那会把文本编辑的 VoiceOver 交互一起包掉。
        view.accessibilityLabel = accessibilityLabel.localizedForApp
        view.accessibilityIdentifier = identifier
        return view
    }

    func updateUIView(_ view: UITextView, context: Context) {
        context.coordinator.text = $text
        // 字号跟随系统辅助功能设置变化，字体本身在排版入口里有缓存。
        view.font = Self.font
        view.isEditable = isEditable
        // 输入法组词期间不要整段替换文本，否则候选会被打断；内容一致时也不写，
        // 不然每次刷新都把光标顶到末尾。
        guard view.markedTextRange == nil, view.text != text else { return }
        let caret = view.selectedRange.location
        view.text = text
        view.selectedRange = NSRange(location: min(caret, text.utf16.count), length: 0)
    }

    /// 编辑区要占满剩下的整块空间。UITextView 的固有尺寸跟着内容走：交给它自己决定的话，
    /// 长文件会把父视图撑出屏幕，短文件又会缩成一条，所以这里直接接受父视图给的尺寸。
    func sizeThatFits(_ proposal: ProposedViewSize, uiView: UITextView, context: Context) -> CGSize? {
        CGSize(
            width: proposal.width ?? uiView.contentSize.width,
            height: proposal.height ?? uiView.contentSize.height
        )
    }

    private static var font: UIFont {
        QuartetTypeface.uiFont(.detail, design: .monospaced)
    }

    @MainActor
    final class Coordinator: NSObject, UITextViewDelegate {
        var text: Binding<String>

        init(text: Binding<String>) {
            self.text = text
        }

        func textViewDidChange(_ textView: UITextView) {
            text.wrappedValue = textView.text
        }
    }
}
