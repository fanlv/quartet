import SwiftUI
import UserNotifications

struct NotificationsView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        List {
            Section("通知权限") {
                HStack {
                    Label(authorizationTitle, systemImage: authorizationIcon)
                    Spacer()
                    if model.notificationAuthorizationStatus == .notDetermined {
                        Button("启用") {
                            Task { await model.requestNotificationAuthorization() }
                        }
                    }
                }
                .foregroundStyle(QuartetTheme.primaryText)
            }

            Section("通知偏好") {
                preferenceToggle(.jobCompleted)
                preferenceToggle(.jobFailed)
                preferenceToggle(.awaitingInput)
                preferenceToggle(.connectionIssue)
            }

            Section {
                if model.notifications.isEmpty {
                    ContentUnavailableView(
                        "暂无通知",
                        systemImage: "bell.slash",
                        description: Text("完成、失败、等待人工和连接异常事件会出现在这里。")
                    )
                    .frame(maxWidth: .infinity, minHeight: 220)
                    .listRowBackground(Color.clear)
                } else {
                    ForEach(model.notifications) { record in
                        Button {
                            if record.jobID != nil {
                                model.openNotification(record)
                                dismiss()
                            } else {
                                model.markNotificationRead(record)
                            }
                        } label: {
                            NotificationRow(record: record)
                        }
                        .buttonStyle(.plain)
                        .disabled(record.jobID == nil && !record.isUnread)
                    }
                }
            } header: {
                HStack {
                    Text("应用内通知")
                    Spacer()
                    if !model.notifications.isEmpty {
                        Button("全部已读") { model.markAllNotificationsRead() }
                    }
                }
            }
        }
        .scrollContentBackground(.hidden)
        .background(QuartetTheme.canvas)
        .navigationTitle("通知中心")
        .toolbar {
            if !model.notifications.isEmpty {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("清空") { model.clearNotificationRecords() }
                }
            }
        }
    }

    private func preferenceToggle(_ kind: QuartetNotificationKind) -> some View {
        Toggle(isOn: Binding(
            get: { model.notificationPreferences.isEnabled(kind) },
            set: { model.setNotificationPreference(kind, enabled: $0) }
        )) {
            Label(kind.title, systemImage: kind.systemImage)
        }
    }

    private var authorizationTitle: String {
        switch model.notificationAuthorizationStatus {
        case .authorized, .provisional, .ephemeral:
            "本地通知已启用"
        case .denied:
            "本地通知已关闭"
        case .notDetermined:
            "尚未请求通知权限"
        default:
            "通知权限受限"
        }
    }

    private var authorizationIcon: String {
        switch model.notificationAuthorizationStatus {
        case .authorized, .provisional, .ephemeral:
            "bell.badge.fill"
        case .denied:
            "bell.slash.fill"
        case .notDetermined:
            "bell.badge"
        default:
            "bell.slash"
        }
    }
}

private struct NotificationRow: View {
    let record: QuartetNotificationRecord

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: record.kind.systemImage)
                .foregroundStyle(iconColor)
                .frame(width: 18, height: 18)
                .padding(.top, 2)

            VStack(alignment: .leading, spacing: 6) {
                HStack {
                    Text(record.title)
                        .font(.headline)
                        .foregroundStyle(QuartetTheme.primaryText)
                    if record.isUnread {
                        Circle()
                            .fill(QuartetTheme.accent)
                            .frame(width: 8, height: 8)
                    }
                    Spacer()
                    Text(record.createdAt, style: .relative)
                        .font(.caption)
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                Text(record.body)
                    .font(.subheadline)
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .multilineTextAlignment(.leading)
                if let jobID = record.jobID, !jobID.isEmpty {
                    Text("JOB \(jobID)")
                        .font(.system(.caption, design: .monospaced).weight(.semibold))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
            }
        }
        .padding(.vertical, 6)
    }

    private var iconColor: Color {
        switch record.kind {
        case .jobCompleted: QuartetTheme.accent
        case .jobFailed, .connectionIssue: QuartetTheme.failed
        case .awaitingInput: QuartetTheme.running
        }
    }
}
