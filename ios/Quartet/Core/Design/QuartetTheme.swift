import SwiftUI
import UIKit

enum QuartetTheme {
    // Warm paper and carbon surfaces keep orange and terminal green crisp in both appearances.
    static let canvas = dynamic(light: 0xF6F2EC, dark: 0x080907)
    static let surface = dynamic(light: 0xFFFDFC, dark: 0x11130F)
    static let elevated = dynamic(light: 0xECE5DC, dark: 0x1B1F18)
    static let primaryText = dynamic(light: 0x181410, dark: 0xF7F2EA)
    static let secondaryText = dynamic(light: 0x685E54, dark: 0xABA297)
    static let divider = dynamic(light: 0xD8CEC3, dark: 0x33382F)

    // Orange owns brand and primary actions. Green is reserved for live, healthy, and terminal states.
    static let accent = dynamic(light: 0xC64B00, dark: 0xFF7A1A)
    static let accentDeep = dynamic(light: 0x8F3500, dark: 0xFF9D52)
    static let accentSoft = dynamic(light: 0xE46A16, dark: 0xD85D0B)
    static let onAccent = dynamic(light: 0xFFFFFF, dark: 0x160A02)
    static let terminalGreen = dynamic(light: 0x08783E, dark: 0x4DFF88)
    static let terminalGreenMuted = dynamic(light: 0x3E704D, dark: 0x79C98F)
    static let success = terminalGreenMuted
    static let running = terminalGreen
    static let warning = accent
    static let failed = dynamic(light: 0xB62435, dark: 0xFF5364)
    static let onDanger = dynamic(light: 0xFFFFFF, dark: 0x190205)
    static let stopped = dynamic(light: 0x686159, dark: 0xA39B91)

    // Code surfaces deliberately stay dark: this is the focused hacker-console moment.
    static let terminalBackground = dynamic(light: 0x10130F, dark: 0x040704)
    static let terminalText = dynamic(light: 0x42E978, dark: 0x62FF98)
    static let terminalBorder = dynamic(light: 0x285F39, dark: 0x245D36)

    // Charts use approved hues only; line shape and labels remain the primary differentiators.
    static let chartOrange = accent
    static let chartDeepOrange = accentDeep
    static let chartSoftOrange = accentSoft
    static let chartGreen = terminalGreen
    static let chartMutedGreen = terminalGreenMuted
    static let chartRed = failed
    static let chartGraphite = dynamic(light: 0x4F4942, dark: 0xC4BBB0)

    static func statusColor(_ status: String) -> Color {
        switch status.lowercased() {
        case "running": running
        case "pending", "awaitinginput", "stepstopping": warning
        case "completed": success
        case "failed", "timedout": failed
        default: stopped
        }
    }

    static func workspaceTint(_ seed: String?) -> Color {
        guard let seed, !seed.isEmpty else { return accent }
        let checksum = seed.unicodeScalars.reduce(0) { ($0 &* 31 &+ Int($1.value)) & 0x7fffffff }
        let palette = [chartOrange, chartGreen, chartDeepOrange, chartMutedGreen, chartGraphite]
        return palette[checksum % palette.count]
    }

    private static func dynamic(light: UInt32, dark: UInt32) -> Color {
        Color(uiColor: UIColor { traits in
            UIColor(rgb: traits.userInterfaceStyle == .dark ? dark : light)
        })
    }
}

enum QuartetFontSize {
    case large
    case regular
    case control
    case detail
    case compact

    fileprivate var textStyle: Font.TextStyle {
        switch self {
        case .large: .title3
        case .regular: .body
        case .control: .subheadline
        case .detail: .footnote
        case .compact: .caption2
        }
    }
}

extension Font {
    static func quartet(
        _ size: QuartetFontSize,
        weight: Font.Weight? = nil,
        design: Font.Design = .default
    ) -> Font {
        .system(size.textStyle, design: design, weight: weight)
    }
}

private extension UIColor {
    convenience init(rgb: UInt32) {
        self.init(
            red: CGFloat((rgb >> 16) & 0xff) / 255,
            green: CGFloat((rgb >> 8) & 0xff) / 255,
            blue: CGFloat(rgb & 0xff) / 255,
            alpha: 1
        )
    }
}

struct PulseMark: View {
    var body: some View {
        HStack(spacing: 5) {
            ForEach(0..<4, id: \.self) { index in
                RoundedRectangle(cornerRadius: 2)
                    .fill(index == 2 ? QuartetTheme.accent : QuartetTheme.terminalGreenMuted.opacity(0.58))
                    .frame(width: 4, height: [12, 24, 36, 18][index])
            }
        }
        .accessibilityHidden(true)
    }
}

struct RunningPulseLine: View {
    let active: Bool
    @State private var moving = false

    var body: some View {
        GeometryReader { proxy in
            ZStack(alignment: .leading) {
                Rectangle().fill(QuartetTheme.divider)
                if active {
                    Capsule()
                        .fill(QuartetTheme.running)
                        .frame(width: max(48, proxy.size.width * 0.25))
                        .offset(x: moving ? proxy.size.width : -proxy.size.width * 0.25)
                }
            }
            .clipShape(Capsule())
        }
        .frame(height: 2)
        .onAppear { moving = true }
        .animation(active ? .linear(duration: 1.8).repeatForever(autoreverses: false) : .default, value: moving)
        .accessibilityHidden(true)
    }
}

extension View {
    func quartetSheetStyle() -> some View {
        presentationDragIndicator(.visible)
            .presentationCornerRadius(28)
            .presentationBackground(QuartetTheme.canvas)
    }
}

struct AttachmentSourcePopover: View {
    let onCamera: () -> Void
    let onFile: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            VStack(alignment: .leading, spacing: 4) {
                Text("添加附件")
                    .font(.quartet(.regular, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                Text("选择相机拍摄，或从系统文件中添加图片。")
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .fixedSize(horizontal: false, vertical: true)
            }

            VStack(spacing: 0) {
                actionRow(
                    title: "相机",
                    detail: "拍摄一张新照片",
                    systemImage: "camera.fill",
                    action: onCamera
                )

                Divider()
                    .overlay(QuartetTheme.divider)
                    .padding(.leading, 52)

                actionRow(
                    title: "文件",
                    detail: "支持最大 10MB 的图片",
                    systemImage: "folder.fill",
                    action: onFile
                )
            }
            .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        }
        .padding(16)
        .frame(width: 286)
        .background(QuartetTheme.surface)
        .presentationCompactAdaptation(.popover)
        .presentationBackground(QuartetTheme.surface)
    }

    private func actionRow(
        title: String,
        detail: String,
        systemImage: String,
        action: @escaping () -> Void
    ) -> some View {
        Button(action: action) {
            HStack(spacing: 12) {
                Image(systemName: systemImage)
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.accent)
                    .frame(width: 34, height: 34)
                    .background(QuartetTheme.accent.opacity(0.12), in: Circle())

                VStack(alignment: .leading, spacing: 2) {
                    Text(title)
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                    Text(detail)
                        .font(.quartet(.compact))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }

                Spacer(minLength: 8)

                Image(systemName: "chevron.right")
                    .font(.quartet(.compact, weight: .bold))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
            .padding(.horizontal, 10)
            .frame(minHeight: 58)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(title)
        .accessibilityHint(detail)
    }
}
