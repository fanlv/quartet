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
    var initialFileAttachments: [FileAttachment]?
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
        initialFileAttachments: [FileAttachment]? = nil,
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
        self.initialFileAttachments = initialFileAttachments
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

private struct NewConversationAgentModelSelection: Hashable {
    let agentID: String
    let agentType: String
    let modelID: String
}

/// 运行配置里的“选一个”入口，统一走 `QuartetChoiceSheet`。
private enum NewConversationPicker: String, Identifiable {
    case agent
    case model
    case mode
    case thoughtLevel

    var id: String { rawValue }

    /// 弹窗标题；`QuartetChoiceSheet` 内部负责本地化。
    var title: String {
        switch self {
        case .agent: "选择 Agent"
        case .model: "选择模型"
        case .mode: "选择模式"
        case .thoughtLevel: "选择思考等级"
        }
    }

    var rowTitle: String {
        switch self {
        case .agent: "Agent"
        case .model: "模型"
        case .mode: "模式"
        case .thoughtLevel: "思考等级"
        }
    }

    var icon: String {
        switch self {
        case .agent: "command"
        case .model: "cpu"
        case .mode: "point.3.connected.trianglepath.dotted"
        case .thoughtLevel: "brain"
        }
    }
}

private enum NewConversationMode: String, CaseIterable, Identifiable {
    case chat
    case graph

    var id: String { rawValue }
    var title: String { (self == .chat ? "对话" : "Graph Workflow").localizedForApp }
    var icon: String { self == .chat ? "bubble.left.and.bubble.right" : "point.3.connected.trianglepath.dotted" }
}

/// 表单自身的坐标空间：用它把“输入框以外的点击”和输入框内的点击区分开。
/// 空间锚在滚动内容上，滚动时输入框在该空间里的位置不变，不会每帧写 State。
private let newConversationFormSpace = "new-conversation-form"

struct NewConversationView: View {
    @EnvironmentObject private var model: AppModel
    let onCreated: (ChatRoute) -> Void

    /// “选择 Agent”弹窗副标题要显示的版本号与用量。全局单例，跨弹窗复用缓存与节流。
    @ObservedObject private var agentUsageSummaries = AgentUsageSummaryStore.shared

    @State private var agents: [AgentSummary] = []
    @State private var agentPreferences: [String: AgentPreferences] = [:]
    @State private var creationMode: NewConversationMode = .chat
    @State private var workspaceID = ""
    @State private var agentID = ""
    @State private var modelID = ""
    @State private var modeID = ""
    @State private var thoughtLevelID = ""
    @State private var linkedThoughtLevels: AgentThoughtLevelState?
    @State private var linkedThoughtLevelSelection: NewConversationAgentModelSelection?
    @State private var thoughtLevelRequestID: UUID?
    @State private var message = ""
    @State private var sentMessageHistory: [SentMessageHistoryItem] = []
    @State private var projectMessagePresets: [MessagePreset] = []
    @State private var globalMessagePresets: [MessagePreset] = []
    @State private var messagePresetLoadErrors: [String] = []
    @State private var loadingMessagePresets = false
    @State private var selectedPhoto: PhotosPickerItem?
    @State private var pendingImage: PendingUpload?
    @State private var showsCameraPicker = false
    @State private var showsDocumentPicker = false
    @State private var showsMessageLibrary = false
    @State private var showsWorkspacePicker = false
    @State private var picker: NewConversationPicker?
    @State private var focusesComposerAfterMessageLibrary = false
    @State private var showsAdvancedOptions = false
    @State private var loading = true
    @State private var creating = false
    @State private var waitingForValidation = false
    @State private var validationAttempt = 0
    @State private var validationTimedOut = false
    @State private var savesDefaults = false
    @State private var localError: PresentedError?
    @State private var createIntent: CreateJobIntent?
    @State private var messageEditorFrame: CGRect = .zero
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
        agent?.modes?.availableModes.first(where: { $0.id == modeID })?.name
            ?? (modeID.isEmpty ? "跟随 Agent" : modeID)
    }
    private var thoughtLevelName: String {
        if isLinkingThoughtLevels { return "正在刷新…" }
        return linkedThoughtLevels?.availableThoughtLevels.first(where: { $0.id == thoughtLevelID })?.name
            ?? (thoughtLevelID.isEmpty ? "跟随 Agent" : thoughtLevelID)
    }
    private var hasAdvancedOptions: Bool {
        agent?.modes?.availableModes.isEmpty == false
            || linkedThoughtLevels?.availableThoughtLevels.isEmpty == false
            || isLinkingThoughtLevels
    }
    private var thoughtLevelSelection: NewConversationAgentModelSelection? {
        guard let agent, agent.available, !modelID.isEmpty, agent.models != nil else { return nil }
        return NewConversationAgentModelSelection(agentID: agent.agentId, agentType: agent.type, modelID: modelID)
    }
    private var isLinkingThoughtLevels: Bool {
        guard let selection = thoughtLevelSelection else { return false }
        return linkedThoughtLevelSelection != selection
    }
    private var cannotCreate: Bool {
        creating
            || isLinkingThoughtLevels
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
                        // 让空隙和留白也参与命中测试，点在卡片之间同样能收起键盘。
                        .contentShape(Rectangle())
                        .coordinateSpace(.named(newConversationFormSpace))
                        .simultaneousGesture(
                            SpatialTapGesture(coordinateSpace: .named(newConversationFormSpace))
                                .onEnded { value in
                                    // 落在输入框里的点击交给 TextEditor 自己处理，否则会先收起再弹出、出现闪烁。
                                    guard !messageEditorFrame.contains(value.location) else { return }
                                    composerFocused = false
                                }
                        )
                    }
                    .scrollDismissesKeyboard(.interactively)
                } else {
                    GraphWorkflowLaunchView(onCreated: onCreated)
                }
            }
            .background(QuartetTheme.canvas)
            .quartetNavigationTitle("新任务")
            .task(id: creationMode) {
                if creationMode == .chat, agents.isEmpty {
                    await load()
                }
            }
            .task(id: workspaceID) {
                guard creationMode == .chat, !workspaceID.isEmpty else { return }
                loadSentMessageHistory()
            }
            .task(id: thoughtLevelSelection) {
                guard creationMode == .chat else { return }
                await refreshThoughtLevels(for: thoughtLevelSelection)
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
            .sheet(item: $localError) {
                ErrorDetailView(error: $0)
            }
            .sheet(isPresented: $showsWorkspacePicker) {
                WorkspaceLaunchPicker(
                    workspaces: model.workspaces,
                    selectedWorkspaceID: workspaceID,
                    accessibilityIdentifierPrefix: "new-task-workspace-",
                    onSelect: { id in
                        guard let id else { return }
                        workspaceID = id
                    }
                )
                .presentationDetents([.medium, .large])
                .quartetSheetStyle()
            }
            .sheet(item: $picker) { picker in
                QuartetChoiceSheet(
                    title: picker.title,
                    choices: choices(for: picker),
                    selection: selectionBinding(for: picker),
                    accessibilityPrefix: "new-task-\(picker.rawValue)-option",
                    favoriteIDs: picker == .model ? favoriteModelIDs : []
                )
                .presentationDetents([.medium, .large])
                .quartetSheetStyle()
                .task {
                    guard picker == .agent else { return }
                    await loadAgentUsageSummaries()
                }
            }
            .sheet(isPresented: $showsCameraPicker) {
                CameraImagePicker(
                    onImagePicked: { image in
                        showsCameraPicker = false
                        setCameraImage(image)
                    },
                    onCancel: { showsCameraPicker = false }
                )
                .quartetSheetStyle()
            }
            .sheet(isPresented: $showsDocumentPicker) {
                DocumentAttachmentPicker(
                    onDocumentPicked: { url in
                        showsDocumentPicker = false
                        Task { await loadDocument(url) }
                    },
                    onCancel: { showsDocumentPicker = false }
                )
                .quartetSheetStyle()
            }
            .sheet(isPresented: $showsMessageLibrary, onDismiss: {
                if focusesComposerAfterMessageLibrary {
                    focusesComposerAfterMessageLibrary = false
                    composerFocused = true
                }
            }) {
                MessagePresetHistorySheet(
                    currentMessage: $message,
                    projectPresets: projectMessagePresets,
                    globalPresets: globalMessagePresets,
                    history: sentMessageHistory,
                    errors: messagePresetLoadErrors,
                    loading: loadingMessagePresets,
                    onApplied: { focusesComposerAfterMessageLibrary = true }
                )
                .presentationDetents([.medium, .large])
                .quartetSheetStyle()
                .task(id: workspaceID) { await loadMessagePresets() }
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
                    Label(item.title.localizedForApp, systemImage: item.icon)
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(selected ? QuartetTheme.primaryText : QuartetTheme.secondaryText)
                        .frame(maxWidth: .infinity)
                        .frame(minHeight: 42)
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
        .contentShape(Rectangle())
        .simultaneousGesture(TapGesture().onEnded { composerFocused = false })
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
                Text(message.isEmpty ? "支持文字、图片与文件" : "\(message.count) 字")
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }

            Button {
                composerFocused = false
                loadSentMessageHistory()
                loadingMessagePresets = true
                showsMessageLibrary = true
            } label: {
                HStack(spacing: 11) {
                    Image(systemName: "clock.arrow.circlepath")
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.accent)
                        .frame(width: 32, height: 32)
                        .background(QuartetTheme.accent.opacity(0.12), in: Circle())
                    VStack(alignment: .leading, spacing: 2) {
                        Text("预置消息与历史")
                            .font(.quartet(.control, weight: .semibold))
                            .foregroundStyle(QuartetTheme.primaryText)
                        Text("当前项目、全部项目与最近发送")
                            .font(.quartet(.compact))
                            .foregroundStyle(QuartetTheme.secondaryText)
                    }
                    Spacer(minLength: 8)
                    Image(systemName: "chevron.up")
                        .font(.quartet(.compact, weight: .bold))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                .padding(.horizontal, 12)
                .frame(minHeight: 52)
                .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            }
            .buttonStyle(.plain)
            .accessibilityLabel("预置消息与历史")
            .accessibilityHint("打开后可从分组列表中选择")
            .accessibilityIdentifier("new-task-message-history")

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
                    .simultaneousGesture(TapGesture().onEnded {
                        Task { @MainActor in
                            await Task.yield()
                            composerFocused = true
                        }
                    })
                    .frame(minHeight: 148)
                    .onGeometryChange(for: CGRect.self) { proxy in
                        proxy.frame(in: .named(newConversationFormSpace))
                    } action: { frame in
                        messageEditorFrame = frame
                    }
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
                        .accessibilityLabel("移除附件")
                        .padding(8)
                    }
            }

            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 8) {
                    PhotosPicker(selection: $selectedPhoto, matching: .images) {
                        Label("照片", systemImage: hasPendingImage ? "photo.fill" : "photo")
                            .font(.quartet(.control, weight: .semibold))
                            .foregroundStyle(QuartetTheme.accent)
                            .padding(.horizontal, 12)
                            .frame(height: 36)
                            .background(QuartetTheme.elevated, in: Capsule())
                    }
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

    private func attachmentActionLabel(_ title: String, icon: String) -> some View {
        Label(title.localizedForApp, systemImage: icon)
            .font(.quartet(.control, weight: .semibold))
            .foregroundStyle(QuartetTheme.accent)
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
                        if isLinkingThoughtLevels {
                            rowDivider.padding(.leading, 58)
                            configurationRow(title: "思考等级", value: "正在读取可用等级…", icon: "brain")
                        } else if linkedThoughtLevels?.availableThoughtLevels.isEmpty == false {
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
        Button {
            composerFocused = false
            showsWorkspacePicker = true
        } label: {
            HStack(spacing: 12) {
                configurationIcon("square.stack.3d.up")
                VStack(alignment: .leading, spacing: 2) {
                    Text("工作空间")
                        .font(.quartet(.detail))
                        .foregroundStyle(QuartetTheme.secondaryText)
                    Text(workspace?.displayName ?? "未选择工作空间")
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .lineLimit(1)
                    if let workspace {
                        Text(workspace.workdir)
                            .font(.quartet(.compact))
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .lineLimit(1)
                            .truncationMode(.middle)
                    }
                }
                Spacer(minLength: 8)
                Image(systemName: "chevron.right")
                    .font(.quartet(.detail, weight: .bold))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
            .padding(.horizontal, 14)
            .frame(minHeight: 68)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(QuartetTheme.divider))
        .accessibilityLabel("工作空间，当前为\(workspace?.displayName ?? "未选择")")
        .accessibilityHint("点按弹出工作空间列表")
        .accessibilityIdentifier("new-task-workspace-picker")
        .onChange(of: workspaceID) { _, _ in applyWorkspaceDefaults() }
    }

    private var agentPicker: some View {
        pickerRow(.agent, value: agentName)
            .disabled(agents.isEmpty)
    }

    private var modelPicker: some View {
        pickerRow(.model, value: modelName)
            .disabled(orderedModels.isEmpty)
    }

    private var modePicker: some View {
        pickerRow(.mode, value: modeName)
    }

    private var thoughtLevelPicker: some View {
        pickerRow(.thoughtLevel, value: thoughtLevelName)
    }

    private func pickerRow(_ target: NewConversationPicker, value: String) -> some View {
        Button {
            composerFocused = false
            picker = target
        } label: {
            configurationRow(title: target.rowTitle, value: value, icon: target.icon)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(target.rowTitle)，当前为\(value)")
        .accessibilityHint("点按弹出可选项列表")
        .accessibilityIdentifier("new-task-\(target.rawValue)-picker")
    }

    private func configurationRow(title: String, value: String, icon: String) -> some View {
        HStack(spacing: 12) {
            configurationIcon(icon)
            VStack(alignment: .leading, spacing: 2) {
                Text(title.localizedForApp)
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
                Text(value.localizedForApp)
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
                        ProgressView().tint(QuartetTheme.onAccent)
                    } else {
                        Image(systemName: "arrow.up.circle.fill")
                    }
                    Text(creating ? "正在创建…" : "创建并发送")
                    Spacer()
                    Image(systemName: "chevron.right")
                        .font(.quartet(.detail, weight: .bold))
                }
                .font(.quartet(.regular, weight: .semibold))
                .foregroundStyle(QuartetTheme.onAccent)
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
        .contentShape(Rectangle())
        .simultaneousGesture(TapGesture().onEnded { composerFocused = false })
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
                    workspaceID = initialWorkspaceID
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

    private var initialWorkspaceID: String {
        let candidates = [model.lastSentMessageWorkspaceID, model.selectedWorkspaceID]
        return candidates.compactMap { $0 }.first(where: { candidate in
            model.workspaces.contains { $0.id == candidate }
        }) ?? model.workspaces.first?.id ?? ""
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
        guard agentID != id else { return }
        let previousSelection = thoughtLevelSelection
        agentID = id
        applyAgentDefaults(includeWorkspaceDefault: false)
        if let selection = thoughtLevelSelection, selection == previousSelection {
            linkedThoughtLevels = nil
            linkedThoughtLevelSelection = nil
            thoughtLevelID = ""
            Task { await refreshThoughtLevels(for: selection) }
        }
    }

    private func selectModel(_ id: String) {
        guard modelID != id else { return }
        modelID = id
        invalidateThoughtLevelsIfNeeded()
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

        invalidateThoughtLevelsIfNeeded()
    }

    private func invalidateThoughtLevelsIfNeeded() {
        guard linkedThoughtLevelSelection != thoughtLevelSelection else { return }
        linkedThoughtLevels = nil
        linkedThoughtLevelSelection = nil
        thoughtLevelID = ""
    }

    private func refreshThoughtLevels(for selection: NewConversationAgentModelSelection?) async {
        guard let selection else {
            linkedThoughtLevels = nil
            linkedThoughtLevelSelection = nil
            thoughtLevelRequestID = nil
            thoughtLevelID = ""
            return
        }
        invalidateThoughtLevelsIfNeeded()
        let requestID = UUID()
        thoughtLevelRequestID = requestID
        do {
            let state = try await model.relinkACPThoughtLevels(
                agentType: selection.agentType,
                modelID: selection.modelID
            )
            try Task.checkCancellation()
            guard thoughtLevelSelection == selection, thoughtLevelRequestID == requestID else { return }
            let preferences = agentPreferences[selection.agentID] ?? agentPreferences[selection.agentType]
            let available = state.availableThoughtLevels.map(\.id)
            linkedThoughtLevels = state
            linkedThoughtLevelSelection = selection
            thoughtLevelID = validID(preferences?.defaultThoughtLevel, in: available)
                ?? validID(state.currentThoughtLevelId, in: available)
                ?? ""
        } catch is CancellationError {
            return
        } catch {
            guard thoughtLevelSelection == selection, thoughtLevelRequestID == requestID else { return }
            linkedThoughtLevels = AgentThoughtLevelState(
                availableThoughtLevels: [],
                currentThoughtLevelId: ""
            )
            linkedThoughtLevelSelection = selection
            thoughtLevelID = ""
            present(error)
        }
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

    private func choices(for picker: NewConversationPicker) -> [QuartetChoice] {
        switch picker {
        case .agent:
            return agents.map { item in
                let name = item.displayName.isEmpty ? item.type : item.displayName
                return .agent(
                    id: item.id,
                    title: name,
                    command: item.type,
                    note: item.available ? nil : item.availabilityLabel,
                    disabled: !item.available && item.id != agentID,
                    usage: agentUsageSummaries.summary(agent: item),
                    retry: { Task { await loadAgentUsageSummaries() } }
                )
            }
        case .model:
            return orderedModels.map { item in
                let name = item.name.trimmingCharacters(in: .whitespacesAndNewlines)
                let description = item.description?.trimmingCharacters(in: .whitespacesAndNewlines)
                return QuartetChoice(
                    id: item.modelId,
                    title: name.isEmpty ? item.modelId : name,
                    detail: description?.isEmpty == false ? description : item.modelId
                )
            }
        case .mode:
            return [QuartetChoice(id: "", title: "跟随 Agent", detail: "使用 Agent 自身的默认模式")]
                + (agent?.modes?.availableModes ?? []).map { QuartetChoice(id: $0.id, title: $0.name) }
        case .thoughtLevel:
            return [QuartetChoice(id: "", title: "跟随 Agent", detail: "使用 Agent 自身的默认思考等级")]
                + (linkedThoughtLevels?.availableThoughtLevels ?? []).map { QuartetChoice(id: $0.id, title: $0.name) }
        }
    }

    /// 打开“选择 Agent”弹窗时读取每个 Agent 的版本号与用量：先出缓存，再后台刷新。
    /// 失败不占用节流窗口，所以行内“重试”按钮直接再调一次。
    private func loadAgentUsageSummaries() async {
        await agentUsageSummaries.load(agents: agents, model: model)
    }

    private func selectionBinding(for picker: NewConversationPicker) -> Binding<String> {
        switch picker {
        case .agent:
            return Binding(get: { agentID }, set: { selectAgent($0) })
        case .model:
            return Binding(get: { modelID }, set: { selectModel($0) })
        case .mode:
            return Binding(get: { modeID }, set: { modeID = $0 })
        case .thoughtLevel:
            return Binding(get: { thoughtLevelID }, set: { thoughtLevelID = $0 })
        }
    }

    private func create() async {
        guard !isLinkingThoughtLevels,
              let workspace, let agent, agent.available, let payload = currentCreatePayload else { return }
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
            model.beginOptimisticJobExecution(id: jobID, fallback: summary)
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
                initialFileAttachments: nil,
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

    private func loadMessagePresets() async {
        guard !workspaceID.isEmpty else {
            projectMessagePresets = []
            globalMessagePresets = []
            messagePresetLoadErrors = []
            return
        }
        let requestedWorkspaceID = workspaceID
        loadingMessagePresets = true
        projectMessagePresets = []
        globalMessagePresets = []
        messagePresetLoadErrors = []
        defer {
            if workspaceID == requestedWorkspaceID {
                loadingMessagePresets = false
            }
        }
        do {
            let response = try await model.effectiveMessagePresets(workspaceID: requestedWorkspaceID)
            guard workspaceID == requestedWorkspaceID else { return }
            projectMessagePresets = response.project
            globalMessagePresets = response.global
            messagePresetLoadErrors = (response.errors ?? []).map { error in
                [error.scope, error.file, error.error]
                    .filter { !$0.isEmpty }
                    .joined(separator: "\n")
            }
        } catch is CancellationError {
            return
        } catch {
            guard workspaceID == requestedWorkspaceID else { return }
            projectMessagePresets = []
            globalMessagePresets = []
            if let error = error as? APIError {
                messagePresetLoadErrors = ["\(error.summary)\n\n\(error.detail)"]
            } else {
                messagePresetLoadErrors = [String(reflecting: error)]
            }
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
                try ChatAttachmentProcessor.prepareFileUpload(
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

/// 首页筛选与新任务两个 tab 共用的工作空间弹窗，样式与首页“任务操作”弹窗保持一致。
struct WorkspaceLaunchPicker: View {
    @Environment(\.dismiss) private var dismiss

    let workspaces: [WorkspaceSummary]
    let selectedWorkspaceID: String?
    /// 传 true 时在列表首位插入“全部工作空间”一行，选中它会回调 nil。
    var includesAllOption: Bool = false
    let accessibilityIdentifierPrefix: String
    let onSelect: (String?) -> Void

    var body: some View {
        NavigationStack {
            Group {
                if workspaces.isEmpty, !includesAllOption {
                    ContentUnavailableView(
                        "没有可用的工作空间",
                        systemImage: "square.stack.3d.up.slash",
                        description: Text("请先在 Web 端创建工作空间。")
                    )
                } else {
                    ScrollView {
                        LazyVStack(spacing: 0) {
                            if includesAllOption {
                                workspaceRow(
                                    id: nil,
                                    title: "ALL",
                                    detail: "显示所有工作空间的任务",
                                    tint: QuartetTheme.accent
                                )
                            }

                            ForEach(Array(workspaces.enumerated()), id: \.element.id) { index, workspace in
                                if index > 0 || includesAllOption {
                                    Divider()
                                        .overlay(QuartetTheme.divider)
                                        .padding(.leading, 62)
                                }
                                workspaceRow(
                                    id: workspace.id,
                                    title: workspace.displayName,
                                    detail: workspace.workdir,
                                    tint: QuartetTheme.workspaceTint(workspace)
                                )
                            }
                        }
                        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                        .overlay {
                            RoundedRectangle(cornerRadius: 18, style: .continuous)
                                .stroke(QuartetTheme.divider.opacity(0.8), lineWidth: 1)
                        }
                        .padding(.horizontal, 20)
                        .padding(.top, 8)
                        .padding(.bottom, 20)
                    }
                }
            }
            .background(QuartetTheme.canvas)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .principal) {
                    Text("选择工作空间")
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .accessibilityAddTraits(.isHeader)
                }
            }
        }
    }

    private func workspaceRow(
        id: String?,
        title: String,
        detail: String,
        tint: Color
    ) -> some View {
        let selected = id == selectedWorkspaceID
        return Button {
            onSelect(id)
            dismiss()
        } label: {
            HStack(spacing: 12) {
                Circle()
                    .fill(tint.opacity(0.11))
                    .frame(width: 38, height: 38)
                    .overlay {
                        Circle()
                            .fill(tint)
                            .frame(width: 10, height: 10)
                    }
                    .accessibilityHidden(true)

                VStack(alignment: .leading, spacing: 3) {
                    Text(title.localizedForApp)
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .lineLimit(1)
                    Text(detail.localizedForApp)
                        .font(.quartet(.detail))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }

                Spacer(minLength: 8)

                if selected {
                    Image(systemName: "checkmark.circle.fill")
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.accent)
                        .accessibilityHidden(true)
                }
            }
            .padding(.horizontal, 13)
            .frame(minHeight: 64)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(title)，\(detail)")
        .accessibilityValue(selected ? "已选择" : "")
        .accessibilityHint("选择此工作空间并关闭弹窗")
        .accessibilityIdentifier("\(accessibilityIdentifierPrefix)\(id ?? "all")")
    }
}

struct MessagePresetHistorySheet: View {
    @Binding var currentMessage: String
    let projectPresets: [MessagePreset]
    let globalPresets: [MessagePreset]
    let history: [SentMessageHistoryItem]
    let errors: [String]
    let loading: Bool
    let onApplied: () -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var pendingPreset: MessagePreset?

    private var hasContent: Bool {
        !projectPresets.isEmpty || !globalPresets.isEmpty || !history.isEmpty
    }

    var body: some View {
        NavigationStack {
            Group {
                if loading, !hasContent, errors.isEmpty {
                    VStack(spacing: 14) {
                        ProgressView()
                            .controlSize(.large)
                            .tint(QuartetTheme.accent)
                        Text("正在读取预置消息…")
                            .font(.quartet(.control))
                            .foregroundStyle(QuartetTheme.secondaryText)
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if !hasContent, errors.isEmpty {
                    ContentUnavailableView(
                        "暂无预置消息或历史",
                        systemImage: "text.badge.plus",
                        description: Text("可在 Web 端设置预置消息；成功发送的消息会保存在最近发送中。")
                    )
                } else {
                    List {
                        if loading {
                            HStack(spacing: 10) {
                                ProgressView().controlSize(.small)
                                Text("正在刷新预置消息…")
                            }
                            .foregroundStyle(QuartetTheme.secondaryText)
                        }

                        if !errors.isEmpty {
                            Section("加载错误") {
                                ForEach(Array(errors.enumerated()), id: \.offset) { _, error in
                                    Text(error)
                                        .font(.quartet(.detail, design: .monospaced))
                                        .foregroundStyle(QuartetTheme.failed)
                                        .textSelection(.enabled)
                                }
                            }
                        }

                        presetSection("当前项目", presets: projectPresets, scope: "project")
                        presetSection("全部项目", presets: globalPresets, scope: "global")

                        if !history.isEmpty {
                            Section("最近发送") {
                                ForEach(history) { item in
                                    Button { applyHistory(item) } label: {
                                        MessageLibraryRow(
                                            title: messagePreview(item.content),
                                            subtitle: Date(timeIntervalSince1970: Double(item.createdAt) / 1_000)
                                                .formatted(date: .abbreviated, time: .shortened),
                                            icon: "clock.arrow.circlepath"
                                        )
                                    }
                                    .buttonStyle(.plain)
                                    .accessibilityLabel(item.content)
                                    .accessibilityHint("替换当前输入内容")
                                    .accessibilityIdentifier("sent-message-history-item-\(item.id)")
                                }
                            }
                        }
                    }
                    .scrollContentBackground(.hidden)
                }
            }
            .background(QuartetTheme.canvas)
            .quartetNavigationTitle("预置消息与历史")
            .alert(
                "输入框已有内容",
                isPresented: Binding(
                    get: { pendingPreset != nil },
                    set: { if !$0 { pendingPreset = nil } }
                )
            ) {
                Button("追加") { applyPendingPreset(append: true) }
                Button("替换") { applyPendingPreset(append: false) }
                Button("关闭", role: .cancel) { pendingPreset = nil }
            } message: {
                Text("请选择替换当前草稿，或把预置消息追加到草稿末尾。")
            }
        }
    }

    @ViewBuilder
    private func presetSection(_ title: String, presets: [MessagePreset], scope: String) -> some View {
        if !presets.isEmpty {
            Section(title) {
                ForEach(presets) { preset in
                    Button { selectPreset(preset) } label: {
                        MessageLibraryRow(
                            title: preset.name?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false
                                ? preset.name!
                                : messagePreview(preset.content),
                            subtitle: preset.name?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false
                                ? messagePreview(preset.content)
                                : nil,
                            icon: preset.content.trimmingCharacters(in: .whitespacesAndNewlines).hasPrefix("/")
                                ? "terminal"
                                : "text.bubble"
                        )
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(preset.name?.isEmpty == false ? preset.name! : messagePreview(preset.content))
                    .accessibilityHint(currentMessage.isEmpty ? "填入消息输入框" : "选择追加或替换当前输入内容")
                    .accessibilityIdentifier("message-preset-\(scope)-\(preset.id)")
                }
            }
        }
    }

    private func selectPreset(_ preset: MessagePreset) {
        if currentMessage.isEmpty {
            applyPreset(preset, append: false)
        } else {
            pendingPreset = preset
        }
    }

    private func applyPendingPreset(append: Bool) {
        guard let pendingPreset else { return }
        self.pendingPreset = nil
        applyPreset(pendingPreset, append: append)
    }

    private func applyPreset(_ preset: MessagePreset, append: Bool) {
        if append, !currentMessage.isEmpty {
            currentMessage += "\n\n\(preset.content)"
        } else {
            currentMessage = preset.content
        }
        onApplied()
        dismiss()
    }

    private func applyHistory(_ item: SentMessageHistoryItem) {
        currentMessage = item.content
        onApplied()
        dismiss()
    }

    private func messagePreview(_ content: String) -> String {
        let value = content
            .split(whereSeparator: \.isWhitespace)
            .joined(separator: " ")
        guard !value.isEmpty else { return "（空）".localizedForApp }
        return value.count > 120 ? "\(value.prefix(120))…" : value
    }
}

private struct MessageLibraryRow: View {
    let title: String
    let subtitle: String?
    let icon: String

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: icon)
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(QuartetTheme.accent)
                .frame(width: 32, height: 32)
                .background(QuartetTheme.accent.opacity(0.1), in: RoundedRectangle(cornerRadius: 9, style: .continuous))

            VStack(alignment: .leading, spacing: 4) {
                Text(title)
                    .font(.quartet(.control, weight: .medium))
                    .foregroundStyle(QuartetTheme.primaryText)
                    .lineLimit(3)
                    .frame(maxWidth: .infinity, alignment: .leading)
                if let subtitle, !subtitle.isEmpty {
                    Text(subtitle)
                        .font(.quartet(.compact))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .lineLimit(2)
                }
            }

            Image(systemName: "chevron.right")
                .font(.quartet(.compact, weight: .bold))
                .foregroundStyle(QuartetTheme.secondaryText.opacity(0.7))
                .padding(.top, 8)
        }
        .padding(.vertical, 5)
        .contentShape(Rectangle())
    }
}
