import Foundation
import SwiftUI

/// Browses files exposed by Quartet's workspace-aware file API. Directory rows
/// navigate, file rows select immediately, and the bottom action selects the
/// directory currently on screen.
struct WorkspacePathPickerView: View {
    @EnvironmentObject private var appModel: AppModel
    @Environment(\.dismiss) private var dismiss

    let variableName: String
    let workspaceRoot: String
    let initialPath: String
    var allowsFileSelection = true
    var navigationTitle: String? = nil
    var rootLabel = "当前工作空间"
    var accessibilityIdentifierPrefix = "graph-path-picker"
    let onSelect: (String) -> Void

    @State private var currentPath = ""
    @State private var parentPath: String?
    @State private var directories: [String] = []
    @State private var files: [DirectoryFileEntry] = []
    @State private var loading = true
    @State private var error: PresentedError?
    @State private var requestGeneration = 0

    private var normalizedRoot: String {
        Self.normalize(workspaceRoot)
    }

    private var visibleParent: String? {
        guard let parentPath, isAllowed(parentPath) else { return nil }
        return parentPath
    }

    private var isEmptyDirectory: Bool {
        directories.isEmpty && files.isEmpty
    }

    private var entryCountSummary: String {
        if !allowsFileSelection {
            return AppLanguage.localizedFormat("%lld 个目录", Int64(directories.count))
        }
        return AppLanguage.localizedFormat(
            "%lld 个目录 · %lld 个文件",
            Int64(directories.count),
            Int64(files.count)
        )
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 0) {
                    locationHeader

                    if loading && !isEmptyDirectory {
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
                    } else if loading && isEmptyDirectory {
                        loadingCard
                    } else {
                        browserRows
                    }
                }
                .padding(.bottom, 18)
            }
            .refreshable { await loadDirectory(currentPath.isEmpty ? normalizedRoot : currentPath) }
            .background(QuartetTheme.canvas)
            .quartetNavigationTitle(
                navigationTitle?.localizedForApp
                    ?? AppLanguage.localizedFormat("选择 %@ 的目录或文件", variableName)
            )
            .safeAreaInset(edge: .bottom, spacing: 0) {
                selectCurrentDirectoryButton
            }
        }
        .task { await loadInitialPath() }
    }

    private var locationHeader: some View {
        WorkspaceBrowserLocationHeader(
            path: currentPath.isEmpty ? normalizedRoot : currentPath,
            workspaceRoot: normalizedRoot,
            workspaceRootTitle: rootLabel,
            detail: error == nil && !loading ? entryCountSummary : nil
        )
    }

    @ViewBuilder
    private var browserRows: some View {
        if let visibleParent {
            WorkspaceBrowserRow(
                title: "返回上级目录".localizedForApp,
                detail: visibleParent,
                systemImage: "arrow.up.left",
                tint: QuartetTheme.secondaryText,
                showsDivider: !isEmptyDirectory,
                path: visibleParent,
                pathKind: .directory,
                actionAccessibilityLabel: "返回上级目录".localizedForApp,
                actionDisabled: loading,
                onOpen: { Task { await loadDirectory(visibleParent) } }
            )
        }

        ForEach(directories, id: \.self) { directory in
            let path = Self.join(currentPath, directory)
            WorkspaceBrowserRow(
                title: directory,
                detail: nil,
                systemImage: "folder.fill",
                tint: QuartetTheme.running,
                showsDivider: directory != directories.last || !files.isEmpty,
                path: path,
                pathKind: .directory,
                actionAccessibilityLabel: AppLanguage.localizedFormat("打开目录 %@", directory),
                actionDisabled: loading,
                onOpen: { Task { await loadDirectory(path) } }
            )
        }

        if allowsFileSelection {
            ForEach(files) { file in
                let path = Self.join(currentPath, file.name)
                WorkspaceBrowserRow(
                    title: file.name,
                    detail: ByteCountFormatter.string(fromByteCount: file.size, countStyle: .file),
                    systemImage: "doc.text.fill",
                    tint: QuartetTheme.secondaryText,
                    showsDivider: file.id != files.last?.id,
                    path: path,
                    pathKind: .file,
                    actionAccessibilityLabel: AppLanguage.localizedFormat("选择文件 %@", file.name),
                    actionDisabled: loading,
                    onOpen: { select(path) }
                )
            }
        }

        if isEmptyDirectory, visibleParent == nil {
            emptyCard
        } else if isEmptyDirectory {
            Text("当前目录为空".localizedForApp)
                .font(.quartet(.detail))
                .foregroundStyle(QuartetTheme.secondaryText)
                .frame(maxWidth: .infinity)
                .padding(.vertical, 28)
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
    }

    private var emptyCard: some View {
        ContentUnavailableView {
            Label("当前目录为空".localizedForApp, systemImage: "folder")
                .font(.quartet(.control, weight: .semibold))
        } description: {
            Text("仍可选择当前目录。".localizedForApp)
                .font(.quartet(.detail))
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 22)
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
                Task { await loadDirectory(currentPath.isEmpty ? normalizedRoot : currentPath) }
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
    }

    private var selectCurrentDirectoryButton: some View {
        Button { select(currentPath) } label: {
            Label("选择当前目录".localizedForApp, systemImage: "folder.badge.checkmark")
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(QuartetTheme.onAccent)
                .frame(maxWidth: .infinity)
                .frame(minHeight: 50)
                .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
        }
        .buttonStyle(.plain)
        .disabled(loading || error != nil || currentPath.isEmpty)
        .opacity(loading || error != nil || currentPath.isEmpty ? 0.45 : 1)
        .accessibilityIdentifier("\(accessibilityIdentifierPrefix)-select-directory")
        .padding(.horizontal, 18)
        .padding(.vertical, 10)
        .background(.ultraThinMaterial)
    }

    private func select(_ path: String) {
        guard !path.isEmpty, isAllowed(path) else { return }
        onSelect(path)
        dismiss()
    }

    private func loadInitialPath() async {
        let candidate = Self.normalize(initialPath)
        let start = !candidate.isEmpty && isAllowed(candidate)
            ? candidate
            : normalizedRoot
        await loadDirectory(start, retryParentForFile: start != normalizedRoot)
    }

    private func loadDirectory(_ requestedPath: String, retryParentForFile: Bool = false) async {
        let path = Self.normalize(requestedPath)
        guard !normalizedRoot.isEmpty, isAllowed(path) else {
            present(APIError(
                summary: "目录读取失败",
                detail: "请求路径不在允许浏览的目录内。\n根目录：\n\(normalizedRoot)\n\n请求路径：\n\(requestedPath)",
                requestWasRejected: true
            ))
            loading = false
            return
        }

        requestGeneration += 1
        let generation = requestGeneration
        loading = true
        error = nil

        do {
            let client = try appModel.apiClient()
            do {
                let listing = try await client.listDirectory(path: path, showFiles: allowsFileSelection)
                apply(listing, generation: generation)
            } catch {
                let parent = Self.parent(of: path)
                guard retryParentForFile,
                      parent != path,
                      isAllowed(parent) else {
                    throw error
                }
                let listing = try await client.listDirectory(path: parent, showFiles: allowsFileSelection)
                apply(listing, generation: generation)
            }
        } catch is CancellationError {
            return
        } catch {
            guard generation == requestGeneration else { return }
            present(error)
            loading = false
        }
    }

    private func apply(_ listing: DirectoryListingResponse, generation: Int) {
        guard generation == requestGeneration else { return }
        let listedPath = Self.normalize(listing.current)
        guard isAllowed(listedPath) else {
            present(APIError(
                summary: "目录读取失败",
                detail: "服务端返回了允许浏览范围之外的目录。\n根目录：\n\(normalizedRoot)\n\n返回目录：\n\(listing.current)"
            ))
            loading = false
            return
        }
        currentPath = listedPath
        if let parent = listing.parent.map({ Self.normalize($0) }), isAllowed(parent) {
            parentPath = parent
        } else {
            parentPath = nil
        }
        directories = listing.dirs.sorted { $0.localizedStandardCompare($1) == .orderedAscending }
        files = listing.files.sorted { $0.name.localizedStandardCompare($1.name) == .orderedAscending }
        error = nil
        loading = false
    }

    private func isAllowed(_ path: String) -> Bool {
        let normalizedPath = Self.normalize(path)
        guard !normalizedPath.isEmpty else { return false }
        return Self.isWithin(normalizedPath, root: normalizedRoot)
    }

    private func present(_ caught: Error) {
        if let apiError = caught as? APIError {
            error = PresentedError(title: apiError.summary, detail: apiError.detail)
        } else {
            error = PresentedError(title: "目录读取失败", detail: String(describing: caught))
        }
    }

    private static func normalize(_ rawPath: String) -> String {
        let trimmed = rawPath.trimmingCharacters(in: .whitespacesAndNewlines)
        guard trimmed.hasPrefix("/") else { return "" }
        var components: [Substring] = []
        for component in trimmed.split(separator: "/", omittingEmptySubsequences: true) {
            if component == "." { continue }
            if component == ".." {
                if !components.isEmpty { components.removeLast() }
                continue
            }
            components.append(component)
        }
        return components.isEmpty ? "/" : "/" + components.joined(separator: "/")
    }

    private static func isWithin(_ rawPath: String, root rawRoot: String) -> Bool {
        let path = normalize(rawPath)
        let root = normalize(rawRoot)
        guard !path.isEmpty, !root.isEmpty else { return false }
        return root == "/" || path == root || path.hasPrefix(root + "/")
    }

    private static func join(_ rawParent: String, _ child: String) -> String {
        let parent = normalize(rawParent)
        return normalize(parent == "/" ? "/" + child : parent + "/" + child)
    }

    private static func parent(of rawPath: String) -> String {
        let path = normalize(rawPath)
        guard path != "/", let separator = path.lastIndex(of: "/") else { return "/" }
        return separator == path.startIndex ? "/" : String(path[..<separator])
    }
}
