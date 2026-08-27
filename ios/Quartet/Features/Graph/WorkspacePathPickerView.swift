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
        guard let parentPath, Self.isWithin(parentPath, root: normalizedRoot) else { return nil }
        return parentPath
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 10) {
                    locationCard

                    if loading && (!directories.isEmpty || !files.isEmpty) {
                        ProgressView()
                            .tint(QuartetTheme.accent)
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 4)
                    }

                    if let error {
                        errorCard(error)
                    } else if loading && directories.isEmpty && files.isEmpty {
                        loadingCard
                    } else {
                        browserRows
                    }
                }
                .padding(.horizontal, 18)
                .padding(.top, 10)
                .padding(.bottom, 18)
            }
            .refreshable { await loadDirectory(currentPath.isEmpty ? normalizedRoot : currentPath) }
            .background(QuartetTheme.canvas)
            .quartetNavigationTitle(AppLanguage.localizedFormat("选择 %@ 的目录或文件", variableName))
            .safeAreaInset(edge: .bottom, spacing: 0) {
                selectCurrentDirectoryButton
            }
        }
        .task { await loadInitialPath() }
    }

    private var locationCard: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label("当前工作空间".localizedForApp, systemImage: "externaldrive.fill")
                .font(.quartet(.detail, weight: .semibold))
                .foregroundStyle(QuartetTheme.secondaryText)

            Text(normalizedRoot.isEmpty ? workspaceRoot : normalizedRoot)
                .font(.quartet(.detail, design: .monospaced))
                .foregroundStyle(QuartetTheme.primaryText)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)

            if !currentPath.isEmpty, currentPath != normalizedRoot {
                Divider().overlay(QuartetTheme.divider)
                Label {
                    Text(currentPath)
                        .font(.quartet(.detail, design: .monospaced))
                        .foregroundStyle(QuartetTheme.accentDeep)
                        .textSelection(.enabled)
                        .fixedSize(horizontal: false, vertical: true)
                } icon: {
                    Image(systemName: "location.fill")
                        .foregroundStyle(QuartetTheme.accent)
                }
            }
        }
        .padding(15)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(QuartetTheme.divider))
    }

    @ViewBuilder
    private var browserRows: some View {
        if let visibleParent {
            pathRow(
                title: "返回上级目录".localizedForApp,
                detail: visibleParent,
                systemImage: "arrow.up.left",
                tint: QuartetTheme.secondaryText
            ) {
                Task { await loadDirectory(visibleParent) }
            }
        }

        ForEach(directories, id: \.self) { directory in
            let path = Self.join(currentPath, directory)
            pathRow(
                title: directory,
                detail: nil,
                systemImage: "folder.fill",
                tint: QuartetTheme.running
            ) {
                Task { await loadDirectory(path) }
            }
            .accessibilityLabel(AppLanguage.localizedFormat("打开目录 %@", directory))
        }

        ForEach(files) { file in
            let path = Self.join(currentPath, file.name)
            pathRow(
                title: file.name,
                detail: ByteCountFormatter.string(fromByteCount: file.size, countStyle: .file),
                systemImage: "doc.fill",
                tint: QuartetTheme.secondaryText
            ) {
                select(path)
            }
            .accessibilityLabel(AppLanguage.localizedFormat("选择文件 %@", file.name))
        }

        if directories.isEmpty, files.isEmpty, visibleParent == nil {
            emptyCard
        } else if directories.isEmpty, files.isEmpty {
            Text("当前目录为空".localizedForApp)
                .font(.quartet(.detail))
                .foregroundStyle(QuartetTheme.secondaryText)
                .frame(maxWidth: .infinity)
                .padding(.vertical, 28)
        }
    }

    private func pathRow(
        title: String,
        detail: String?,
        systemImage: String,
        tint: Color,
        action: @escaping () -> Void
    ) -> some View {
        Button(action: action) {
            HStack(spacing: 12) {
                Image(systemName: systemImage)
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(tint)
                    .frame(width: 38, height: 38)
                    .background(tint.opacity(0.1), in: RoundedRectangle(cornerRadius: 11, style: .continuous))
                    .accessibilityHidden(true)

                VStack(alignment: .leading, spacing: 3) {
                    Text(title)
                        .font(.quartet(.control, weight: .medium))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .lineLimit(2)
                    if let detail, !detail.isEmpty {
                        Text(detail)
                            .font(.quartet(.compact, design: .monospaced))
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .lineLimit(2)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                Image(systemName: "chevron.right")
                    .font(.quartet(.detail, weight: .bold))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .accessibilityHidden(true)
            }
            .padding(13)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(loading)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 16, style: .continuous).stroke(QuartetTheme.divider))
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
        .accessibilityIdentifier("graph-path-picker-select-directory")
        .padding(.horizontal, 18)
        .padding(.vertical, 10)
        .background(.ultraThinMaterial)
    }

    private func select(_ path: String) {
        guard !path.isEmpty, Self.isWithin(path, root: normalizedRoot) else { return }
        onSelect(path)
        dismiss()
    }

    private func loadInitialPath() async {
        let candidate = Self.normalize(initialPath)
        let start = !candidate.isEmpty && Self.isWithin(candidate, root: normalizedRoot)
            ? candidate
            : normalizedRoot
        await loadDirectory(start, retryParentForFile: start != normalizedRoot)
    }

    private func loadDirectory(_ requestedPath: String, retryParentForFile: Bool = false) async {
        let path = Self.normalize(requestedPath)
        guard !normalizedRoot.isEmpty, Self.isWithin(path, root: normalizedRoot) else {
            present(APIError(
                summary: "目录读取失败",
                detail: "请求路径不在当前工作空间内。\n工作空间：\n\(normalizedRoot)\n\n请求路径：\n\(requestedPath)",
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
                let listing = try await client.listDirectory(path: path)
                apply(listing, generation: generation)
            } catch {
                let parent = Self.parent(of: path)
                guard retryParentForFile,
                      parent != path,
                      Self.isWithin(parent, root: normalizedRoot) else {
                    throw error
                }
                let listing = try await client.listDirectory(path: parent)
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
        guard Self.isWithin(listedPath, root: normalizedRoot) else {
            present(APIError(
                summary: "目录读取失败",
                detail: "服务端返回了当前工作空间之外的目录。\n工作空间：\n\(normalizedRoot)\n\n返回目录：\n\(listing.current)"
            ))
            loading = false
            return
        }
        currentPath = listedPath
        if let parent = listing.parent.map({ Self.normalize($0) }), Self.isWithin(parent, root: normalizedRoot) {
            parentPath = parent
        } else {
            parentPath = nil
        }
        directories = listing.dirs.sorted { $0.localizedStandardCompare($1) == .orderedAscending }
        files = listing.files.sorted { $0.name.localizedStandardCompare($1.name) == .orderedAscending }
        error = nil
        loading = false
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
