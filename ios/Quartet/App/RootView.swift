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
    @State private var showsTabBar = true

    var body: some View {
        GeometryReader { proxy in
            ZStack(alignment: .bottom) {
                TabView(selection: $selectedTab) {
                    JobsView(showsMainTabBar: $showsTabBar)
                        .tag(0)
                        .tabItem { Label("最近任务", systemImage: "clock.arrow.circlepath") }

                    StatsView()
                        .tag(1)
                        .tabItem { Label("统计", systemImage: "chart.xyaxis.line") }

                    SettingsView()
                        .tag(2)
                        .tabItem { Label("设置", systemImage: "slider.horizontal.3") }
                }
                .toolbar(.hidden, for: .tabBar)

                if showsTabBar {
                    TransparentTabBar(selection: $selectedTab)
                        .padding(.horizontal, 12)
                        .padding(.bottom, max(6, proxy.safeAreaInsets.bottom - 2))
                        .transition(.move(edge: .bottom).combined(with: .opacity))
                }
            }
            .ignoresSafeArea(.container, edges: .bottom)
            .animation(.snappy(duration: 0.24), value: showsTabBar)
            .onChange(of: selectedTab) { _, _ in
                showsTabBar = true
            }
        }
    }
}

private struct TransparentTabBar: View {
    @Binding var selection: Int

    private let items = [
        TabItem(id: 0, title: "最近任务", systemImage: "clock.arrow.circlepath"),
        TabItem(id: 1, title: "统计", systemImage: "chart.xyaxis.line"),
        TabItem(id: 2, title: "设置", systemImage: "slider.horizontal.3")
    ]

    var body: some View {
        HStack(spacing: 0) {
            ForEach(items) { item in
                Button { selection = item.id } label: {
                    VStack(spacing: 3) {
                        Image(systemName: item.systemImage)
                            .font(.system(size: 21, weight: selection == item.id ? .semibold : .medium))
                            .symbolVariant(selection == item.id ? .fill : .none)
                        Text(item.title)
                            .font(.caption2.weight(selection == item.id ? .bold : .semibold))
                    }
                    .foregroundStyle(selection == item.id ? QuartetTheme.accent : QuartetTheme.primaryText)
                    .frame(maxWidth: .infinity, minHeight: 54)
                    .contentShape(Rectangle())
                    .shadow(color: QuartetTheme.canvas, radius: 2)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(item.title)
                .accessibilityAddTraits(selection == item.id ? .isSelected : [])
                .accessibilityIdentifier("main-tab-\(item.id)")
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("main-tab-bar")
    }

    private struct TabItem: Identifiable {
        let id: Int
        let title: String
        let systemImage: String
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
                .sharedBackgroundVisibility(.hidden)
                ToolbarItem(placement: .confirmationAction) {
                    Button("关闭") { dismiss() }
                }
                .sharedBackgroundVisibility(.hidden)
            }
        }
        .presentationDetents([.medium, .large])
        .quartetSheetStyle()
    }
}

struct PresentedError: Identifiable {
    let id = UUID()
    let title: String
    let detail: String
}
