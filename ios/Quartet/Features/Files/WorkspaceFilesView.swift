import Foundation
import SwiftUI

/// 文件浏览 tab：导航栏按运行台的方式切换工作空间，逐级下钻工作空间目录，
/// 点击文件复用后端的 Web 文件预览页在应用内打开，不在本地解析文件内容。
struct WorkspaceFilesView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.mainTabBarInset) private var mainTabBarInset

    @State private var workspaceID: String?
    @State private var routes: [WorkspaceDirectoryRoute] = []
    @State private var presentsWorkspaceSelector = false
    @State private var webDestination: ChatWebDestination?

    private var selectedWorkspace: WorkspaceSummary? {
        guard let workspaceID else { return nil }
        return model.workspaces.first { $0.id == workspaceID }
    }

    private var workspaceRoot: String {
        selectedWorkspace?.workdir.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    }

    private var selectedWorkspaceTitle: String {
        selectedWorkspace?.displayName ?? "选择工作空间".localizedForApp
    }

    var body: some View {
        NavigationStack(path: $routes) {
            rootContent
                .background(QuartetTheme.canvas)
                .navigationTitle("文件")
                .navigationBarTitleDisplayMode(.inline)
                .navigationDestination(for: WorkspaceDirectoryRoute.self) { route in
                    WorkspaceDirectoryView(directory: route.path, onOpenFile: openFile)
                        .quartetNavigationTitle(route.name)
                }
                .toolbar {
                    ToolbarItem(placement: .principal) {
                        workspaceSelector
                    }
                }
                .sheet(isPresented: $presentsWorkspaceSelector) {
                    WorkspaceLaunchPicker(
                        workspaces: model.workspaces,
                        selectedWorkspaceID: workspaceID,
                        accessibilityIdentifierPrefix: "files-workspace-",
                        onSelect: { selectWorkspace($0) }
                    )
                    .presentationDetents([.medium, .large])
                    .quartetSheetStyle()
                }
        }
        .toolbarBackground(QuartetTheme.canvas, for: .navigationBar)
        .toolbarBackground(.visible, for: .navigationBar)
        // 预览页是全屏 WebView：目录列表停留在原处，关闭后回到同一层目录。
        .fullScreenCover(item: $webDestination) { destination in
            NavigationStack {
                ChatWebViewPage(
                    destination: destination,
                    onError: { model.present($0) }
                )
            }
            .quartetSheetStyle()
        }
        .onAppear { resolveWorkspaceIfNeeded() }
        .onChange(of: model.workspaces.map(\.id)) { _, _ in resolveWorkspaceIfNeeded() }
    }

    private var workspaceSelector: some View {
        Button { presentsWorkspaceSelector = true } label: {
            HStack(spacing: 6) {
                Text(selectedWorkspaceTitle)
                    .font(.quartet(.regular, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                    .lineLimit(1)
                Image(systemName: "chevron.down")
                    .font(.quartet(.compact, weight: .bold))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
            .frame(maxWidth: 190)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(model.workspaces.isEmpty)
        .accessibilityLabel("工作空间，当前为\(selectedWorkspaceTitle)")
        .accessibilityHint("点按选择其他工作空间")
        .accessibilityIdentifier("files-workspace-selector")
    }

    @ViewBuilder
    private var rootContent: some View {
        if !model.can("file.read") {
            unavailableState(
                title: "无法浏览文件",
                systemImage: "lock.fill",
                description: "当前账号缺少 file.read 权限，无法读取工作空间目录。",
                identifier: "files-no-permission"
            ) {
                EmptyView()
            }
        } else if model.workspaces.isEmpty {
            unavailableState(
                title: "没有可用的工作空间",
                systemImage: "square.stack.3d.up.slash",
                description: "请先在 Web 端创建工作空间。",
                identifier: "files-no-workspace"
            ) {
                Button("刷新") {
                    Task { await model.refreshDashboard() }
                }
            }
        } else if workspaceRoot.isEmpty {
            unavailableState(
                title: "工作空间没有工作目录",
                systemImage: "folder.badge.questionmark",
                description: "这个工作空间还没有配置工作目录，换一个工作空间再浏览。",
                identifier: "files-no-workdir"
            ) {
                Button("选择工作空间") { presentsWorkspaceSelector = true }
            }
        } else {
            // 切换工作空间时重建列表：沿用同一个视图会让上一个空间的目录内容停留一帧。
            WorkspaceDirectoryView(directory: workspaceRoot, onOpenFile: openFile)
                .id(workspaceRoot)
        }
    }

    private func unavailableState<Actions: View>(
        title: String,
        systemImage: String,
        description: String,
        identifier: String,
        @ViewBuilder actions: () -> Actions
    ) -> some View {
        ContentUnavailableView {
            Label(title.localizedForApp, systemImage: systemImage)
                .font(.quartet(.control, weight: .semibold))
        } description: {
            Text(description.localizedForApp)
                .font(.quartet(.detail))
        } actions: {
            actions()
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .mainTabBarBottomInset(mainTabBarInset)
        .accessibilityIdentifier(identifier)
    }

    private func resolveWorkspaceIfNeeded() {
        if let workspaceID, model.workspaces.contains(where: { $0.id == workspaceID }) { return }
        // 先用本 tab 上次浏览的空间，其次跟随运行台的当前筛选，最后回落到第一个空间。
        let resolved = [model.lastFilesWorkspaceID, model.selectedWorkspaceID]
            .compactMap { $0 }
            .first { candidate in model.workspaces.contains { $0.id == candidate } }
            ?? model.workspaces.first?.id
        guard resolved != workspaceID else { return }
        routes = []
        workspaceID = resolved
    }

    private func selectWorkspace(_ id: String?) {
        guard let id, id != workspaceID else { return }
        routes = []
        workspaceID = id
        model.recordFilesWorkspace(id)
    }

    private func openFile(_ filePath: String) {
        let name = URL(fileURLWithPath: filePath).lastPathComponent
        do {
            let baseURL = try model.apiClient().baseURL
            guard let previewURL = ChatLinkTarget.filePreviewURL(baseURL: baseURL, path: filePath) else {
                throw APIError(
                    summary: "无法生成文件 URL",
                    detail: "Quartet 服务地址或文件预览 URL 无效。\n服务地址：\n\(baseURL.absoluteString)\n文件路径：\n\(filePath)"
                )
            }
            webDestination = ChatWebDestination(url: previewURL, title: name)
        } catch {
            model.present(error)
        }
    }
}

/// 目录下钻的导航值：只带绝对路径，标题从最后一段推导。
struct WorkspaceDirectoryRoute: Hashable {
    let path: String

    var name: String {
        path.split(separator: "/", omittingEmptySubsequences: true).last.map(String.init) ?? path
    }
}

/// 单层目录列表：目录行下钻到下一层，文件行交给外层打开预览。
private struct WorkspaceDirectoryView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.mainTabBarInset) private var mainTabBarInset

    let directory: String
    let onOpenFile: (String) -> Void

    @State private var directories: [String] = []
    @State private var files: [DirectoryFileEntry] = []
    @State private var isLoading = true
    @State private var error: PresentedError?
    @State private var requestGeneration = 0

    private var isEmptyDirectory: Bool {
        directories.isEmpty && files.isEmpty
    }

    private var entryCountSummary: String {
        AppLanguage.localizedFormat(
            "%lld 个目录 · %lld 个文件",
            Int64(directories.count),
            Int64(files.count)
        )
    }

    var body: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 0) {
                locationHeader

                if isLoading, !isEmptyDirectory {
                    HStack {
                        Spacer()
                        ProgressView().tint(QuartetTheme.accent)
                        Spacer()
                    }
                    .frame(height: 36)
                    .background(QuartetTheme.surface)
                }

                if let error {
                    errorCard(error)
                        .padding(.horizontal, 18)
                        .padding(.top, 10)
                } else if isLoading, isEmptyDirectory {
                    loadingCard
                } else if isEmptyDirectory {
                    emptyCard
                } else {
                    entryRows
                }
            }
            .padding(.bottom, 18)
        }
        .background(QuartetTheme.canvas)
        .mainTabBarBottomInset(mainTabBarInset)
        .refreshable { await load() }
        .task(id: directory) { await load() }
    }

    private var locationHeader: some View {
        WorkspaceBrowserLocationHeader(
            path: directory,
            detail: error == nil && !isLoading ? entryCountSummary : nil
        )
        .accessibilityIdentifier("files-location-card")
    }

    @ViewBuilder
    private var entryRows: some View {
        ForEach(directories, id: \.self) { name in
            NavigationLink(value: WorkspaceDirectoryRoute(path: Self.join(directory, name))) {
                WorkspaceBrowserRow(
                    title: name,
                    detail: nil,
                    systemImage: "folder.fill",
                    tint: QuartetTheme.running,
                    showsDivider: name != directories.last || !files.isEmpty
                )
            }
            .buttonStyle(.plain)
            .accessibilityLabel(AppLanguage.localizedFormat("打开目录 %@", name))
            .accessibilityIdentifier("files-directory-\(name)")
        }

        ForEach(files) { file in
            Button {
                onOpenFile(Self.join(directory, file.name))
            } label: {
                WorkspaceBrowserRow(
                    title: file.name,
                    detail: fileDetail(file),
                    systemImage: "doc.text.fill",
                    tint: QuartetTheme.secondaryText,
                    showsDivider: file.id != files.last?.id
                )
            }
            .buttonStyle(.plain)
            .accessibilityLabel(AppLanguage.localizedFormat("打开文件 %@", file.name))
            .accessibilityIdentifier("files-file-\(file.name)")
        }
    }

    private var loadingCard: some View {
        HStack(spacing: 10) {
            ProgressView().tint(QuartetTheme.accent)
            Text("正在读取目录…".localizedForApp)
                .font(.quartet(.detail))
                .foregroundStyle(QuartetTheme.secondaryText)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 34)
        .accessibilityIdentifier("files-loading")
    }

    private var emptyCard: some View {
        ContentUnavailableView {
            Label("当前目录为空".localizedForApp, systemImage: "folder")
                .font(.quartet(.control, weight: .semibold))
        } description: {
            Text("这个目录下没有子目录或文件。".localizedForApp)
                .font(.quartet(.detail))
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 22)
        .accessibilityIdentifier("files-empty")
    }

    private func errorCard(_ error: PresentedError) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Label(error.title.localizedForApp, systemImage: "exclamationmark.triangle.fill")
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(QuartetTheme.failed)

            Text(error.detail)
                .font(.quartet(.compact, design: .monospaced))
                .foregroundStyle(QuartetTheme.primaryText)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)

            Button {
                Task { await load() }
            } label: {
                Label("重试".localizedForApp, systemImage: "arrow.clockwise")
                    .font(.quartet(.control, weight: .semibold))
            }
            .buttonStyle(.plain)
            .foregroundStyle(QuartetTheme.accent)
        }
        .padding(15)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(QuartetTheme.failed.opacity(0.08), in: RoundedRectangle(cornerRadius: 18))
        .overlay(RoundedRectangle(cornerRadius: 18).stroke(QuartetTheme.failed.opacity(0.22)))
        .accessibilityIdentifier("files-error")
    }

    private func load() async {
        requestGeneration += 1
        let generation = requestGeneration
        isLoading = true

        do {
            let listing = try await model.apiClient().listDirectory(path: directory)
            guard generation == requestGeneration else { return }
            directories = listing.dirs.sorted { $0.localizedStandardCompare($1) == .orderedAscending }
            files = listing.files.sorted { $0.name.localizedStandardCompare($1.name) == .orderedAscending }
            error = nil
            isLoading = false
        } catch is CancellationError {
            return
        } catch let caught {
            guard generation == requestGeneration else { return }
            if let apiError = caught as? APIError {
                error = PresentedError(title: apiError.summary, detail: apiError.detail)
            } else {
                error = PresentedError(title: "目录读取失败", detail: String(describing: caught))
            }
            isLoading = false
        }
    }

    /// 目录列表每一行都会用到这两个 formatter，复用同一份避免逐行重建。
    @MainActor
    private static let modTimeParser = ISO8601DateFormatter()

    @MainActor
    private static let modTimeFormatter: DateFormatter = {
        let formatter = DateFormatter()
        formatter.locale = .autoupdatingCurrent
        formatter.timeZone = .autoupdatingCurrent
        formatter.dateStyle = .short
        formatter.timeStyle = .short
        return formatter
    }()

    private func fileDetail(_ file: DirectoryFileEntry) -> String {
        let size = ByteCountFormatter.string(fromByteCount: file.size, countStyle: .file)
        guard let date = Self.modTimeParser.date(from: file.modTime) else { return size }
        return "\(size) · \(Self.modTimeFormatter.string(from: date))"
    }

    private static func join(_ parent: String, _ child: String) -> String {
        parent.hasSuffix("/") ? parent + child : parent + "/" + child
    }
}

/// 文件页与 Graph 路径选择器共用的目录位置说明。它沿用首页“标题 + 连续列表”的层级，
/// 不再把路径信息包进独立卡片。
struct WorkspaceBrowserLocationHeader: View {
    let path: String
    var workspaceRoot: String? = nil
    var detail: String? = nil

    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                Label("当前目录".localizedForApp, systemImage: "folder.fill")
                    .font(.quartet(.detail, weight: .semibold))
                    .foregroundStyle(QuartetTheme.secondaryText)

                Spacer(minLength: 8)

                if let detail, !detail.isEmpty {
                    Text(detail)
                        .font(.quartet(.compact, design: .monospaced))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .lineLimit(1)
                }
            }

            Text(path)
                .font(.quartet(.detail, design: .monospaced))
                .foregroundStyle(QuartetTheme.primaryText)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)

            if let workspaceRoot, workspaceRoot != path {
                HStack(alignment: .firstTextBaseline, spacing: 5) {
                    Text("当前工作空间".localizedForApp)
                    Text(workspaceRoot)
                        .textSelection(.enabled)
                        .lineLimit(2)
                        .truncationMode(.middle)
                }
                .font(.quartet(.compact, design: .monospaced))
                .foregroundStyle(QuartetTheme.secondaryText)
            }
        }
        .padding(.horizontal, 20)
        .padding(.top, 12)
        .padding(.bottom, 10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .combine)
    }
}

/// 与首页 Job 行一致的连续列表项：整行使用 surface，项目之间只用分隔线，不使用卡片描边。
struct WorkspaceBrowserRow: View {
    let title: String
    let detail: String?
    let systemImage: String
    let tint: Color
    let showsDivider: Bool

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: systemImage)
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(tint)
                .frame(width: 34, height: 34)
                .background(tint.opacity(0.1), in: RoundedRectangle(cornerRadius: 8, style: .continuous))
                .accessibilityHidden(true)

            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.quartet(.control, weight: .medium))
                    .foregroundStyle(QuartetTheme.primaryText)
                    .lineLimit(1)
                    .truncationMode(.middle)

                if let detail, !detail.isEmpty {
                    Text(detail)
                        .font(.quartet(.compact, design: .monospaced))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)

            Image(systemName: "chevron.right")
                .font(.quartet(.compact, weight: .bold))
                .foregroundStyle(QuartetTheme.secondaryText.opacity(0.72))
                .accessibilityHidden(true)
        }
        .padding(.leading, 16)
        .padding(.trailing, 14)
        .padding(.vertical, 9)
        .frame(maxWidth: .infinity, minHeight: 52, alignment: .leading)
        .contentShape(Rectangle())
        .background(QuartetTheme.surface)
        .overlay(alignment: .bottom) {
            if showsDivider {
                Divider()
                    .overlay(QuartetTheme.divider)
                    .padding(.leading, 62)
            }
        }
    }
}
