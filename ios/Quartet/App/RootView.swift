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
        .onChange(of: scenePhase) { _, phase in
            Task { await model.handleScenePhaseChange(phase) }
        }
        .sheet(item: $model.presentedError) { error in
            ErrorDetailView(error: error)
        }
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
