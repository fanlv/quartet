import SwiftUI
import UIKit

struct RootView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.scenePhase) private var scenePhase
    @State private var selectedTab = 0

    var body: some View {
        Group {
            switch model.canPresentDashboard ? .connected : model.phase {
            case .booting:
                LaunchView()
            case .disconnected, .connecting:
                ConnectionView()
            case .connected:
                MainView(selectedTab: $selectedTab)
            }
        }
        .tint(QuartetTheme.accent)
        .task {
            await model.bootstrap()
        }
        .task(id: notificationPollingEnabled) {
            guard notificationPollingEnabled else { return }
            let clock = ContinuousClock()
            var nextPoll = clock.now.advanced(by: .seconds(5))
            while !Task.isCancelled {
                do {
                    try await clock.sleep(until: nextPoll)
                } catch {
                    return
                }
                guard notificationPollingEnabled else { return }
                nextPoll = nextPoll.advanced(by: .seconds(5))
                await model.pollNotifications()
                if nextPoll < clock.now {
                    nextPoll = clock.now
                }
            }
        }
        .onChange(of: scenePhase) { _, phase in
            Task { await model.handleScenePhaseChange(phase) }
        }
        .onChange(of: model.pendingNotificationDestination) { _, destination in
            guard destination != nil else { return }
            selectedTab = 0
        }
        .sheet(item: $model.presentedError) { error in
            ErrorDetailView(error: error)
        }
    }

    private var notificationPollingEnabled: Bool {
        scenePhase == .active && model.phase == .connected
    }
}

private struct MainView: View {
    @Binding var selectedTab: Int

    var body: some View {
        TabView(selection: $selectedTab) {
            JobsView()
                .tag(0)
                .tabItem { Label("运行台", systemImage: "waveform.path.ecg") }

            SettingsView()
                .tag(1)
                .tabItem { Label("设置", systemImage: "slider.horizontal.3") }
        }
    }
}

private struct LaunchView: View {
    var body: some View {
        ZStack {
            QuartetTheme.canvas.ignoresSafeArea()
            VStack(spacing: 16) {
                PulseMark()
                Text("QUARTET")
                    .font(.system(size: 15, weight: .semibold, design: .monospaced))
                    .tracking(4)
                    .foregroundStyle(QuartetTheme.secondaryText)
                ProgressView()
                    .tint(QuartetTheme.accent)
            }
        }
    }
}

struct ErrorDetailView: View {
    @Environment(\.dismiss) private var dismiss
    let error: PresentedError

    var body: some View {
        NavigationStack {
            ScrollView {
                Text(error.detail)
                    .font(.system(.footnote, design: .monospaced))
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(20)
            }
            .background(QuartetTheme.canvas)
            .navigationTitle(error.title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button("复制") { UIPasteboard.general.string = error.detail }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("关闭") { dismiss() }
                }
            }
        }
        .presentationDetents([.medium, .large])
    }
}

struct PresentedError: Identifiable {
    let id = UUID()
    let title: String
    let detail: String
}
