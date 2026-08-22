import SwiftUI
import UIKit

enum QuartetTheme {
    static let canvas = dynamic(light: 0xF4F7F8, dark: 0x071014)
    static let surface = dynamic(light: 0xFFFFFF, dark: 0x0D1B20)
    static let elevated = dynamic(light: 0xEAF0F2, dark: 0x14272D)
    static let primaryText = dynamic(light: 0x102127, dark: 0xEAF6F6)
    static let secondaryText = dynamic(light: 0x587078, dark: 0x89A2A9)
    static let divider = dynamic(light: 0xD3DEE1, dark: 0x20363D)
    static let accent = Color(red: 0.10, green: 0.72, blue: 0.66)
    static let running = Color(red: 0.95, green: 0.66, blue: 0.24)
    static let failed = Color(red: 0.94, green: 0.35, blue: 0.32)
    static let stopped = Color(red: 0.54, green: 0.61, blue: 0.64)

    static func statusColor(_ status: String) -> Color {
        switch status.lowercased() {
        case "running", "pending": running
        case "completed": accent
        case "failed": failed
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
