import SwiftUI
import UIKit

struct JobDetailView: View {
    @EnvironmentObject private var model: AppModel
    let summary: JobSummary

    @State private var detail: JobDetail?
    @State private var graphState: AppModel.GraphJobState?
    @State private var loading = true
    @State private var stopping = false
    @State private var confirmsStop = false
    @State private var presentsLatestError = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                statusHeader
                if loading {
                    HStack { Spacer(); ProgressView(); Spacer() }.padding(.top, 40)
                } else if let detail {
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
        .navigationTitle(summary.displayTitle)
        .navigationBarTitleDisplayMode(.inline)
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
                        .font(.system(.footnote, design: .monospaced))
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(20)
                }
                .background(QuartetTheme.canvas)
                .navigationTitle("最新运行错误")
                .navigationBarTitleDisplayMode(.inline)
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
            Button("取消", role: .cancel) {}
            Button("停止 \(summary.displayTitle)", role: .destructive) { Task { await stop() } }
        } message: {
            Text("正在执行的 Agent 或工作流将收到停止请求。")
        }
    }

    private var statusHeader: some View {
        let currentStatus = graphState?.status ?? detail?.status ?? summary.status
        return VStack(alignment: .leading, spacing: 14) {
            HStack {
                Text(summary.modeLabel)
                    .font(.system(.caption, design: .monospaced).weight(.bold))
                    .foregroundStyle(QuartetTheme.secondaryText)
                Spacer()
                Label(statusLabel(currentStatus), systemImage: isActive(currentStatus) ? "bolt.fill" : "circle.fill")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(QuartetTheme.statusColor(colorKey(currentStatus)))
            }
            if summary.scheduleId != nil || detail?.scheduleId != nil {
                Text("SCHEDULE \(detail?.scheduleId ?? summary.scheduleId ?? "—")")
                    .font(.system(.caption, design: .monospaced).weight(.semibold))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
            RunningPulseLine(active: isActive(currentStatus))
        }
        .padding(18)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18))
        .overlay(RoundedRectangle(cornerRadius: 18).stroke(QuartetTheme.divider))
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
                    .font(.system(.caption, design: .monospaced).weight(.bold))
                    .foregroundStyle(QuartetTheme.failed)
                Spacer()
                Button("复制") { UIPasteboard.general.string = latestError }
                    .font(.caption.weight(.semibold))
            }
            Text(latestError)
                .font(.footnote)
                .foregroundStyle(QuartetTheme.secondaryText)
                .lineLimit(5)
                .textSelection(.enabled)
            Button("展开完整错误") { presentsLatestError = true }
                .font(.subheadline.weight(.semibold))
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
            .font(.headline)
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

    private func isActive(_ status: String) -> Bool {
        ["running", "pending", "awaitingInput", "stepStopping"].contains(status)
    }

    private func statusLabel(_ status: String) -> String {
        switch status {
        case "pending": "等待中"
        case "running": "运行中"
        case "awaitingInput": "等待人工"
        case "stepStopping": "步骤后停止中"
        case "stepStopped": "已在步骤后停止"
        case "completed": "已完成"
        case "failed", "timedOut": "失败"
        case "stopped": "已停止"
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
                    .font(.system(size: 10, weight: .bold, design: .monospaced))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .frame(width: 82, alignment: .leading)
                Text(value)
                    .font(.system(.subheadline, design: .monospaced))
                    .foregroundStyle(QuartetTheme.primaryText)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .padding(16)
            if showsDivider { Divider().overlay(QuartetTheme.divider).padding(.leading, 114) }
        }
    }
}
