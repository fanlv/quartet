import SwiftUI
import Combine
import UniformTypeIdentifiers
import PhotosUI
import UIKit

@MainActor
private final class PersonalChatHomeModel: ObservableObject {
    @Published var snapshot: PersonalChatSnapshot?
    @Published var messages: [PersonalChatMessage] = []
    @Published var harnesses: [AgentSummary] = []
    @Published var sending = false
    @Published var error: String?
    @Published var hasMore = false
    @Published var paging = false
    private var before: UInt64?
    private var generation = UUID()
    private var refreshVersion = 0
    private var connectionIdentity = ""
    private let viewerID = UUID().uuidString
    private var observedTaskID="default-main"
    private var retry: PersonalChatSend?

    func observe(app: AppModel, taskID: String) async {
        observedTaskID=taskID
        let identity = app.serverAddress+"|"+app.username
        if identity != connectionIdentity { snapshot=nil;messages=[];before=nil;hasMore=false;retry=nil;harnesses=[];error=nil;connectionIdentity=identity }
        let captured = UUID(); generation = captured
        defer { if generation == captured { generation = UUID() } }
        var reconnects = 0
        while !Task.isCancelled, generation == captured {
            do {
                let client = try app.apiClient()
                await refresh(app:app)
                do {
                    let available = try await client.agents()
                    guard generation == captured, !Task.isCancelled else { return }
                    harnesses = available.agentList
                } catch { if generation == captured, !Task.isCancelled { self.error=String(reflecting:error) } }
                try await client.personalChatEvents(lastEventID:snapshot?.seq ?? 0, viewerID:viewerID,taskID:taskID,onOpen: { [weak self] in
                    await self?.refresh(app:app)
                }, onChange: { [weak self] _, _ in
                    await self?.refresh(app:app)
                })
                reconnects = 0
            } catch {
                guard generation == captured, !Task.isCancelled else { return }
                self.error = String(reflecting:error)
                reconnects += 1
            }
            do { try await Task.sleep(for:.seconds(min(15,max(1,reconnects*2)))) } catch { return }
        }
    }
    func refresh(app: AppModel) async {
        refreshVersion += 1
        let version = refreshVersion, captured = generation
        do {
            let value = try await app.apiClient().personalChat(since:snapshot?.seq ?? 0,taskID:observedTaskID,windowStart:messages.map(\.order).min() ?? 0)
            guard captured == generation, version == refreshVersion, !Task.isCancelled else { return }
            if let old=snapshot,old.epoch==value.epoch,value.seq<old.seq{return}
            let reset=snapshot.map{$0.epoch != value.epoch && value.seq < $0.seq} ?? false
            if reset { messages = []; before = nil }
            snapshot = value
            merge(value.messages)
            if before == nil { before = value.before; hasMore = value.hasMore }
        } catch {
            guard captured == generation, version == refreshVersion, !Task.isCancelled else { return }
            self.error = String(reflecting:error)
        }
    }
    func older(app: AppModel) async {
        guard !paging, hasMore, let before else { return }
        paging = true; let captured = generation
        defer { paging = false }
        do {
            let page = try await app.apiClient().personalChat(before:before)
            guard captured == generation, !Task.isCancelled else { return }
            merge(page.messages); self.before = page.before; hasMore = page.hasMore
        } catch { if captured == generation, !Task.isCancelled { self.error = String(reflecting:error) } }
    }
    private func merge(_ additions: [PersonalChatMessage]) {
        var byID = Dictionary(uniqueKeysWithValues:messages.map { ($0.id,$0) })
        for m in additions where byID[m.id] == nil || byID[m.id]!.revision <= m.revision { byID[m.id] = m }
        messages = byID.values.sorted { $0.order == $1.order ? $0.id < $1.id : $0.order < $1.order }
    }
    func send(_ submitted: PersonalChatSend, app: AppModel) async -> Bool {
        guard !sending else { return false }
        var request = submitted
        if let old = retry { request.clientMessageId = old.clientMessageId; if request != old { request = submitted } }
        retry = request
        sending = true; let captured = generation
        defer { sending = false }
        do {
            let receipt = try await app.apiClient().personalChatSend(request)
            guard captured == generation, !Task.isCancelled else { return false }
            guard receipt.run.messageId == request.clientMessageId else {
                throw APIError(summary:"发送回执不一致",detail:"Expected \(request.clientMessageId), received \(receipt.run.messageId)")
            }
            retry = nil; await refresh(app:app); return true
        } catch { if captured == generation, !Task.isCancelled { self.error = String(reflecting:error) }; return false }
    }
    func control(taskID:String, action:String, runID:String? = nil, app:AppModel) async {
        let captured = generation
        do {
            try await app.apiClient().personalChatControl(PersonalChatControl(taskId:taskID,action:action,runId:runID))
            guard captured == generation, !Task.isCancelled else { return }
            await refresh(app:app)
        } catch { if captured == generation, !Task.isCancelled { self.error = String(reflecting:error) } }
    }
}

struct PersonalChatView: View {
    @EnvironmentObject private var app: AppModel
    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.mainTabBarInset) private var tabInset
    @Binding var showsMainTabBar: Bool
    @StateObject private var home = PersonalChatHomeModel()
    @StateObject private var voice = PersonalChatVoice()
    @State private var text = ""
    @State private var agentID = "main"
    @State private var taskID = ""
    @State private var harnessID = ""
    @State private var modelID = ""
    @State private var modeID = ""
    @State private var thoughtID = ""
    @State private var independent = false
    @State private var choice: Choice?
    @State private var editingAgent: PersonalAgent?
    @State private var showsHistory = false
    @State private var showsFiles = false
    @State private var showsPhotos=false
    @State private var showsCamera=false
    @State private var selectedPhotos:[PhotosPickerItem]=[]
    @State private var linkedThought:AgentThoughtLevelState?
    @State private var linkedThoughtKey=""
    @State private var linkingThought=false
    @State private var uploading = false
    @State private var following = true
    @State private var readingAnchor: String?
    @State private var timelineWidth: CGFloat = 320
    private enum Choice: String, Identifiable { case agent, harness, model, mode, thought, task, attachment; var id:String { rawValue } }
    private var agents: [PersonalAgent] { home.snapshot?.agents.filter { $0.deleted != true } ?? [] }
    private var selectedTask: PersonalChatTask? { home.snapshot?.tasks.first { $0.id == (taskID.isEmpty ? "default-\(agentID)" : taskID) } }
    private var session: PersonalChatSession? { home.snapshot?.sessions.first { $0.id == selectedTask?.sessionId } }
    private var effectiveHarness: String { !harnessID.isEmpty ? harnessID : session?.harnessId ?? agents.first { $0.id == agentID }?.harnessId ?? home.snapshot?.defaultHarnessId ?? home.harnesses.first?.id ?? "" }
    private var effectiveModel:String{!modelID.isEmpty ? modelID : (session?.harnessId==effectiveHarness ? session?.meta.model_id:nil) ?? harness?.models?.currentModelId ?? ""}
    private var effectiveMode:String{!modeID.isEmpty ? modeID : (session?.harnessId==effectiveHarness ? session?.meta.acp_mode:nil) ?? harness?.modes?.currentModeId ?? ""}
    private var needsThoughtLink:Bool{!effectiveModel.isEmpty && effectiveModel != harness?.models?.currentModelId}
    private var thoughtOptions:AgentThoughtLevelState?{needsThoughtLink ? (linkedThoughtKey==effectiveHarness+"|"+effectiveModel ? linkedThought:nil) : harness?.thoughtLevels}
    private var effectiveThought:String{
        let stored=session?.harnessId==effectiveHarness && session?.meta.model_id==effectiveModel ? session?.meta.acp_thought_level:nil
        let requested = !thoughtID.isEmpty ? thoughtID : stored ?? thoughtOptions?.currentThoughtLevelId ?? ""
        return thoughtOptions?.availableThoughtLevels.contains { $0.id==requested } == true ? requested : ""
    }
    private var harness: AgentSummary? { home.harnesses.first { $0.id == effectiveHarness } }
    private var running: Bool { home.snapshot?.runs.contains { $0.taskId == selectedTask?.id && ["running","stopping"].contains($0.state) } == true }
    private var sendDisabled: Bool { home.sending || uploading || voiceBusy || linkingThought || text.trimmingCharacters(in:.whitespacesAndNewlines).isEmpty || !app.can("job.execute") }
    private var voiceBusy: Bool { voice.isPreparing || voice.isRecording || voice.isFinishing }
    private var identity: String { "\(app.serverAddress)|\(app.username)|\(scenePhase == .active)" }

    var body: some View {
        NavigationStack {
            VStack(spacing:0) {
                timeline
                composer
            }
            .background(QuartetTheme.canvas)
            .mainTabBarBottomInset(tabInset)
            .quartetNavigationTitle("首页")
            .toolbar {
                ToolbarItem(placement:.topBarLeading) {
                    NavigationLink { JobsView(showsMainTabBar:$showsMainTabBar) } label: { Image(systemName:"point.3.connected.trianglepath.dotted") }
                        .accessibilityLabel("Graph Workflow").accessibilityIdentifier("personal-graph")
                }
                ToolbarItemGroup(placement:.topBarTrailing) {
                    Button { showsHistory = true } label: { Image(systemName:"magnifyingglass") }.accessibilityLabel("历史 Session 检索")
                    if app.can("agent.manage") {
                        Button { editingAgent = PersonalAgent(id:"",name:"",systemPrompt:"") } label: { Image(systemName:"person.badge.plus") }.accessibilityLabel("添加 Agent")
                    }
                }
            }
            .task(id:identity+"|"+taskID+"|"+agentID) {
                guard scenePhase == .active else { return }
                await home.observe(app:app,taskID:taskID.isEmpty ? "default-\(agentID)":taskID)
            }
            .task(id:effectiveHarness+"|"+effectiveModel) {
                let key=effectiveHarness+"|"+effectiveModel
                guard needsThoughtLink else{linkedThought=nil;linkingThought=false;return}
                linkedThoughtKey=key;linkedThought=nil;linkingThought=true
                defer{if key==effectiveHarness+"|"+effectiveModel{linkingThought=false}}
                do{let options=try await app.relinkACPThoughtLevels(agentType:effectiveHarness,modelID:effectiveModel);guard !Task.isCancelled,key==effectiveHarness+"|"+effectiveModel else{return};linkedThought=options}
                catch{if !Task.isCancelled,key==effectiveHarness+"|"+effectiveModel{app.present(error)}}
            }
            .photosPicker(isPresented:$showsPhotos,selection:$selectedPhotos,matching:.images)
            .onChange(of:selectedPhotos){_,items in
                let captured=identity
                Task{
                    uploading=true;defer{uploading=false}
                    do{
                        for item in items{
                            guard let data=try await item.loadTransferable(type:Data.self) else{throw APIError(summary:"附件上传失败",detail:"PhotosPicker returned no image data.")}
                            guard captured==identity,!Task.isCancelled else{return}
                            let type=item.supportedContentTypes.first ?? .image
                            let file=try await app.apiClient().uploadFile(data:data,filename:"photo-\(UUID().uuidString).\(type.preferredFilenameExtension ?? "jpg")",mimeType:type.preferredMIMEType ?? "image/jpeg")
                            guard captured==identity,!Task.isCancelled else{return};text+="\n![\(file.name)](\(file.path))"
                        }
                        selectedPhotos=[]
                    }catch{if captured==identity{app.present(error)}}
                }
            }
            .fullScreenCover(isPresented:$showsCamera){CameraImagePicker(onImagePicked:{ image in
                showsCamera=false
                guard let data=image.jpegData(compressionQuality:0.88)else{app.present(APIError(summary:"附件上传失败",detail:"Could not encode camera image as JPEG."));return}
                let captured=identity
                Task{uploading=true;defer{uploading=false};do{let file=try await app.apiClient().uploadFile(data:data,filename:"camera-\(UUID().uuidString).jpg",mimeType:"image/jpeg");guard captured==identity,!Task.isCancelled else{return};text+="\n![\(file.name)](\(file.path))"}catch{if captured==identity{app.present(error)}}}
            },onCancel:{showsCamera=false}).ignoresSafeArea()}
            .onChange(of:scenePhase) { _, phase in if phase == .background { voice.cancel() } }
            .onDisappear { voice.cancel() }
            .sheet(item:$choice) { item in
                QuartetChoiceSheet(title:choiceTitle(item),choices:choices(item),selection:selection(item),accessibilityPrefix:"personal-\(item.rawValue)")
            }
            .sheet(item:$editingAgent) { agent in PersonalAgentEditor(agent:agent,harnesses:home.harnesses) { Task { await home.refresh(app:app) } } }
            .sheet(isPresented:$showsHistory) { PersonalChatHistoryView { m in selectAgent(m.agentId); taskID = m.taskId; independent=false; showsHistory = false } }
            .fileImporter(isPresented:$showsFiles,allowedContentTypes:[.item],allowsMultipleSelection:true) { result in
                Task { await upload(result) }
            }
        }
    }
    private var timeline: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment:.leading,spacing:24) {
                    if home.hasMore {
                        Button("更早消息") {
                            let anchor = readingAnchor ?? home.messages.first?.id
                            Task { await home.older(app:app); if let anchor { proxy.scrollTo(anchor,anchor:.top) } }
                        }.frame(maxWidth:.infinity,minHeight:44).disabled(home.paging)
                    }
                    if home.snapshot == nil { ProgressView() }
                    if home.snapshot != nil, home.messages.isEmpty {
                        VStack(alignment:.leading,spacing:12) {
                            Text("和你的主 Agent 聊聊").font(.quartet(.large,weight:.medium))
                            Text("直接发送给主 Agent，或 @ 其他 Agent 开始协作。").foregroundStyle(QuartetTheme.secondaryText)
                        }.padding(.vertical,48)
                    }
                    ForEach(home.messages.filter { $0.internal != true || home.snapshot?.settings.debug == true }) { m in
                        PersonalChatMessageRow(contentWidth:max(0,timelineWidth-32),message:m,agent:home.snapshot?.agents.first { $0.id == m.agentId },debug:home.snapshot?.settings.debug == true,onReply:{ selectAgent(m.agentId);taskID=m.taskId;independent=false },onWithdraw:{ Task { await home.control(taskID:m.taskId,action:"withdraw",runID:m.runId,app:app) } })
                            .id(m.id)
                            .onAppear { if !following, readingAnchor == nil { readingAnchor=m.id } }
                    }
                    Color.clear.frame(height:1).id("personal-bottom")
                        .onAppear { following = true; readingAnchor = nil }
                        .onDisappear { following = false }
                }.padding(16)
            }
            .onChange(of:home.snapshot?.seq) { _, _ in if following { proxy.scrollTo("personal-bottom",anchor:.bottom) } }
            .onGeometryChange(for:CGFloat.self) { $0.size.width } action: { timelineWidth=$0 }
            .refreshable { await home.refresh(app:app) }
        }
    }
    private var composer: some View {
        VStack(alignment:.leading,spacing:10) {
            if let error = home.error {
                Button { app.present(APIError(summary:"请求失败",detail:error)) } label: { Text(verbatim:error).foregroundStyle(QuartetTheme.failed).font(.quartet(.detail)) }
                    .accessibilityIdentifier("personal-error")
            }
            ForEach(home.snapshot?.warnings ?? [],id:\.self) { warning in
                Text(verbatim:warning).font(.quartet(.detail)).foregroundStyle(QuartetTheme.failed).textSelection(.enabled)
            }
            ForEach(home.snapshot?.runs.filter { !($0.deliveryError ?? "").isEmpty } ?? []) { run in
                Text(verbatim:run.deliveryError ?? "").font(.quartet(.detail)).foregroundStyle(QuartetTheme.failed).textSelection(.enabled)
            }
            if selectedTask?.awaitingInput == true { Text(verbatim:selectedTask?.waitReason ?? "等待你的回复".localizedForApp).font(.quartet(.detail)).foregroundStyle(QuartetTheme.secondaryText) }
            if !taskID.isEmpty || selectedTask?.paused == true {
                HStack {
                    Text(selectedTask?.title ?? taskID).font(.quartet(.detail))
                    Spacer()
                    if selectedTask?.paused == true, let task=selectedTask {
                        Button("继续队列") { Task { await home.control(taskID:task.id,action:"resume",app:app) } }.frame(minHeight:44)
                    }
                    Button { taskID = "" } label: { Image(systemName:"xmark") }.frame(minWidth:44,minHeight:44).accessibilityLabel("关闭")
                }
            }
            ScrollView(.horizontal) {
                HStack(spacing:6) {
                    Button { choice = .agent } label: { ComposerMetadataChip(icon:"at",text:agents.first { $0.id == agentID }?.name ?? "主 Agent".localizedForApp,accessibilityLabel:"选择 Agent".localizedForApp) }
                        .contextMenu { if let agent=agents.first(where:{$0.id==agentID}),app.can("agent.manage") { Button("编辑 Agent") { editingAgent=agent } } }
                    Button { choice = .harness } label: { ComposerMetadataChip(icon:"cpu",text:harness?.displayName ?? "Harness Client",accessibilityLabel:"Harness Client") }
                    if harness?.models?.availableModels.isEmpty == false { Button { choice = .model } label: { ComposerMetadataChip(icon:"cpu",text:harness?.models?.availableModels.first { $0.id==effectiveModel }?.name ?? effectiveModel,accessibilityLabel:"模型".localizedForApp) } }
                    if harness?.modes?.availableModes.isEmpty == false { Button { choice = .mode } label: { ComposerMetadataChip(icon:"point.3.connected.trianglepath.dotted",text:harness?.modes?.availableModes.first { $0.id==effectiveMode }?.name ?? "模式".localizedForApp,accessibilityLabel:"模式".localizedForApp) } }
                    if thoughtOptions?.availableThoughtLevels.isEmpty == false { Button { choice = .thought } label: { ComposerMetadataChip(icon:"brain",text:thoughtOptions?.availableThoughtLevels.first { $0.id==effectiveThought }?.name ?? "思考强度".localizedForApp,accessibilityLabel:"思考强度".localizedForApp) } }
                    Button { choice = .task } label: { ComposerMetadataChip(icon:"tray",text:"后台任务".localizedForApp,accessibilityLabel:"后台任务".localizedForApp) }
                }.font(.quartet(.detail)).disabled(voiceBusy).frame(minHeight:44)
            }
            if voiceBusy {
                Text(verbatim:voice.transcript).font(.quartet(.regular)).textSelection(.enabled)
                HStack {
                    Button("取消录音") { voice.cancel() }.frame(minHeight:44)
                    Spacer()
                    if voice.isPreparing || voice.isFinishing { ProgressView() } else { Button("结束并发送") { voice.finish() }.frame(minHeight:44) }
                }
            } else {
                TextField("发消息，@ 选择 Agent",text:$text,axis:.vertical)
                    .lineLimit(2...7).font(.chat(.regular)).padding(.horizontal,4).padding(.vertical,8).accessibilityIdentifier("personal-message-input")
                    .onChange(of:text) { _,value in
                        if let mentioned=agents.sorted(by:{$0.name.count>$1.name.count}).first(where:{value.hasPrefix("@\($0.name) ")}),mentioned.id != agentID { selectAgent(mentioned.id) }
                    }
            }
            HStack {
                Button { choice = .attachment } label: { Image(systemName:"paperclip") }.frame(minWidth:44,minHeight:44).accessibilityLabel("添加附件").disabled(uploading || voiceBusy)
                Button { startVoice() } label: { Image(systemName:"mic") }.frame(minWidth:44,minHeight:44).accessibilityLabel("语音输入").disabled(voiceBusy || home.sending || !text.isEmpty)
                Toggle("新任务",isOn:$independent).toggleStyle(QuartetCheckmarkToggleStyle(layout:.compact)).disabled(voiceBusy)
                Spacer()
                if running {
                    Button { if text.trimmingCharacters(in:.whitespacesAndNewlines).isEmpty,let task=selectedTask { Task { await home.control(taskID:task.id,action:"stop",app:app) } } else { submit(stop:true) } } label: { Image(systemName:"stop.fill") }
                        .frame(minWidth:44,minHeight:44).accessibilityLabel("停止并发送")
                }
                Button { submit() } label: {
                    Image(systemName:"arrow.up").font(.chat(.compact,weight:.bold))
                        .foregroundStyle(sendDisabled ? QuartetTheme.secondaryText:QuartetTheme.onAccent)
                        .frame(width:30,height:30)
                        .background(sendDisabled ? QuartetTheme.elevated:QuartetTheme.accent,in:RoundedRectangle(cornerRadius:8,style:.continuous))
                        .frame(minWidth:44,minHeight:44)
                }.buttonStyle(.plain).accessibilityLabel((running ? "排队发送":"发送").localizedForApp)
                    .disabled(sendDisabled)
                    .accessibilityIdentifier("personal-send")
            }.font(.quartet(.detail))
        }
        .padding(12)
        .background(QuartetTheme.surface,in:RoundedRectangle(cornerRadius:20,style:.continuous))
        .overlay(RoundedRectangle(cornerRadius:20,style:.continuous).stroke(QuartetTheme.divider,lineWidth:1))
        .padding(.horizontal,12).padding(.vertical,8)
    }
    private func selectAgent(_ id:String) { agentID=id;taskID="";harnessID="";modelID="";modeID="";thoughtID="" }
    private func draft(_ content:String,stop:Bool=false) -> PersonalChatSend {
        var req=PersonalChatSend(content:content)
        req.agentId=agentID;req.taskId=taskID.isEmpty ? nil:taskID;req.harnessId=effectiveHarness.isEmpty ? nil:effectiveHarness
        req.modelId=effectiveModel.isEmpty ? nil:effectiveModel
        req.mode=modeID.isEmpty ? ((session?.harnessId == effectiveHarness ? session?.meta.acp_mode:nil) ?? harness?.modes?.currentModeId):modeID;req.thoughtLevel=effectiveThought.isEmpty ? nil:effectiveThought
        req.independent=independent;req.stopAndSend=stop;return req
    }
    private func submit(stop:Bool=false) {
        let submitted=text, request=draft(text,stop:stop)
        Task { if await home.send(request,app:app),text==submitted { text="";independent=false;following=true } }
    }
    private func startVoice() {
        let target=draft("")
        let captured=identity
        Task {
            await voice.start(onFinal:{ final in
                guard identity==captured else { return }
                var request=target;request.content=final;text=final
                Task { if await home.send(request,app:app),text==final { text="";independent=false;following=true } }
            },onError:{ error in text=voice.transcript;app.present(error) })
        }
    }
    private func upload(_ result:Result<[URL],Error>) async {
        uploading=true;defer { uploading=false }
        let captured=identity
        do {
            for url in try result.get() {
                let access=url.startAccessingSecurityScopedResource();defer { if access { url.stopAccessingSecurityScopedResource() } }
                let data=try Data(contentsOf:url)
                let attachment=try await app.apiClient().uploadFile(data:data,filename:url.lastPathComponent,mimeType:UTType(filenameExtension:url.pathExtension)?.preferredMIMEType ?? "application/octet-stream")
                guard captured==identity,!Task.isCancelled else { return }
                text += "\n[\(attachment.name)](\(attachment.path))"
            }
        } catch { if captured==identity { app.present(error) } }
    }
    private func choiceTitle(_ item:Choice) -> String {
        switch item { case .agent:"选择 Agent";case .harness:"Harness Client";case .model:"模型";case .mode:"模式";case .thought:"思考强度";case .task:"后台任务";case .attachment:"添加附件" }
    }
    private func choices(_ item:Choice) -> [QuartetChoice] {
        switch item {
        case .agent:agents.map { QuartetChoice(id:$0.id,title:$0.name) }
        case .harness:home.harnesses.map { QuartetChoice(id:$0.id,title:$0.displayName,detail:$0.error,disabled:!$0.available) }
        case .model:harness?.models?.availableModels.map { QuartetChoice(id:$0.id,title:$0.name) } ?? []
        case .mode:harness?.modes?.availableModes.map { QuartetChoice(id:$0.id,title:$0.name) } ?? []
        case .thought:thoughtOptions?.availableThoughtLevels.map { QuartetChoice(id:$0.id,title:$0.name) } ?? []
        case .attachment:[QuartetChoice(id:"photos",title:"照片".localizedForApp),QuartetChoice(id:"camera",title:"拍照".localizedForApp,disabled:!UIImagePickerController.isSourceTypeAvailable(.camera)),QuartetChoice(id:"files",title:"文件".localizedForApp)]
        case .task:(home.snapshot?.tasks ?? []).filter { !$0.id.hasPrefix("default-") }.map { QuartetChoice(id:$0.id,title:$0.title,detail:$0.paused ? "队列已暂停".localizedForApp:nil) }
        }
    }
    private func selection(_ item:Choice) -> Binding<String> {
        switch item {
        case .agent:Binding(get:{agentID},set:{ selectAgent($0) })
        case .harness:Binding(get:{effectiveHarness},set:{harnessID=$0;modelID="";modeID="";thoughtID=""})
        case .model:Binding(get:{effectiveModel},set:{modelID=$0;thoughtID=""})
        case .mode:Binding(get:{effectiveMode},set:{modeID=$0})
        case .thought:Binding(get:{effectiveThought},set:{thoughtID=$0})
        case .attachment:Binding(get:{""},set:{ value in
            let captured=identity;choice=nil
            Task{do{try await Task.sleep(for:.milliseconds(300))}catch{return};guard captured==identity else{return};switch value{case "photos":showsPhotos=true;case "camera":showsCamera=true;default:showsFiles=true}}
        })
        case .task:Binding(get:{taskID},set:{ id in if let task=home.snapshot?.tasks.first(where:{$0.id==id}) { selectAgent(task.agentId);taskID=id;independent=false } })
        }
    }
}

private struct PersonalChatMessageRow: View {
    @EnvironmentObject private var app: AppModel
    let contentWidth:CGFloat
    let message:PersonalChatMessage
    let agent:PersonalAgent?
    let debug:Bool
    let onReply:()->Void
    let onWithdraw:()->Void
    @State private var expanded=false
    @State private var detail:PersonalChatMessage?
    @State private var error:String?
    private func visible(_ text:String)->String {
        if debug { return text }
        var depth=0,rest=text[...],result=""
        while let opening=rest.range(of:"<system-reminder>"), let closing=rest.range(of:"</system-reminder>") {
            let next=opening.lowerBound<closing.lowerBound ? opening:closing
            if depth==0 { result += rest[..<next.lowerBound] }
            if next==opening { depth += 1 } else { depth=max(0,depth-1) }
            rest=rest[next.upperBound...]
        }
        while let opening=rest.range(of:"<system-reminder>") {
            if depth==0 { result += rest[..<opening.lowerBound] }
            depth += 1;rest=rest[opening.upperBound...]
        }
        while let closing=rest.range(of:"</system-reminder>") {
            if depth==0 { result += rest[..<closing.lowerBound] }
            depth=max(0,depth-1);rest=rest[closing.upperBound...]
        }
        if depth==0 { result += rest }
        let marker="<system-reminder>"
        for count in stride(from:marker.count-1,through:1,by:-1) {
            let partial=String(marker.prefix(count))
            if result.hasSuffix(partial) { result.removeLast(count);break }
        }
        return result
    }
    private var bodyText:String { debug ? detail?.rawContent ?? message.rawContent ?? detail?.content ?? message.content:visible(message.content) }
    private var rendered:ChatMessage {
        ChatMessage(id:message.id,kind:message.role=="user" ? .user:.assistant,content:bodyText,detail:nil,isFinished:!["queued","running","stopping"].contains(message.state),isFailed:message.state=="failed",timestamp:message.createdAt)
    }
    var body:some View {
        VStack(alignment:message.role=="user" ? .trailing:.leading,spacing:6) {
            if debug {
                Text(verbatim:bodyText).font(.chat(.detail,design:.monospaced)).textSelection(.enabled)
                    .frame(maxWidth:.infinity,alignment:.leading).padding(16)
                    .background(QuartetTheme.surface,in:RoundedRectangle(cornerRadius:16,style:.continuous))
                    .overlay(RoundedRectangle(cornerRadius:16,style:.continuous).stroke(QuartetTheme.divider,lineWidth:1))
            } else if message.role=="user" {
                UserMessageBubble(message:rendered,contentWidth:contentWidth)
            } else {
                AssistantMessageCard(message:rendered,agentName:message.role=="notification" ? "系统消息".localizedForApp:agent?.name ?? message.agentId,agentIconUrl:nil)
            }
            if message.state != "completed" || message.source.platform != nil {
                HStack(spacing:8) {
                    if let platform=message.source.platform { Text(verbatim:platform) }
                    if message.state != "completed" { Text(verbatim:stateTitle(message.state)).foregroundStyle(QuartetTheme.statusColor(message.state)) }
                }.font(.chat(.detail)).foregroundStyle(QuartetTheme.secondaryText)
            }
            if let error=message.error,!error.isEmpty { Text(verbatim:error).foregroundStyle(QuartetTheme.failed).textSelection(.enabled) }
            HStack(spacing:8) {
                if !message.taskId.isEmpty { Button(action:onReply) { Image(systemName:"arrowshape.turn.up.left").frame(minWidth:44,minHeight:44) }.accessibilityLabel("回复任务") }
                if message.role=="user",message.state=="queued" { Button("撤回",action:onWithdraw).frame(minHeight:44) }
                if message.role=="assistant" {
                    Button { expanded.toggle() } label: {
                        Image(systemName:expanded ? "chevron.down":"chevron.right")
                        Text("思考与工具")
                    }.frame(minHeight:44).accessibilityIdentifier("personal-message-details")
                }
            }.font(.chat(.detail)).foregroundStyle(QuartetTheme.secondaryText)
            if expanded {
                VStack(alignment:.leading,spacing:12) {
                    Text(verbatim:message.sessionId).font(.chat(.compact)).foregroundStyle(QuartetTheme.secondaryText).textSelection(.enabled)
                    if let thought=detail?.thought,!thought.isEmpty { Text(verbatim:visible(thought)).font(.chat(.detail)).textSelection(.enabled) }
                    ForEach(detail?.tools ?? []) { tool in
                        DisclosureGroup(tool.name) { Text(verbatim:visible(tool.args)+"\n"+visible(tool.result)).font(.chat(.detail,design:.monospaced)).textSelection(.enabled) }
                    }
                }.padding(14).frame(maxWidth:.infinity,alignment:.leading)
                    .background(QuartetTheme.surface,in:RoundedRectangle(cornerRadius:16,style:.continuous))
                    .overlay(RoundedRectangle(cornerRadius:16,style:.continuous).stroke(QuartetTheme.divider,lineWidth:1))
            }
            if let error { Text(verbatim:error).foregroundStyle(QuartetTheme.failed).textSelection(.enabled) }
        }
        .foregroundStyle(QuartetTheme.primaryText)
        .task(id:"\(expanded)|\(debug)|\(message.revision)|\(app.serverAddress)|\(app.username)") {
            guard expanded || debug else { return }
            do { let value=try await app.apiClient().personalChatMessage(message.id);guard !Task.isCancelled else { return };detail=value.message;error=nil }
            catch { if !Task.isCancelled { self.error=String(reflecting:error) } }
        }
    }
    private func stateTitle(_ state:String)->String {
        switch state { case "queued":"排队中".localizedForApp;case "running":"处理中".localizedForApp;case "stopping":"停止中".localizedForApp;case "failed":"失败".localizedForApp;case "stopped":"已停止".localizedForApp;case "interrupted":"已中断".localizedForApp;case "withdrawn":"已撤回".localizedForApp;default:state }
    }
}

struct PersonalChatSettingsView: View {
    @EnvironmentObject private var app:AppModel
    @State private var settings:PersonalChatSettings?
    @State private var saving=false
    var body:some View {
        ScrollView {
            if let current=settings {
                VStack(alignment:.leading,spacing:20) {
                    Toggle("调试模式：显示 system-reminder",isOn:Binding(get:{settings?.debug ?? false},set:{settings?.debug=$0}))
                        .toggleStyle(QuartetCheckmarkToggleStyle())
                    Text("普通模式隐藏内部提示；调试模式显示实际发送的完整消息，不改变 Session 或执行。").font(.quartet(.detail)).foregroundStyle(QuartetTheme.secondaryText)
                    TextField("Session 空闲轮换（分钟）",value:Binding(get:{settings?.idleMinutes ?? 30},set:{settings?.idleMinutes=$0}),format:.number).keyboardType(.numberPad)
                    TextField("并行任务上限",value:Binding(get:{settings?.maxConcurrent ?? 8},set:{settings?.maxConcurrent=$0}),format:.number).keyboardType(.numberPad)
                    Button("保存") {
                        saving=true
                        Task {
                            defer { saving=false }
                            do { let result=try await app.apiClient().personalChatSettings(current);guard !Task.isCancelled else{return};settings=result.settings }
                            catch { if !Task.isCancelled { app.present(error) } }
                        }
                    }.disabled(saving || !app.can("config.write")).frame(minHeight:44)
                }.padding(16).font(.quartet(.regular)).foregroundStyle(QuartetTheme.primaryText)
            } else { ProgressView() }
        }
        .background(QuartetTheme.canvas)
        .quartetNavigationTitle("首页聊天设置").quartetPlainNavigationBackButton()
        .task(id:"\(app.serverAddress)|\(app.username)") {
            do { let result=try await app.apiClient().personalChatSettings();guard !Task.isCancelled else{return};settings=result.settings }
            catch { if !Task.isCancelled { app.present(error) } }
        }
    }
}

private struct PersonalAgentEditor:View {
    @EnvironmentObject private var app:AppModel
    @Environment(\.dismiss) private var dismiss
    @State var agent:PersonalAgent
    let harnesses:[AgentSummary]
    let onSaved:()->Void
    @State private var saving=false
    @State private var showsHarness=false
    @State private var confirmsDelete=false
    @State private var error:PresentedError?
    var body:some View {
        NavigationStack {
            ScrollView {
                VStack(alignment:.leading,spacing:18) {
                    TextField("Agent 名称",text:$agent.name)
                    Text("SystemPrompt").font(.quartet(.detail)).foregroundStyle(QuartetTheme.secondaryText)
                    TextEditor(text:$agent.systemPrompt).frame(minHeight:220)
                    Button { showsHarness=true } label: { Text(verbatim:agent.harnessId.flatMap { id in harnesses.first { $0.id==id }?.displayName } ?? "跟随对话选择".localizedForApp) }.frame(minHeight:44)
                    Button("保存") { save() }.disabled(saving || agent.name.trimmingCharacters(in:.whitespacesAndNewlines).isEmpty).frame(minHeight:44)
                    if !agent.main,!agent.id.isEmpty {
                        Button("删除 Agent",role:.destructive) { confirmsDelete=true }.frame(minHeight:44)
                    }
                }.padding(16).font(.quartet(.regular)).foregroundStyle(QuartetTheme.primaryText)
            }
            .background(QuartetTheme.canvas).quartetNavigationTitle("编辑 Agent")
            .toolbar { ToolbarItem(placement:.topBarTrailing) { Button { dismiss() } label: { Image(systemName:"xmark") }.accessibilityLabel("关闭") } }
            .sheet(isPresented:$showsHarness) { QuartetChoiceSheet(title:"默认 Harness Client",choices:[QuartetChoice(id:"",title:"跟随对话选择".localizedForApp)]+harnesses.map { QuartetChoice(id:$0.id,title:$0.displayName) },selection:Binding(get:{agent.harnessId ?? ""},set:{agent.harnessId=$0}),accessibilityPrefix:"personal-agent-harness") }
            .sheet(item:$error) { ErrorDetailView(error:$0) }
            .alert("删除 Agent",isPresented:$confirmsDelete) {
                Button("关闭",role:.cancel) { }
                Button("删除 Agent",role:.destructive) { agent.deleted=true;save() }
            } message: { Text("历史记录保留，有排队或运行任务时不能删除。") }
        }.quartetSheetStyle()
    }
    private func save() {
        saving=true
        Task {
            defer { saving=false }
            do { _ = try await app.apiClient().personalChatAgent(agent);guard !Task.isCancelled else{return};onSaved();dismiss() }
            catch { self.error=PresentedError(title:"请求失败",detail:String(reflecting:error)) }
        }
    }
}

private struct PersonalChatHistoryView:View {
    @EnvironmentObject private var app:AppModel
    @Environment(\.dismiss) private var dismiss
    let onReply:(PersonalChatMessage)->Void
    @State private var query=""
    @State private var result:PersonalChatSearch?
    @State private var history:[PersonalChatMessage]=[]
    @State private var version=UUID()
    @State private var error:PresentedError?
    var body:some View {
        NavigationStack {
            ScrollView {
                VStack(alignment:.leading,spacing:20) {
                    TextField("搜索历史消息",text:$query).onSubmit { search() }.accessibilityIdentifier("personal-history-query")
                    Button("搜索") { search() }.frame(minHeight:44).disabled(query.trimmingCharacters(in:.whitespacesAndNewlines).isEmpty)
                    ForEach(result?.hits ?? []) { hit in
                        Button {
                            let captured=UUID();version=captured
                            Task {
                                do { let page=try await app.apiClient().personalChatHistory(sessionID:hit.sessionId,messageID:hit.messageId);guard version==captured,!Task.isCancelled else{return};history=page.messages }
                                catch { if version==captured,!Task.isCancelled { self.error=PresentedError(title:"请求失败",detail:String(reflecting:error)) } }
                            }
                        } label: { Text(verbatim:hit.excerpt).multilineTextAlignment(.leading) }.frame(minHeight:44)
                    }
                    if let result,result.hits.count<result.total {
                        Button("更多结果") { search(offset:result.hits.count) }.frame(minHeight:44)
                    }
                    ForEach(history) { m in
                        VStack(alignment:.leading,spacing:8) {
                            Text(verbatim:m.content).textSelection(.enabled)
                            Button("回复任务") { onReply(m) }.frame(minHeight:44)
                        }.padding(.vertical,8)
                    }
                }.padding(16).font(.quartet(.regular)).foregroundStyle(QuartetTheme.primaryText)
            }
            .background(QuartetTheme.canvas).quartetNavigationTitle("历史 Session 检索")
            .toolbar { ToolbarItem(placement:.topBarTrailing) { Button { dismiss() } label: { Image(systemName:"xmark") }.accessibilityLabel("关闭") } }
            .sheet(item:$error) { ErrorDetailView(error:$0) }
            .onDisappear { version=UUID() }
        }.quartetSheetStyle()
    }
    private func search(offset:Int=0) {
        let captured=UUID();version=captured
        Task {
            do {
                let page=try await app.apiClient().personalChatSearch(query,offset:offset)
                guard version==captured,!Task.isCancelled else{return}
                if offset>0,let old=result { result=PersonalChatSearch(code:page.code,total:page.total,hits:old.hits+page.hits) } else { result=page;history=[] }
            } catch { if version==captured,!Task.isCancelled { self.error=PresentedError(title:"请求失败",detail:String(reflecting:error)) } }
        }
    }
}
