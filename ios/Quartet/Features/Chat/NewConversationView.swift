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

struct NewConversationView: View {
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var model: AppModel
    let onCreated: (ChatRoute) -> Void

    @State private var agents: [AgentSummary] = []
    @State private var workspaceID = ""
    @State private var agentID = ""
    @State private var modelID = ""
    @State private var modeID = ""
    @State private var thoughtLevelID = ""
    @State private var message = ""
    @State private var selectedPhoto: PhotosPickerItem?
    @State private var pendingImage: PendingUpload?
    @State private var showsAttachmentMenu = false
    @State private var showsCameraPicker = false
    @State private var showsDocumentPicker = false
    @State private var loading = true
    @State private var creating = false
    @State private var waitingForValidation = false
    @State private var validationAttempt = 0
    @State private var validationTimedOut = false
    @State private var savesDefaults = false
    @State private var localError: PresentedError?
    @State private var createIntent: CreateJobIntent?

    private var workspace: WorkspaceSummary? { model.workspaces.first { $0.id == workspaceID } }
    private var agent: AgentSummary? { agents.first { $0.id == agentID } }
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
            ScrollView {
                VStack(alignment: .leading, spacing: 22) {
                    Text("NEW RUN / INTERACTIVE")
                        .font(.system(.caption, design: .monospaced).weight(.bold))
                        .tracking(1.5)
                        .foregroundStyle(QuartetTheme.accent)

                    if loading {
                        VStack(spacing: 12) {
                            ProgressView()
                            if waitingForValidation {
                                Text("Agent 正在后台验证，页面会自动重试（第 \(validationAttempt) 次）…")
                                    .font(.footnote)
                                    .foregroundStyle(QuartetTheme.secondaryText)
                            }
                        }
                        .frame(maxWidth: .infinity)
                        .padding(.top, 70)
                    } else {
                        pickerSection
                        agentStatusNotice
                        composer
                    }
                }
                .padding(20)
            }
            .background(QuartetTheme.canvas)
            .navigationTitle("新对话")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("取消") { dismiss() } }
            }
            .task { await load() }
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
            .confirmationDialog("添加图片", isPresented: $showsAttachmentMenu, titleVisibility: .visible) {
                Button("相机") { requestCameraAccess() }
                Button("文件") { showsDocumentPicker = true }
                Button("取消", role: .cancel) {}
            } message: {
                Text("可从照片、相机或系统文件中添加图片。")
            }
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
        }
    }

    private var pickerSection: some View {
        VStack(spacing: 0) {
            Picker("工作空间", selection: $workspaceID) {
                ForEach(model.workspaces) { Text($0.displayName).tag($0.id) }
            }
            .onChange(of: workspaceID) { _, _ in applyWorkspaceDefaults() }
            rowDivider
            Picker("Agent", selection: $agentID) {
                ForEach(agents) { item in
                    let name = item.displayName.isEmpty ? item.type : item.displayName
                    Text(item.available ? name : "\(name) · \(item.availabilityLabel)").tag(item.id)
                }
            }
            .onChange(of: agentID) { _, _ in applyAgentDefaults() }
            if let models = agent?.models?.availableModels, !models.isEmpty {
                rowDivider
                Picker("模型", selection: $modelID) {
                    ForEach(models) { Text($0.name).tag($0.modelId) }
                }
            }
            if let modes = agent?.modes?.availableModes, !modes.isEmpty {
                rowDivider
                Picker("模式", selection: $modeID) {
                    ForEach(modes) { Text($0.name).tag($0.id) }
                }
            }
            if let levels = agent?.thoughtLevels?.availableThoughtLevels, !levels.isEmpty {
                rowDivider
                Picker("思考等级", selection: $thoughtLevelID) {
                    ForEach(levels) { Text($0.name).tag($0.id) }
                }
            }
            rowDivider
            Toggle("设为工作空间默认", isOn: $savesDefaults)
        }
        .pickerStyle(.menu)
        .padding(.horizontal, 16)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18))
        .overlay(RoundedRectangle(cornerRadius: 18).stroke(QuartetTheme.divider))
    }

    private var composer: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("第一条消息")
                .font(.headline)
            TextEditor(text: $message)
                .scrollContentBackground(.hidden)
                .frame(minHeight: 130)
                .padding(12)
                .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12))
            HStack {
                PhotosPicker(selection: $selectedPhoto, matching: .images) {
                    Label("照片", systemImage: pendingImage == nil ? "photo" : "photo.fill")
                }
                .foregroundStyle(pendingImage == nil ? QuartetTheme.secondaryText : QuartetTheme.accent)
                Button { showsAttachmentMenu = true } label: {
                    Label("相机或文件", systemImage: "plus.viewfinder")
                }
                .foregroundStyle(QuartetTheme.secondaryText)
                if pendingImage != nil {
                    Spacer()
                    Button("移除") { pendingImage = nil; selectedPhoto = nil }
                        .foregroundStyle(QuartetTheme.failed)
                }
            }
            .font(.subheadline)
            Button { Task { await create() } } label: {
                HStack {
                    if creating { ProgressView().tint(.black) }
                    Text(creating ? "正在创建…" : "创建并发送")
                    Spacer()
                    Image(systemName: "arrow.up")
                }
                .font(.headline)
                .foregroundStyle(.black)
                .padding(.horizontal, 18)
                .frame(height: 52)
                .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 14))
            }
            .disabled(creating || workspaceID.isEmpty || agent?.available != true || (message.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && pendingImage == nil))
        }
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
                        .font(.subheadline.weight(.semibold))
                }
                if let error = agent.error, !error.isEmpty {
                    Text(error)
                        .font(.caption)
                        .foregroundStyle(QuartetTheme.failed)
                        .textSelection(.enabled)
                }
                if !waitingForValidation {
                    Button("重新检查") { Task { await load(preserveSelection: true) } }
                        .font(.caption.weight(.semibold))
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
            var didInitializeSelection = preserveSelection && !agentID.isEmpty
            var receivedSnapshot = false
            let client = try model.apiClient()
            for attempt in 1...15 {
                try Task.checkCancellation()
                let response = try await client.agents()
                let snapshot = response.agentList
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
                    applyAgentDefaults()
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
            agentID = agents.first(where: { $0.type == requested || $0.agentId == requested })?.id ?? fallback?.id ?? ""
        } else {
            agentID = fallback?.id ?? ""
        }
        applyAgentDefaults()
    }

    private func applyAgentDefaults() {
        guard let agent else { return }
        let workspaceMatchesAgent = workspace?.defaultAgent == agent.type || workspace?.defaultAgent == agent.agentId
        let preferredModel = workspaceMatchesAgent ? workspace?.defaultModel : nil
        modelID = agent.models?.availableModels.first(where: { $0.modelId == preferredModel })?.modelId
            ?? agent.models?.currentModelId
            ?? agent.modelId
        modeID = agent.modes?.currentModeId ?? ""
        thoughtLevelID = agent.thoughtLevels?.currentThoughtLevelId ?? ""
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
            let now = Int64(Date().timeIntervalSince1970 * 1_000)
            let summary = JobSummary(
                id: jobID,
                title: "新对话",
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
                        present(APIError(summary: "没有相机权限", detail: "请在系统设置中允许 Quartet 访问相机后重试。"))
                    }
                }
            }
        case .denied, .restricted:
            present(APIError(summary: "没有相机权限", detail: "请在系统设置中允许 Quartet 访问相机后重试。"))
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
