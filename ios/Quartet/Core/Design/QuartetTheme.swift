import SwiftUI
import UIKit

enum QuartetTheme {
    // Neutral graphite surfaces keep the interface calm; phosphor mint carries the brand signal.
    static let canvas = dynamic(light: 0xF2F5F2, dark: 0x060B09)
    static let surface = dynamic(light: 0xFBFDFB, dark: 0x0C1512)
    static let elevated = dynamic(light: 0xE7EDE9, dark: 0x14231E)
    static let primaryText = dynamic(light: 0x101815, dark: 0xECF8F3)
    static let secondaryText = dynamic(light: 0x5C6B65, dark: 0x8EA49B)
    static let divider = dynamic(light: 0xD2DDD7, dark: 0x233A32)

    static let accent = dynamic(light: 0x007C5E, dark: 0x42E6B1)
    static let onAccent = dynamic(light: 0xFFFFFF, dark: 0x04110D)
    static let success = dynamic(light: 0x5F7C16, dark: 0xB4D64B)
    static let running = dynamic(light: 0x9A5600, dark: 0xF2B84B)
    static let failed = dynamic(light: 0xB83A44, dark: 0xFF747D)
    static let stopped = dynamic(light: 0x5E6B66, dark: 0x8B9B95)

    // Restrained secondary hues are reserved for charts and mode identification.
    static let chartLime = success
    static let chartViolet = dynamic(light: 0x745D91, dark: 0xB49BD3)
    static let chartRose = dynamic(light: 0xA64F69, dark: 0xE58AA2)
    static let chartSlate = dynamic(light: 0x526D64, dark: 0x87A49A)

    static func statusColor(_ status: String) -> Color {
        switch status.lowercased() {
        case "running": accent
        case "pending", "awaitinginput", "stepstopping": running
        case "completed": success
        case "failed", "timedout": failed
        default: stopped
        }
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
                    .fill(index == 2 ? QuartetTheme.accent : QuartetTheme.secondaryText.opacity(0.45))
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
                        .fill(QuartetTheme.accent)
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
