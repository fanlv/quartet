import Foundation
import SwiftUI
import UIKit

struct JobDetailView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.openURL) private var openURL
    let summary: JobSummary

    @State private var detail: JobDetail?
    @State private var graphState: AppModel.GraphJobState?
    @State private var loading = true
    @State private var stopping = false
    @State private var confirmsStop = false
    @State private var presentsLatestError = false
    @State private var copiedWebLink = false
    @State private var sharing = false
    @State private var copiedShareLink = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                statusHeader
                if loading {
                    HStack { Spacer(); ProgressView(); Spacer() }.padding(.top, 40)
                } else if let detail {
                    webLinkActions(detail)
                    metadata(detail)
                    if let latestError = detail.latestRunLastError ?? graphState?.lastError, !latestError.isEmpty {
                        latestErrorCard(latestError)
                    }
                    if detail.isActive { stopButton }
                }
            }
            .padding(20)
        }
        .background(QuartetTheme.canvas)
        .quartetNavigationTitle(summary.displayTitle)
        .quartetPlainNavigationBackButton()
        .task {
            if model.agentCatalogSnapshot.isEmpty {
                await model.refreshAgentCatalog()
            }
        }
        .task { await load() }
        .refreshable { await load() }
        .sheet(isPresented: $presentsLatestError) {
            NavigationStack {
                ScrollView {
                    Text(detail?.latestRunLastError ?? graphState?.lastError ?? "暂无错误详情")
                        .font(.quartet(.detail, design: .monospaced))
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(20)
                }
                .background(QuartetTheme.canvas)
                .quartetNavigationTitle("最新运行错误")
                .toolbar {
                    ToolbarItem(placement: .topBarTrailing) {
                        Button("复制") {
                            UIPasteboard.general.string = detail?.latestRunLastError ?? graphState?.lastError
                        }
                    }
                    .sharedBackgroundVisibility(.hidden)
                }
            }
            .quartetSheetStyle()
        }
        .alert("停止这个 Job？", isPresented: $confirmsStop) {
            Button("关闭", role: .cancel) {}
            Button("停止 \(summary.displayTitle)", role: .destructive) { Task { await stop() } }
        } message: {
            Text("正在执行的 Agent 或工作流将收到停止请求。")
        }
    }

    private var statusHeader: some View {
        let currentStatus = graphState?.status ?? detail?.status ?? summary.status
        let active = isActive(currentStatus)
        let statusColor = QuartetTheme.statusColor(colorKey(currentStatus))
        return VStack(alignment: .leading, spacing: 14) {
            HStack {
                Text(summary.modeLabel)
                    .font(.quartet(.compact, weight: .bold, design: .monospaced))
                    .foregroundStyle(QuartetTheme.secondaryText)
                Spacer()
                // The dashboard says "this run is live" by breathing, so the detail header says it the same
                // way rather than with a static bolt.
                Label {
                    Text(statusLabel(currentStatus))
                } icon: {
                    if active {
                        RunningBreathDot(color: statusColor, diameter: 8)
                    } else {
                        Image(systemName: "circle.fill")
                    }
                }
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(statusColor)
            }
            if summary.scheduleId != nil || detail?.scheduleId != nil {
                Text("SCHEDULE \(detail?.scheduleId ?? summary.scheduleId ?? "—")")
                    .font(.quartet(.compact, weight: .semibold, design: .monospaced))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
        }
        .padding(18)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18))
        .overlay {
            // The card's own border carries the run, the same way a dashboard row's tile border does. The
            // sliding pulse bar this replaces was a second, unrelated loading idiom for the same state, and
            // it sat inside the card while the border it shares with every other card stayed dead.
            if active {
                RunningBorderSweep(
                    color: statusColor,
                    track: QuartetTheme.divider,
                    cornerRadius: 18,
                    cornerStyle: .circular,
                    lineWidth: 1
                )
            } else {
                RoundedRectangle(cornerRadius: 18).strokeBorder(QuartetTheme.divider, lineWidth: 1)
            }
        }
    }

    private func metadata(_ detail: JobDetail) -> some View {
        VStack(spacing: 0) {
            DetailRow(label: "JOB ID", value: detail.id)
            DetailRow(label: "WORKSPACE", value: detail.workspaceId)
            DetailRow(label: "SCHEDULE", value: detail.scheduleId ?? summary.scheduleId ?? "—")
            DetailRow(label: "MODEL", value: AgentConfigurationDisplay.modelName(
                detail.firstModelId ?? summary.modelId,
                agentReference: detail.initialAgentId ?? summary.agentId,
                agents: model.agentCatalogSnapshot
            ) ?? "—")
            DetailRow(label: "STATUS", value: statusLabel(graphState?.status ?? detail.status))
            DetailRow(label: "WORKDIR", value: detail.workdir ?? "—")
            DetailRow(label: "SESSIONS", value: String(detail.sessionCount))
            DetailRow(label: "LAST EVENT", value: String(detail.lastEventSeq), showsDivider: false)
        }
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18))
        .overlay(RoundedRectangle(cornerRadius: 18).stroke(QuartetTheme.divider))
    }

    private func latestErrorCard(_ latestError: String) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Label("LATEST RUN ERROR", systemImage: "exclamationmark.triangle.fill")
                    .font(.quartet(.compact, weight: .bold, design: .monospaced))
                    .foregroundStyle(QuartetTheme.failed)
                Spacer()
                Button("复制") { UIPasteboard.general.string = latestError }
                    .font(.quartet(.compact, weight: .semibold))
            }
            Text(latestError)
                .font(.quartet(.detail))
                .foregroundStyle(QuartetTheme.secondaryText)
                .lineLimit(5)
                .textSelection(.enabled)
            Button("展开完整错误") { presentsLatestError = true }
                .font(.quartet(.control, weight: .semibold))
        }
        .padding(18)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18))
        .overlay(RoundedRectangle(cornerRadius: 18).stroke(QuartetTheme.divider))
    }

    private func webLinkActions(_ detail: JobDetail) -> some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack(alignment: .top, spacing: 12) {
                Image(systemName: "globe")
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.accentDeep)
                    .frame(width: 36, height: 36)
                    .background(QuartetTheme.accent.opacity(0.12), in: Circle())

                VStack(alignment: .leading, spacing: 4) {
                    Text("快捷操作")
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                    Text("打开或复制当前 Job 的 Web 链接，也可以生成只读分享链接")
                        .font(.quartet(.detail))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }

            Divider().overlay(QuartetTheme.divider)

            HStack(spacing: 10) {
                Button { openWebLink(for: detail) } label: {
                    HStack(spacing: 7) {
                        Image(systemName: "safari")
                        Text("浏览器打开")
                    }
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.onAccent)
                    .frame(maxWidth: .infinity, minHeight: 46)
                    .background(QuartetTheme.accentDeep, in: RoundedRectangle(cornerRadius: 12))
                }
                .buttonStyle(.plain)
                .accessibilityLabel("在浏览器中打开")
                .accessibilityHint("使用系统浏览器打开当前 Job 的 Quartet Web 页面")
                .accessibilityIdentifier("job-open-in-browser")

                Button { copyWebLink(for: detail) } label: {
                    HStack(spacing: 7) {
                        Image(systemName: copiedWebLink ? "checkmark" : "doc.on.doc")
                        Text(copiedWebLink ? "已复制" : "复制链接")
                    }
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.accentDeep)
                    .frame(maxWidth: .infinity, minHeight: 46)
                    .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12))
                    .overlay {
                        RoundedRectangle(cornerRadius: 12)
                            .stroke(QuartetTheme.divider, lineWidth: 1)
                    }
                }
                .buttonStyle(.plain)
                .accessibilityLabel(copiedWebLink ? "Web 链接已复制" : "复制 Web 链接")
                .accessibilityHint("复制可在 Quartet Web 端打开当前 Job 的链接")
                .accessibilityIdentifier("job-web-link")
            }

            if model.can("job.share") {
                Button { Task { await shareWebLink(for: detail) } } label: {
                    HStack(spacing: 7) {
                        if sharing {
                            ProgressView()
                                .tint(QuartetTheme.onAccent)
                        } else {
                            Image(systemName: copiedShareLink ? "checkmark" : "square.and.arrow.up")
                        }
                        Text(sharing ? "正在生成…" : copiedShareLink ? "分享链接已复制" : "分享")
                    }
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.onAccent)
                    .frame(maxWidth: .infinity, minHeight: 46)
                    .background(QuartetTheme.accentDeep, in: RoundedRectangle(cornerRadius: 12))
                }
                .buttonStyle(.plain)
                .disabled(sharing)
                .accessibilityLabel(
                    sharing ? "正在生成分享链接" : copiedShareLink ? "分享链接已复制" : "分享 Job"
                )
                .accessibilityHint("生成当前 Job 的只读分享链接并复制到剪贴板")
                .accessibilityIdentifier("job-share-link")
            }
        }
        .padding(18)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18))
        .overlay(RoundedRectangle(cornerRadius: 18).stroke(QuartetTheme.divider))
    }

    private var stopButton: some View {
        Button(role: .destructive) { confirmsStop = true } label: {
            HStack {
                if stopping { ProgressView() }
                Text(stopping ? "正在停止…" : "停止 Job")
                Spacer()
                Image(systemName: "stop.fill")
            }
            .font(.quartet(.regular, weight: .semibold))
            .padding(.horizontal, 18)
            .frame(height: 52)
            .background(QuartetTheme.failed.opacity(0.14), in: RoundedRectangle(cornerRadius: 14))
        }
        .disabled(stopping)
    }

    private func load() async {
        loading = true
        defer { loading = false }
        do {
            detail = try await model.jobDetail(id: summary.id)
            if summary.mode == "graph" {
                await model.refreshGraphStatus(jobID: summary.id)
                graphState = model.graphState(for: summary.id)
            }
        } catch {
            model.present(error)
        }
    }

    private func stop() async {
        stopping = true
        defer { stopping = false }
        do {
            try await model.stopJob(id: summary.id)
            await load()
        } catch {
            model.present(error)
        }
    }

    private func copyWebLink(for detail: JobDetail) {
        do {
            let url = try webURL(for: detail)
            UIPasteboard.general.string = url.absoluteString
            copiedWebLink = true
            UIAccessibility.post(notification: .announcement, argument: "Web 链接已复制")
            Task { @MainActor in
                try? await Task.sleep(for: .seconds(2))
                copiedWebLink = false
            }
        } catch {
            model.present(error)
        }
    }

    private func openWebLink(for detail: JobDetail) {
        do {
            let url = try webURL(for: detail)
            openURL(url) { accepted in
                guard !accepted else { return }
                model.present(APIError(
                    summary: "无法打开 Web 链接",
                    detail: "系统浏览器未接受当前 URL。\nURL：\n\(url.absoluteString)\n工作空间：\n\(detail.workspaceId)\nJob ID：\n\(detail.id)"
                ))
            }
        } catch {
            model.present(error)
        }
    }

    @MainActor
    private func shareWebLink(for detail: JobDetail) async {
        guard !sharing else { return }
        sharing = true
        defer { sharing = false }

        do {
            let shareToken = try await model.shareJob(id: detail.id)
            let url = try webURL(for: detail, shareToken: shareToken)
            UIPasteboard.general.string = url.absoluteString
            copiedShareLink = true
            UIAccessibility.post(notification: .announcement, argument: "分享链接已复制")
            Task { @MainActor in
                try? await Task.sleep(for: .seconds(2))
                copiedShareLink = false
            }
        } catch {
            model.present(error)
        }
    }

    private func webURL(for detail: JobDetail, shareToken: String? = nil) throws -> URL {
        let baseURL = try model.apiClient().baseURL
        guard var components = URLComponents(url: baseURL, resolvingAgainstBaseURL: false) else {
            throw APIError(
                summary: "无法生成 Web 链接",
                detail: "Quartet 服务地址无法转换为 Web URL。\n服务地址：\n\(baseURL.absoluteString)\n工作空间：\n\(detail.workspaceId)\nJob ID：\n\(detail.id)"
            )
        }
        if components.path.isEmpty {
            components.path = "/"
        }
        var queryItems = [
            URLQueryItem(name: "workspaceId", value: detail.workspaceId),
            URLQueryItem(name: "jobId", value: detail.id)
        ]
        if let shareToken {
            queryItems.append(URLQueryItem(name: "shareToken", value: shareToken))
        }
        components.queryItems = queryItems
        components.fragment = nil
        guard let url = components.url else {
            throw APIError(
                summary: "无法生成 Web 链接",
                detail: "无法为当前 Job 生成有效的 Web URL。\n服务地址：\n\(baseURL.absoluteString)\n工作空间：\n\(detail.workspaceId)\nJob ID：\n\(detail.id)"
            )
        }
        return url
    }

    private func isActive(_ status: String) -> Bool {
        ["running", "pending", "awaitingInput", "stepStopping"].contains(status)
    }

    private func statusLabel(_ status: String) -> String {
        switch status {
        case "pending": "等待中".localizedForApp
        case "running": "运行中".localizedForApp
        case "awaitingInput": "等待人工".localizedForApp
        case "stepStopping": "步骤后停止中".localizedForApp
        case "stepStopped": "已在步骤后停止".localizedForApp
        case "completed": "已完成".localizedForApp
        case "failed", "timedOut": "失败".localizedForApp
        case "stopped": "已停止".localizedForApp
        default: status
        }
    }

    private func colorKey(_ status: String) -> String {
        switch status {
        case "running", "pending", "awaitingInput", "stepStopping":
            return "running"
        case "completed":
            return "completed"
        case "failed", "timedOut":
            return "failed"
        default:
            return "stopped"
        }
    }
}

private struct DetailRow: View {
    let label: String
    let value: String
    var showsDivider = true

    var body: some View {
        VStack(spacing: 0) {
            HStack(alignment: .top, spacing: 16) {
                Text(label)
                    .font(.quartet(.compact, weight: .bold, design: .monospaced))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .frame(width: 82, alignment: .leading)
                Text(value)
                    .font(.quartet(.control))
                    .foregroundStyle(QuartetTheme.primaryText)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .padding(16)
            if showsDivider { Divider().overlay(QuartetTheme.divider).padding(.leading, 114) }
        }
    }
}
