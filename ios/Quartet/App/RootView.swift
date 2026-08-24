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
        .background {
            QuartetTheme.canvas.ignoresSafeArea()
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
            VStack(spacing: 0) {
                Group {
                    switch selectedTab {
                    case 1:
                        StatsView()
                    case 2:
                        SettingsView()
                    default:
                        JobsView(showsMainTabBar: $showsTabBar)
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)

                if showsTabBar {
                    MainTabBar(selection: $selectedTab, bottomSafeAreaHeight: proxy.safeAreaInsets.bottom)
                        .transition(.move(edge: .bottom).combined(with: .opacity))
                }
            }
            .ignoresSafeArea(.container, edges: showsTabBar ? .bottom : Edge.Set())
        }
        .onChange(of: selectedTab) { _, _ in
            showsTabBar = true
        }
    }
}

private struct MainTabBar: View {
    @Binding var selection: Int
    let bottomSafeAreaHeight: CGFloat

    private let items = [
        TabItem(id: 0, title: "最近任务", systemImage: "clock.arrow.circlepath"),
        TabItem(id: 1, title: "统计", systemImage: "chart.xyaxis.line"),
        TabItem(id: 2, title: "设置", systemImage: "slider.horizontal.3")
    ]
    private let contentHeight: CGFloat = 49
    private let itemContentVerticalOffset: CGFloat = 5

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 0) {
                ForEach(items) { item in
                    Button { selection = item.id } label: {
                        VStack(spacing: 1) {
                            Image(systemName: item.systemImage)
                                .font(.system(size: 22, weight: selection == item.id ? .semibold : .regular))
                                .symbolVariant(selection == item.id ? .fill : .none)
                                .frame(height: 25)
                            Text(item.title)
                                .font(.quartet(.compact, weight: selection == item.id ? .semibold : .regular))
                                .lineLimit(1)
                                .minimumScaleFactor(0.86)
                        }
                        .foregroundStyle(selection == item.id ? QuartetTheme.accent : QuartetTheme.secondaryText)
                        .offset(y: itemContentVerticalOffset)
                        .frame(maxWidth: .infinity)
                        .frame(height: contentHeight)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(item.title)
                    .accessibilityAddTraits(selection == item.id ? .isSelected : [])
                    .accessibilityIdentifier("main-tab-\(item.id)")
                }
            }
            .frame(height: contentHeight)
            .padding(.horizontal, 4)

            if bottomSafeAreaHeight > 0 {
                Color.clear
                    .frame(height: bottomSafeAreaHeight)
            }
        }
        .frame(maxWidth: .infinity)
        .frame(height: contentHeight + max(bottomSafeAreaHeight, 0))
        .background(QuartetTheme.surface)
        .overlay(alignment: .top) {
            Rectangle()
                .fill(QuartetTheme.divider)
                .frame(height: 0.5)
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
