import SwiftUI
import UIKit
import Combine

struct RootView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.scenePhase) private var scenePhase
    @State private var selectedTab = 0

    var body: some View {
        ZStack {
            QuartetTheme.canvas.ignoresSafeArea()

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
        .background(QuartetKeyboardDismissInstaller())
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

/// Installs one non-blocking tap recognizer on the app window so every current and
/// future text input follows the same keyboard-dismissal behavior. Taps inside a
/// text field or text view are left alone, including their internal UIKit subviews.
private struct QuartetKeyboardDismissInstaller: UIViewRepresentable {
    func makeUIView(context: Context) -> ResolverView {
        ResolverView()
    }

    func updateUIView(_ uiView: ResolverView, context: Context) {
        uiView.installGestureRecognizerIfPossible()
    }

    static func dismantleUIView(_ uiView: ResolverView, coordinator: Void) {
        uiView.removeGestureRecognizer()
    }

    final class ResolverView: UIView, UIGestureRecognizerDelegate {
        private weak var gestureContainer: UIWindow?
        private lazy var gestureRecognizer: UITapGestureRecognizer = {
            let recognizer = UITapGestureRecognizer(target: self, action: #selector(dismissKeyboard))
            recognizer.cancelsTouchesInView = false
            recognizer.delegate = self
            return recognizer
        }()

        override func didMoveToWindow() {
            super.didMoveToWindow()
            installGestureRecognizerIfPossible()
        }

        fileprivate func installGestureRecognizerIfPossible() {
            guard gestureContainer !== window else { return }
            removeGestureRecognizer()
            window?.addGestureRecognizer(gestureRecognizer)
            gestureContainer = window
        }

        fileprivate func removeGestureRecognizer() {
            gestureContainer?.removeGestureRecognizer(gestureRecognizer)
            gestureContainer = nil
        }

        func gestureRecognizer(_ gestureRecognizer: UIGestureRecognizer, shouldReceive touch: UITouch) -> Bool {
            var touchedView = touch.view
            while let view = touchedView {
                if view is UITextField || view is UITextView {
                    return false
                }
                touchedView = view.superview
            }
            return true
        }

        func gestureRecognizer(
            _ gestureRecognizer: UIGestureRecognizer,
            shouldRecognizeSimultaneouslyWith otherGestureRecognizer: UIGestureRecognizer
        ) -> Bool {
            true
        }

        @objc private func dismissKeyboard() {
            gestureContainer?.endEditing(true)
        }
    }
}

private struct MainTabBarInsetKey: EnvironmentKey {
    static let defaultValue: CGFloat = 0
}

extension EnvironmentValues {
    /// Height the docked main tab bar covers above the existing bottom safe area, or 0 while hidden.
    /// The tab bar is drawn as an overlay, so scrollable tab content has to reserve this itself.
    var mainTabBarInset: CGFloat {
        get { self[MainTabBarInsetKey.self] }
        set { self[MainTabBarInsetKey.self] = newValue }
    }
}

extension View {
    /// Reserves the part of the docked main tab bar not already covered by the system safe area.
    func mainTabBarBottomInset(_ inset: CGFloat) -> some View {
        safeAreaInset(edge: .bottom, spacing: 0) {
            Color.clear
                .frame(height: max(inset, 0))
                .accessibilityHidden(true)
        }
    }
}

private struct MainView: View {
    @Binding var selectedTab: Int
    @State private var showsTabBar = true
    @State private var keyboardIsVisible = false

    private var displaysTabBar: Bool { showsTabBar && !keyboardIsVisible }

    var body: some View {
        GeometryReader { proxy in
            let bottomSafeAreaHeight = max(proxy.safeAreaInsets.bottom, 0)
            let tabBarContentInset = max(
                MainTabBar.height(bottomSafeAreaHeight: bottomSafeAreaHeight) - bottomSafeAreaHeight,
                0
            )
            ZStack {
                QuartetTheme.canvas.ignoresSafeArea()

                // The tab bar is an overlay rather than a `VStack` sibling on purpose: keeping it out of
                // the layout flow makes the content region a constant full-screen rect, so pushing the
                // chat (which hides the tab bar) never resizes the region. Resizing it left the bottom
                // strip clipped to the old height — rendered as bare canvas — until an unrelated state
                // change re-dirtied the view a few frames later.
                Group {
                    switch selectedTab {
                    case 1:
                        ScheduledTasksView(showsMainTabBar: $showsTabBar)
                    case 2:
                        StatsView()
                    case 3:
                        SettingsView()
                    default:
                        JobsView(showsMainTabBar: $showsTabBar)
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                // A `safeAreaInset` here would not reach the ScrollViews inside each tab's
                // NavigationStack, so the overlap above the system safe area is published instead
                // and each tab applies it to its own scrollable content via `mainTabBarInset`.
                .environment(
                    \.mainTabBarInset,
                    displaysTabBar ? tabBarContentInset : 0
                )
                .overlay(alignment: .bottom) {
                    if displaysTabBar {
                        MainTabBar(selection: $selectedTab, bottomSafeAreaHeight: bottomSafeAreaHeight)
                    }
                }
            }
            .ignoresSafeArea(.container, edges: .bottom)
        }
        .onChange(of: selectedTab) { _, _ in
            setTabBarVisible(true)
        }
        .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillShowNotification)) { _ in
            setKeyboardVisible(true)
        }
        .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardDidHideNotification)) { _ in
            setKeyboardVisible(false)
        }
    }

    private func setTabBarVisible(_ isVisible: Bool) {
        var transaction = Transaction()
        transaction.disablesAnimations = true
        withTransaction(transaction) {
            showsTabBar = isVisible
        }
    }

    private func setKeyboardVisible(_ isVisible: Bool) {
        var transaction = Transaction()
        transaction.disablesAnimations = true
        withTransaction(transaction) {
            keyboardIsVisible = isVisible
        }
    }
}

private struct MainTabBar: View {
    @Binding var selection: Int
    let bottomSafeAreaHeight: CGFloat

    private let items = [
        TabItem(id: 0, title: "最近任务", systemImage: "clock.arrow.circlepath"),
        TabItem(id: 1, title: "定时任务", systemImage: "calendar.badge.clock"),
        TabItem(id: 2, title: "统计", systemImage: "chart.xyaxis.line"),
        TabItem(id: 3, title: "设置", systemImage: "slider.horizontal.3")
    ]
    private static let contentHeight: CGFloat = 49
    private let itemContentVerticalOffset: CGFloat = 5

    static func height(bottomSafeAreaHeight: CGFloat) -> CGFloat {
        contentHeight + max(bottomSafeAreaHeight, 0)
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 0) {
                ForEach(items) { item in
                    Button { selection = item.id } label: {
                        VStack(spacing: 1) {
                            Image(systemName: item.systemImage)
                                .font(.quartet(.large, weight: selection == item.id ? .semibold : .regular))
                                .symbolVariant(selection == item.id ? .fill : .none)
                                .frame(height: 25)
                            Text(LocalizedStringKey(item.title))
                                .font(.quartet(.compact, weight: selection == item.id ? .semibold : .regular))
                                .lineLimit(1)
                                .minimumScaleFactor(0.86)
                        }
                        .foregroundStyle(selection == item.id ? QuartetTheme.accent : QuartetTheme.secondaryText)
                        .offset(y: itemContentVerticalOffset)
                        .frame(maxWidth: .infinity)
                        .frame(height: Self.contentHeight)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(Text(LocalizedStringKey(item.title)))
                    .accessibilityAddTraits(selection == item.id ? .isSelected : [])
                    .accessibilityIdentifier("main-tab-\(item.id)")
                }
            }
            .frame(height: Self.contentHeight)
            .padding(.horizontal, 4)

            if bottomSafeAreaHeight > 0 {
                Color.clear
                    .frame(height: bottomSafeAreaHeight)
            }
        }
        .frame(maxWidth: .infinity)
        .frame(height: Self.height(bottomSafeAreaHeight: bottomSafeAreaHeight))
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
                    .font(.quartet(.control, weight: .semibold, design: .monospaced))
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
                    .font(.quartet(.detail, design: .monospaced))
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(20)
            }
            .background(QuartetTheme.canvas)
            .quartetNavigationTitle(error.title.localizedForApp)
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
