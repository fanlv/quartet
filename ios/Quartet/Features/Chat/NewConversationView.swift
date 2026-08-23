import AVFoundation
import SwiftUI
import PhotosUI
import UniformTypeIdentifiers
import UIKit

struct ChatRoute: Hashable {
    let summary: JobSummary
    var initialMessage: String?
    var initialAttachment: PendingUpload?
    var agentType: String?
    var modelID: String?
    var modeID: String?
    var thoughtLevelID: String?
    var initialImagePaths: [String]?
    var targetSessionID: String?

    init(
        summary: JobSummary,
        initialMessage: String? = nil,
        initialAttachment: PendingUpload? = nil,
        agentType: String? = nil,
        modelID: String? = nil,
        modeID: String? = nil,
        thoughtLevelID: String? = nil,
        initialImagePaths: [String]? = nil,
        targetSessionID: String? = nil
    ) {
        self.summary = summary
        self.initialMessage = initialMessage
        self.initialAttachment = initialAttachment
        self.agentType = agentType
        self.modelID = modelID
        self.modeID = modeID
        self.thoughtLevelID = thoughtLevelID
        self.initialImagePaths = initialImagePaths
        self.targetSessionID = targetSessionID
    }
}

private struct CreateJobIntentPayload: Equatable {
    let workspaceID: String
    let agentType: String
    let modelID: String
    let modeID: String?
    let thoughtLevelID: String?
    let workdir: String
}

private struct CreateJobIntent {
    let id: String
    let payload: CreateJobIntentPayload
}

private enum NewConversationMode: String, CaseIterable, Identifiable {
    case chat
    case graph

    var id: String { rawValue }
    var title: String { self == .chat ? "对话" : "Graph Workflow" }
    var icon: String { self == .chat ? "bubble.left.and.bubble.right" : "point.3.connected.trianglepath.dotted" }
}

struct NewConversationView: View {
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var model: AppModel
    let onCreated: (ChatRoute) -> Void

    @State private var agents: [AgentSummary] = []
    @State private var agentPreferences: [String: AgentPreferences] = [:]
    @State private var creationMode: NewConversationMode = .chat
    @State private var workspaceID = ""
    @State private var agentID = ""
    @State private var modelID = ""
    @State private var modeID = ""
    @State private var thoughtLevelID = ""
    @State private var message = ""
    @State private var sentMessageHistory: [SentMessageHistoryItem] = []
    @State private var selectedPhoto: PhotosPickerItem?
    @State private var pendingImage: PendingUpload?
    @State private var showsCameraPicker = false
    @State private var showsDocumentPicker = false
    @State private var showsSentMessageHistory = false
    @State private var showsAdvancedOptions = false
    @State private var loading = true
    @State private var creating = false
    @State private var waitingForValidation = false
    @State private var validationAttempt = 0
    @State private var validationTimedOut = false
    @State private var savesDefaults = false
    @State private var localError: PresentedError?
    @State private var createIntent: CreateJobIntent?
    @FocusState private var composerFocused: Bool

    private var workspace: WorkspaceSummary? { model.workspaces.first { $0.id == workspaceID } }
    private var agent: AgentSummary? { agents.first { $0.id == agentID } }
    private var agentName: String {
        guard let agent else { return "选择 Agent" }
        return agent.displayName.isEmpty ? agent.type : agent.displayName
    }
    private var modelName: String {
        agent?.models?.availableModels.first(where: { $0.modelId == modelID })?.name
            ?? (modelID.isEmpty ? "选择模型" : modelID)
    }
    private var modeName: String {
        agent?.modes?.availableModes.first(where: { $0.id == modeID })?.name ?? "跟随 Agent"
    }
    private var thoughtLevelName: String {
        agent?.thoughtLevels?.availableThoughtLevels.first(where: { $0.id == thoughtLevelID })?.name ?? "跟随 Agent"
    }
    private var hasAdvancedOptions: Bool {
        agent?.modes?.availableModes.isEmpty == false || agent?.thoughtLevels?.availableThoughtLevels.isEmpty == false
    }
    private var cannotCreate: Bool {
        creating
            || workspaceID.isEmpty
            || agent?.available != true
            || (message.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && pendingImage == nil)
    }
    private var currentCreatePayload: CreateJobIntentPayload? {
        guard let workspace, let agent else { return nil }
        return CreateJobIntentPayload(
            workspaceID: workspace.id,
            agentType: agent.type,
            modelID: modelID,
            modeID: modeID.isEmpty ? nil : modeID,
            thoughtLevelID: thoughtLevelID.isEmpty ? nil : thoughtLevelID,
            workdir: workspace.workdir
        )
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                modeSelector

                if creationMode == .chat {
                    ScrollView {
                        VStack(alignment: .leading, spacing: 20) {
                            if loading {
                                loadingState
                            } else {
                                composer
                                configurationSection
                                agentStatusNotice
                            }
                        }
                        .padding(.horizontal, 18)
                        .padding(.top, 12)
                        .padding(.bottom, 24)
                    }
                    .scrollDismissesKeyboard(.interactively)
                } else {
                    GraphWorkflowLaunchView(onCreated: onCreated)
                }
            }
            .background(QuartetTheme.canvas)
            .navigationTitle("新任务")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("取消") { dismiss() } }
            }
            .task(id: creationMode) {
                if creationMode == .chat, agents.isEmpty {
                    await load()
                }
            }
            .task(id: workspaceID) {
                guard creationMode == .chat, !workspaceID.isEmpty else { return }
                loadSentMessageHistory()
            }
            .onChange(of: currentCreatePayload) { _, payload in
                if createIntent?.payload != payload {
                    createIntent = nil
                }
            }
            .onChange(of: selectedPhoto) { _, item in
                guard let item else { return }
                Task { await loadPhoto(item) }
            }
            .sheet(item: $localError) { ErrorDetailView(error: $0) }
            .sheet(isPresented: $showsCameraPicker) {
                CameraImagePicker(
                    onImagePicked: { image in
                        showsCameraPicker = false
                        setCameraImage(image)
                    },
                    onCancel: { showsCameraPicker = false }
                )
            }
            .sheet(isPresented: $showsDocumentPicker) {
                DocumentImagePicker(
                    onDocumentPicked: { url in
                        showsDocumentPicker = false
                        Task { await loadDocument(url) }
                    },
                    onCancel: { showsDocumentPicker = false }
                )
            }
            .safeAreaInset(edge: .bottom, spacing: 0) {
                if creationMode == .chat, !loading { actionBar }
            }
        }
    }

    private var modeSelector: some View {
        HStack(spacing: 8) {
            ForEach(NewConversationMode.allCases) { item in
                let selected = creationMode == item
                Button {
                    composerFocused = false
                    withAnimation(.easeInOut(duration: 0.2)) { creationMode = item }
                } label: {
                    Label(item.title, systemImage: item.icon)
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(selected ? QuartetTheme.primaryText : QuartetTheme.secondaryText)
                        .frame(maxWidth: .infinity)
                        .frame(height: 42)
                        .background(selected ? QuartetTheme.surface : Color.clear, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                        .shadow(color: selected ? Color.black.opacity(0.08) : .clear, radius: 8, y: 3)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(item == .chat ? "创建普通任务" : "创建 Graph Workflow 任务")
                .accessibilityAddTraits(selected ? .isSelected : [])
            }
        }
        .padding(4)
        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        .padding(.horizontal, 18)
        .padding(.top, 10)
        .padding(.bottom, 2)
    }

    private var loadingState: some View {
        VStack(spacing: 16) {
            ZStack {
                Circle()
                    .fill(QuartetTheme.accent.opacity(0.12))
                    .frame(width: 64, height: 64)
                ProgressView()
                    .tint(QuartetTheme.accent)
                    .controlSize(.large)
            }
            Text("正在准备任务")
                .font(.quartet(.regular, weight: .semibold))
                .foregroundStyle(QuartetTheme.primaryText)
            Text("正在读取空间与 Agent 配置…")
                .font(.quartet(.control))
                .foregroundStyle(QuartetTheme.secondaryText)
        }
        .frame(maxWidth: .infinity)
        .padding(.top, 90)
    }

    private var composer: some View {
        let hasPendingImage = pendingImage != nil
        return VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 12) {
                Text("第一条消息")
                    .font(.quartet(.regular, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                Spacer()
                Button {
                    composerFocused = false
                    loadSentMessageHistory()
                    withAnimation(.easeInOut(duration: 0.2)) {
                        showsSentMessageHistory.toggle()
                    }
                } label: {
                    Label(showsSentMessageHistory ? "收起历史" : "历史消息", systemImage: "clock.arrow.circlepath")
                        .font(.quartet(.detail, weight: .semibold))
                }
                .buttonStyle(.plain)
                .foregroundStyle(QuartetTheme.accent)
                .accessibilityIdentifier("new-task-message-history")
                Text(message.isEmpty ? "支持文字与图片" : "\(message.count) 字")
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }

            if showsSentMessageHistory {
                sentMessageHistoryPicker
            }

            ZStack(alignment: .topLeading) {
                if message.isEmpty {
                    Text("描述你想完成的事情…")
                        .font(.quartet(.regular))
                        .foregroundStyle(QuartetTheme.secondaryText.opacity(0.8))
                        .padding(.horizontal, 5)
                        .padding(.vertical, 8)
                        .allowsHitTesting(false)
                }
                TextEditor(text: $message)
                    .font(.quartet(.regular))
                    .scrollContentBackground(.hidden)
                    .focused($composerFocused)
                    .frame(minHeight: 148)
                    .accessibilityIdentifier("new-conversation-message")
            }

            if let pendingImage {
                ChatAttachmentPreview(upload: pendingImage)
                    .overlay(alignment: .topTrailing) {
                        Button {
                            self.pendingImage = nil
                            selectedPhoto = nil
                        } label: {
                            Image(systemName: "xmark")
                                .font(.quartet(.detail, weight: .bold))
                                .foregroundStyle(QuartetTheme.primaryText)
                                .frame(width: 28, height: 28)
                                .background(QuartetTheme.elevated, in: Circle())
                        }
                        .accessibilityLabel("移除图片")
                        .padding(8)
                    }
            }

            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 8) {
                    PhotosPicker(selection: $selectedPhoto, matching: .images) {
                        Label("照片", systemImage: hasPendingImage ? "photo.fill" : "photo")
                            .font(.quartet(.control, weight: .semibold))
                            .padding(.horizontal, 12)
                            .frame(height: 36)
                            .background(QuartetTheme.elevated, in: Capsule())
                    }
                    .foregroundStyle(hasPendingImage ? QuartetTheme.accent : QuartetTheme.secondaryText)
                    Button { requestCameraAccess() } label: {
                        attachmentActionLabel("相机", icon: "camera")
                    }
                    Button { showsDocumentPicker = true } label: {
                        attachmentActionLabel("文件", icon: "folder")
                    }
                }
            }
        }
        .padding(16)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 22, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 22, style: .continuous)
                .stroke(composerFocused ? QuartetTheme.accent.opacity(0.75) : QuartetTheme.divider, lineWidth: composerFocused ? 1.5 : 1)
        )
        .shadow(color: Color.black.opacity(composerFocused ? 0.08 : 0.03), radius: 18, y: 8)
        .animation(.easeOut(duration: 0.18), value: composerFocused)
    }

    @ViewBuilder
    private var sentMessageHistoryPicker: some View {
        if sentMessageHistory.isEmpty {
            Text("暂无发送历史")
                .font(.quartet(.detail))
                .foregroundStyle(QuartetTheme.secondaryText)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(12)
                .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12))
        } else {
            ScrollView(.horizontal, showsIndicators: false) {
                LazyHStack(spacing: 10) {
                    ForEach(sentMessageHistory) { item in
                        Button {
                            message = item.content
                            withAnimation(.easeInOut(duration: 0.2)) {
                                showsSentMessageHistory = false
                            }
                            composerFocused = true
                        } label: {
                            VStack(alignment: .leading, spacing: 7) {
                                Text(item.content)
                                    .font(.quartet(.control))
                                    .foregroundStyle(QuartetTheme.primaryText)
                                    .lineLimit(3)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                                Text(Date(timeIntervalSince1970: Double(item.createdAt) / 1_000)
                                    .formatted(date: .abbreviated, time: .shortened))
                                    .font(.quartet(.compact))
                                    .foregroundStyle(QuartetTheme.secondaryText)
                            }
                            .padding(12)
                            .frame(width: 250, alignment: .leading)
                            .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12))
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel(item.content)
                        .accessibilityHint("选择后可继续修改")
                        .accessibilityIdentifier("sent-message-history-item-\(item.id)")
                    }
                }
            }
        }
    }

    private func attachmentActionLabel(_ title: String, icon: String) -> some View {
        Label(title, systemImage: icon)
            .font(.quartet(.control, weight: .semibold))
            .padding(.horizontal, 12)
            .frame(height: 36)
            .background(QuartetTheme.elevated, in: Capsule())
    }

    private var configurationSection: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .firstTextBaseline) {
                Text("运行配置")
                    .font(.quartet(.regular, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                Spacer()
                Text("按空间记忆")
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }

            workspacePicker

            VStack(spacing: 0) {
                agentPicker
                rowDivider
                modelPicker

                if hasAdvancedOptions {
                    rowDivider
                    Button {
                        withAnimation(.easeInOut(duration: 0.2)) {
                            showsAdvancedOptions.toggle()
                        }
                    } label: {
                        HStack(spacing: 12) {
                            configurationIcon("slider.horizontal.3")
                            VStack(alignment: .leading, spacing: 2) {
                                Text("高级选项")
                                    .font(.quartet(.control, weight: .semibold))
                                    .foregroundStyle(QuartetTheme.primaryText)
                                Text("\(modeName) · \(thoughtLevelName)")
                                    .font(.quartet(.detail))
                                    .foregroundStyle(QuartetTheme.secondaryText)
                                    .lineLimit(1)
                            }
                            Spacer()
                            Image(systemName: "chevron.down")
                                .font(.quartet(.detail, weight: .bold))
                                .foregroundStyle(QuartetTheme.secondaryText)
                                .rotationEffect(.degrees(showsAdvancedOptions ? 180 : 0))
                        }
                        .padding(.horizontal, 14)
                        .frame(minHeight: 60)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)

                    if showsAdvancedOptions {
                        if agent?.modes?.availableModes.isEmpty == false {
                            rowDivider.padding(.leading, 58)
                            modePicker
                        }
                        if agent?.thoughtLevels?.availableThoughtLevels.isEmpty == false {
                            rowDivider.padding(.leading, 58)
                            thoughtLevelPicker
                        }
                    }
                }

                rowDivider
                Toggle(isOn: $savesDefaults) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text("设为空间默认")
                            .font(.quartet(.control, weight: .semibold))
                            .foregroundStyle(QuartetTheme.primaryText)
                        Text("创建成功时保存当前 Agent 与模型")
                            .font(.quartet(.detail))
                            .foregroundStyle(QuartetTheme.secondaryText)
                    }
                }
                .tint(QuartetTheme.accent)
                .padding(.horizontal, 14)
                .frame(minHeight: 64)
            }
            .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 20, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 20, style: .continuous).stroke(QuartetTheme.divider))
        }
    }

    private var workspacePicker: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 10) {
                ForEach(model.workspaces) { item in
                    let selected = workspaceID == item.id
                    Button { workspaceID = item.id } label: {
                        HStack(spacing: 9) {
                            Circle()
                                .fill(workspaceTint(item))
                                .frame(width: 10, height: 10)
                            Text(item.displayName)
                                .font(.quartet(.control, weight: .semibold))
                                .lineLimit(1)
                            if selected {
                                Image(systemName: "checkmark")
                                    .font(.quartet(.detail, weight: .bold))
                            }
                        }
                        .foregroundStyle(selected ? QuartetTheme.primaryText : QuartetTheme.secondaryText)
                        .padding(.horizontal, 14)
                        .frame(height: 44)
                        .background(selected ? QuartetTheme.accent.opacity(0.14) : QuartetTheme.surface, in: Capsule())
                        .overlay(Capsule().stroke(selected ? QuartetTheme.accent.opacity(0.7) : QuartetTheme.divider))
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("工作空间，\(item.displayName)\(selected ? "，已选择" : "")")
                }
            }
            .padding(.vertical, 1)
        }
        .onChange(of: workspaceID) { _, _ in applyWorkspaceDefaults() }
    }

    private var agentPicker: some View {
        Menu {
            ForEach(agents) { item in
                let name = item.displayName.isEmpty ? item.type : item.displayName
                Button { selectAgent(item.id) } label: {
                    if item.id == agentID {
                        Label(name, systemImage: "checkmark")
                    } else {
                        Text(item.available ? name : "\(name) · \(item.availabilityLabel)")
                    }
                }
                .disabled(!item.available && item.id != agentID)
            }
        } label: {
            configurationRow(title: "Agent", value: agentName, icon: "command")
        }
        .buttonStyle(.plain)
    }

    private var modelPicker: some View {
        Menu {
            ForEach(orderedModels) { item in
                Button { modelID = item.modelId } label: {
                    if item.modelId == modelID {
                        Label(item.name, systemImage: "checkmark")
                    } else if favoriteModelIDs.contains(item.modelId) {
                        Label(item.name, systemImage: "star.fill")
                    } else {
                        Text(item.name)
                    }
                }
            }
        } label: {
            configurationRow(title: "模型", value: modelName, icon: "cpu")
        }
        .buttonStyle(.plain)
        .disabled(orderedModels.isEmpty)
    }

    private var modePicker: some View {
        Menu {
            ForEach(agent?.modes?.availableModes ?? []) { item in
                Button { modeID = item.id } label: {
                    item.id == modeID ? Label(item.name, systemImage: "checkmark") : Label(item.name, systemImage: "circle")
                }
            }
        } label: {
            configurationRow(title: "模式", value: modeName, icon: "point.3.connected.trianglepath.dotted")
        }
        .buttonStyle(.plain)
    }

    private var thoughtLevelPicker: some View {
        Menu {
            ForEach(agent?.thoughtLevels?.availableThoughtLevels ?? []) { item in
                Button { thoughtLevelID = item.id } label: {
                    item.id == thoughtLevelID ? Label(item.name, systemImage: "checkmark") : Label(item.name, systemImage: "circle")
                }
            }
        } label: {
            configurationRow(title: "思考等级", value: thoughtLevelName, icon: "brain")
        }
        .buttonStyle(.plain)
    }

    private func configurationRow(title: String, value: String, icon: String) -> some View {
        HStack(spacing: 12) {
            configurationIcon(icon)
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
                Text(value)
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                    .lineLimit(1)
            }
            Spacer()
            Image(systemName: "chevron.up.chevron.down")
                .font(.quartet(.compact, weight: .bold))
                .foregroundStyle(QuartetTheme.secondaryText)
        }
        .padding(.horizontal, 14)
        .frame(minHeight: 60)
        .contentShape(Rectangle())
    }

    private func configurationIcon(_ name: String) -> some View {
        Image(systemName: name)
            .font(.quartet(.control, weight: .semibold))
            .foregroundStyle(QuartetTheme.accent)
            .frame(width: 32, height: 32)
            .background(QuartetTheme.accent.opacity(0.11), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }

    private var actionBar: some View {
        VStack(spacing: 10) {
            HStack(spacing: 8) {
                contextPill(workspace?.displayName ?? "未选择空间", icon: "square.stack.3d.up")
                contextPill(agentName, icon: "command")
                contextPill(modelName, icon: "cpu")
            }

            Button { Task { await create() } } label: {
                HStack(spacing: 10) {
                    if creating {
                        ProgressView().tint(.black)
                    } else {
                        Image(systemName: "arrow.up.circle.fill")
                    }
                    Text(creating ? "正在创建…" : "创建并发送")
                    Spacer()
                    Image(systemName: "chevron.right")
                        .font(.quartet(.detail, weight: .bold))
                }
                .font(.quartet(.regular, weight: .semibold))
                .foregroundStyle(.black)
                .padding(.horizontal, 18)
                .frame(height: 54)
                .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
            }
            .disabled(cannotCreate)
            .opacity(cannotCreate ? 0.45 : 1)
            .accessibilityIdentifier("new-conversation-create")
        }
        .padding(.horizontal, 18)
        .padding(.top, 10)
        .padding(.bottom, 8)
        .background(.ultraThinMaterial)
        .overlay(alignment: .top) { Rectangle().fill(QuartetTheme.divider).frame(height: 0.5) }
    }

    private func contextPill(_ value: String, icon: String) -> some View {
        Label(value, systemImage: icon)
            .font(.quartet(.detail, weight: .medium))
            .foregroundStyle(QuartetTheme.secondaryText)
            .lineLimit(1)
            .padding(.horizontal, 9)
            .frame(height: 26)
            .background(QuartetTheme.elevated, in: Capsule())
    }

    @ViewBuilder
    private var agentStatusNotice: some View {
        if let agent, !agent.available {
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 8) {
                    if agent.isValidationPending && waitingForValidation {
                        ProgressView().controlSize(.small)
                    } else {
                        Image(systemName: agent.isValidationPending ? "clock" : "exclamationmark.triangle.fill")
                    }
                    Text(agent.isValidationPending
                        ? (validationTimedOut ? "Agent 验证尚未完成" : "Agent \(agent.availabilityLabel)，正在自动重试")
                        : "Agent 当前不可用")
                        .font(.quartet(.control, weight: .semibold))
                }
                if let error = agent.error, !error.isEmpty {
                    Text(error)
                        .font(.quartet(.detail))
                        .foregroundStyle(QuartetTheme.failed)
                        .textSelection(.enabled)
                }
                if !waitingForValidation {
                    Button("重新检查") { Task { await load(preserveSelection: true) } }
                        .font(.quartet(.detail, weight: .semibold))
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(14)
            .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12))
        }
    }

    private var rowDivider: some View { Divider().overlay(QuartetTheme.divider) }

    private func load(preserveSelection: Bool = false) async {
        loading = true
        waitingForValidation = false
        validationTimedOut = false
        defer { loading = false }
        do {
            agentPreferences = try await model.agentPreferences()
        } catch {
            present(error)
            agentPreferences = [:]
        }
        do {
            var didInitializeSelection = preserveSelection && !agentID.isEmpty
            var receivedSnapshot = false
            for attempt in 1...15 {
                try Task.checkCancellation()
                let snapshot = try await model.agentCatalog()
                receivedSnapshot = true
                guard !snapshot.isEmpty else {
                    throw APIError(summary: "没有可用的 Agent", detail: "GET /api/v1/agent/list 返回空列表。")
                }
                let selectedWasAvailable = agent?.available == true
                agents = snapshot
                if !didInitializeSelection || !snapshot.contains(where: { $0.id == agentID }) {
                    workspaceID = model.selectedWorkspaceID ?? model.workspaces.first?.id ?? ""
                    applyWorkspaceDefaults()
                    didInitializeSelection = true
                } else if !selectedWasAvailable, agent?.available == true {
                    applyAgentDefaults(includeWorkspaceDefault: true)
                }
                loading = false

                let hasPending = snapshot.contains(where: \.isValidationPending)
                if !hasPending {
                    waitingForValidation = false
                    return
                }
                waitingForValidation = true
                validationAttempt = attempt
                try await Task.sleep(for: .seconds(2))
            }
            waitingForValidation = false
            validationTimedOut = receivedSnapshot
        } catch is CancellationError {
            return
        } catch {
            waitingForValidation = false
            present(error)
        }
    }

    private func applyWorkspaceDefaults() {
        let requested = workspace?.defaultAgent
        let fallback = agents.first(where: \.available) ?? agents.first
        if let requested {
            agentID = agents.first(where: {
                $0.available && ($0.type == requested || $0.agentId == requested)
            })?.id ?? fallback?.id ?? ""
        } else {
            agentID = fallback?.id ?? ""
        }
        applyAgentDefaults(includeWorkspaceDefault: true)
    }

    private func selectAgent(_ id: String) {
        agentID = id
        applyAgentDefaults(includeWorkspaceDefault: false)
    }

    private func applyAgentDefaults(includeWorkspaceDefault: Bool = false) {
        guard let agent else { return }
        let preferences = agentPreferences[agent.agentId] ?? agentPreferences[agent.type]
        let availableModels = agent.models?.availableModels ?? []
        let workspaceMatchesAgent = workspace?.defaultAgent == agent.type || workspace?.defaultAgent == agent.agentId
        let workspaceModel = includeWorkspaceDefault && workspaceMatchesAgent ? workspace?.defaultModel : nil
        modelID = validID(workspaceModel, in: availableModels.map(\.modelId))
            ?? validID(preferences?.defaultModelID, in: availableModels.map(\.modelId))
            ?? availableModels.first?.modelId
            ?? agent.modelId

        let availableModes = agent.modes?.availableModes.map(\.id) ?? []
        modeID = validID(preferences?.defaultMode, in: availableModes)
            ?? validID(agent.modes?.currentModeId, in: availableModes)
            ?? ""

        let availableThoughtLevels = agent.thoughtLevels?.availableThoughtLevels.map(\.id) ?? []
        thoughtLevelID = validID(preferences?.defaultThoughtLevel, in: availableThoughtLevels)
            ?? validID(agent.thoughtLevels?.currentThoughtLevelId, in: availableThoughtLevels)
            ?? ""
    }

    private func validID(_ candidate: String?, in available: [String]) -> String? {
        guard let candidate, available.contains(candidate) else { return nil }
        return candidate
    }

    private var favoriteModelIDs: Set<String> {
        guard let agent else { return [] }
        return Set((agentPreferences[agent.agentId] ?? agentPreferences[agent.type])?.favoriteModelIDs ?? [])
    }

    private var orderedModels: [AgentModel] {
        let available = agent?.models?.availableModels ?? []
        let favoriteOrder = (agent.flatMap { agentPreferences[$0.agentId] ?? agentPreferences[$0.type] })?.favoriteModelIDs ?? []
        let byID = Dictionary(available.map { ($0.modelId, $0) }, uniquingKeysWith: { first, _ in first })
        let favorites = favoriteOrder.compactMap { byID[$0] }
        let favoriteSet = Set(favoriteOrder)
        return favorites + available.filter { !favoriteSet.contains($0.modelId) }
    }

    private func workspaceTint(_ item: WorkspaceSummary) -> Color {
        guard let raw = item.color?.trimmingCharacters(in: CharacterSet(charactersIn: "#")),
              raw.count == 6,
              let value = UInt64(raw, radix: 16) else { return QuartetTheme.accent }
        return Color(
            red: Double((value >> 16) & 0xff) / 255,
            green: Double((value >> 8) & 0xff) / 255,
            blue: Double(value & 0xff) / 255
        )
    }

    private func create() async {
        guard let workspace, let agent, agent.available, let payload = currentCreatePayload else { return }
        creating = true
        defer { creating = false }
        do {
            if savesDefaults {
                try await model.saveWorkspaceDefaults(
                    workspaceID: workspace.id,
                    agent: agent.type,
                    model: modelID
                )
            }
            if createIntent?.payload != payload {
                createIntent = CreateJobIntent(
                    id: UUID().uuidString.lowercased(),
                    payload: payload
                )
            }
            guard let createIntent else { return }
            let request = CreateJobRequest(
                modelId: payload.modelID,
                agentType: payload.agentType,
                acpMode: payload.modeID,
                acpThoughtLevel: payload.thoughtLevelID,
                workdir: payload.workdir,
                workspaceId: payload.workspaceID,
                clientMessageId: createIntent.id
            )
            let jobID = try await model.createJob(request: request)
            self.createIntent = nil
            do {
                sentMessageHistory = try model.recordSentMessage(
                    message,
                    workspaceID: payload.workspaceID
                )
            } catch {
                model.present(error)
            }
            let now = Int64(Date().timeIntervalSince1970 * 1_000)
            let summary = JobSummary(
                id: jobID,
                title: "新任务",
                modelId: payload.modelID,
                status: "pending",
                mode: "interactive",
                workspaceId: payload.workspaceID,
                workdir: payload.workdir,
                createdAt: now,
                updatedAt: now,
                pinnedAt: nil,
                sessionCount: 0,
                scheduleId: nil,
                shareToken: nil,
                agentId: payload.agentType,
                acpMode: payload.modeID,
                acpThoughtLevel: payload.thoughtLevelID
            )
            await model.reloadJobs()
            onCreated(ChatRoute(
                summary: summary,
                initialMessage: message.trimmingCharacters(in: .whitespacesAndNewlines),
                initialAttachment: pendingImage,
                agentType: payload.agentType,
                modelID: payload.modelID,
                modeID: payload.modeID,
                thoughtLevelID: payload.thoughtLevelID,
                initialImagePaths: nil,
                targetSessionID: nil
            ))
        } catch {
            if isDefinitelyRejected(error) {
                createIntent = nil
            }
            present(error)
        }
    }

    private func isDefinitelyRejected(_ error: Error) -> Bool {
        (error as? APIError)?.requestWasRejected == true
    }

    private func loadSentMessageHistory() {
        do {
            sentMessageHistory = try model.sentMessageHistory(workspaceID: workspaceID)
        } catch {
            present(error)
        }
    }

    private func loadPhoto(_ item: PhotosPickerItem) async {
        do {
            guard let data = try await item.loadTransferable(type: Data.self) else { return }
            let contentType = item.supportedContentTypes.first
            pendingImage = try ChatAttachmentProcessor.prepareImageUpload(
                data: data,
                suggestedFilename: "ios-\(UUID().uuidString).\(contentType?.preferredFilenameExtension ?? "jpg")",
                contentType: contentType
            )
        } catch {
            present(error)
            selectedPhoto = nil
        }
    }

    private func setCameraImage(_ image: UIImage) {
        do {
            pendingImage = try ChatAttachmentProcessor.prepareImageUpload(
                image: image,
                suggestedFilename: "camera-\(UUID().uuidString).jpg"
            )
        } catch {
            present(error)
        }
    }

    private func loadDocument(_ url: URL) async {
        let didAccess = url.startAccessingSecurityScopedResource()
        defer { if didAccess { url.stopAccessingSecurityScopedResource() } }
        do {
            let data = try await Task.detached(priority: .userInitiated) {
                try Data(contentsOf: url)
            }.value
            pendingImage = try await MainActor.run {
                try ChatAttachmentProcessor.prepareImageUpload(
                    data: data,
                    suggestedFilename: url.lastPathComponent,
                    contentType: UTType(filenameExtension: url.pathExtension)
                )
            }
        } catch {
            present(error)
        }
    }

    private func requestCameraAccess() {
        guard UIImagePickerController.isSourceTypeAvailable(.camera) else {
            present(APIError(summary: "相机不可用", detail: "当前设备没有可用相机。"))
            return
        }
        switch AVCaptureDevice.authorizationStatus(for: .video) {
        case .authorized:
            showsCameraPicker = true
        case .notDetermined:
            AVCaptureDevice.requestAccess(for: .video) { granted in
                Task { @MainActor in
                    if granted {
                        showsCameraPicker = true
                    } else {
                        present(APIError(summary: "没有相机权限", detail: "请在系统设置中允许 Sophia 访问相机后重试。"))
                    }
                }
            }
        case .denied, .restricted:
            present(APIError(summary: "没有相机权限", detail: "请在系统设置中允许 Sophia 访问相机后重试。"))
        @unknown default:
            present(APIError(summary: "相机权限状态未知", detail: "系统返回了未知的相机权限状态。"))
        }
    }

    private func present(_ error: Error) {
        if let error = error as? APIError {
            localError = PresentedError(title: error.summary, detail: error.detail)
        } else {
            localError = PresentedError(title: "操作失败", detail: String(describing: error))
        }
    }
}
