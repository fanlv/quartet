import SwiftUI

struct ScheduledTasksView: View {
    var body: some View {
        NavigationStack {
            VStack(spacing: 12) {
                Image(systemName: "calendar.badge.clock")
                    .font(.system(size: 34, weight: .semibold))
                    .foregroundStyle(QuartetTheme.accent)
                    .accessibilityHidden(true)

                Text("TODO")
                    .font(.quartet(.large, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)

                Text("定时任务后续实现")
                    .font(.quartet(.regular))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .padding(.horizontal, 24)
            .background(QuartetTheme.canvas)
            .navigationTitle("定时任务")
            .navigationBarTitleDisplayMode(.inline)
        }
        .toolbarBackground(QuartetTheme.canvas, for: .navigationBar)
        .toolbarBackground(.visible, for: .navigationBar)
    }
}
