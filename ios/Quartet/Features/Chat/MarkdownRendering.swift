import SwiftUI
import UIKit

struct MarkdownMessageView: View {
    let text: String
    var tone: MarkdownTone = .standard

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            ForEach(MarkdownRenderer.blocks(from: text)) { block in
                switch block.kind {
                case .markdown(let content):
                    MarkdownTextBlock(text: content, tone: tone)
                case .code(let language, let content):
                    CodeBlockView(language: language, code: content, tone: tone)
                case .table(let headers, let rows):
                    MarkdownTableView(headers: headers, rows: rows, tone: tone)
                case .heading(let level, let content):
                    MarkdownTextBlock(text: content, tone: tone)
                        .font(headingFont(level))
                        .fontWeight(.bold)
                case .quote(let content):
                    HStack(alignment: .top, spacing: 10) {
                        Capsule()
                            .fill(tone == .user ? QuartetTheme.onAccent.opacity(0.42) : QuartetTheme.secondaryText.opacity(0.5))
                            .frame(width: 3)
                        MarkdownTextBlock(text: content, tone: tone)
                    }
                    .padding(.vertical, 4)
                    .padding(.horizontal, 10)
                    .background(tone.codeBackground.opacity(0.7), in: RoundedRectangle(cornerRadius: 7))
                case .list(let ordered, let items):
                    VStack(alignment: .leading, spacing: 7) {
                        ForEach(Array(items.enumerated()), id: \.offset) { index, item in
                            HStack(alignment: .firstTextBaseline, spacing: 8) {
                                Text(ordered ? "\(index + 1)." : "•")
                                    .font(.quartet(tone.contentFontSize, weight: .semibold))
                                    .foregroundStyle(tone.secondaryForeground)
                                    .frame(minWidth: ordered ? 20 : 10, alignment: .trailing)
                                MarkdownTextBlock(text: item, tone: tone)
                            }
                        }
                    }
                case .divider:
                    Divider().overlay(tone.codeBorder)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func headingFont(_ level: Int) -> Font {
        switch level {
        case 1, 2: .quartet(.large)
        case 3: .quartet(.regular)
        default: .quartet(.control)
        }
    }
}

struct MarkdownTextBlock: View {
    let text: String
    let tone: MarkdownTone

    var body: some View {
        if let attributed = MarkdownRenderer.attributedString(from: text) {
            Text(attributed)
                .font(.quartet(tone.contentFontSize))
                .foregroundStyle(tone.foreground)
                .lineSpacing(4)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        } else {
            Text(text)
                .font(.quartet(tone.contentFontSize))
                .foregroundStyle(tone.foreground)
                .lineSpacing(4)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

struct CodeBlockView: View {
    let language: String?
    let code: String
    let tone: MarkdownTone

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text((language?.isEmpty == false ? language! : "code").uppercased())
                    .font(.quartet(.compact, weight: .bold, design: .monospaced))
                    .foregroundStyle(codeSecondaryForeground)
                Spacer()
                Button("复制代码") {
                    UIPasteboard.general.string = code
                }
                .font(.quartet(.detail))
            }

            ScrollView(.horizontal, showsIndicators: false) {
                Text(code)
                    .font(.quartet(.detail, design: .monospaced))
                    .foregroundStyle(codeForeground)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding(12)
        .background(codeBackground, in: RoundedRectangle(cornerRadius: 10))
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(codeBorder, lineWidth: 1))
    }

    private var codeForeground: Color {
        tone == .user ? tone.foreground : QuartetTheme.terminalText
    }

    private var codeSecondaryForeground: Color {
        tone == .user ? tone.secondaryForeground : QuartetTheme.terminalGreenMuted
    }

    private var codeBackground: Color {
        tone == .user ? tone.codeBackground : QuartetTheme.terminalBackground
    }

    private var codeBorder: Color {
        tone == .user ? tone.codeBorder : QuartetTheme.terminalBorder
    }
}

enum MarkdownTone: Equatable {
    case standard
    case user
    case thought
    case tool

    var contentFontSize: QuartetFontSize {
        switch self {
        case .standard, .thought: .control
        case .user, .tool: .regular
        }
    }

    var foreground: Color {
        switch self {
        case .user: QuartetTheme.onAccent
        case .thought: QuartetTheme.primaryText.opacity(0.82)
        case .standard, .tool: QuartetTheme.primaryText
        }
    }

    var secondaryForeground: Color {
        self == .user ? QuartetTheme.onAccent.opacity(0.68) : QuartetTheme.secondaryText
    }

    var codeBackground: Color {
        switch self {
        case .user: QuartetTheme.onAccent.opacity(0.08)
        case .thought: QuartetTheme.accent.opacity(0.07)
        case .standard, .tool: QuartetTheme.elevated.opacity(0.72)
        }
    }

    var codeBorder: Color {
        self == .user ? QuartetTheme.onAccent.opacity(0.16) : QuartetTheme.divider
    }
}

struct MarkdownTableView: View {
    let headers: [String]
    let rows: [[String]]
    let tone: MarkdownTone

    var body: some View {
        ScrollView(.horizontal, showsIndicators: true) {
            Grid(horizontalSpacing: 0, verticalSpacing: 0) {
                GridRow {
                    ForEach(Array(headers.enumerated()), id: \.offset) { _, value in
                        MarkdownTextBlock(text: value, tone: tone)
                            .fontWeight(.semibold)
                            .padding(.horizontal, 10)
                            .padding(.vertical, 8)
                            .background(tone.codeBackground)
                    }
                }
                ForEach(Array(rows.enumerated()), id: \.offset) { _, row in
                    GridRow {
                        ForEach(0..<headers.count, id: \.self) { index in
                            MarkdownTextBlock(text: index < row.count ? row[index] : "", tone: tone)
                                .padding(.horizontal, 10)
                                .padding(.vertical, 8)
                        }
                    }
                }
            }
        }
        .background(tone.codeBackground.opacity(0.45), in: RoundedRectangle(cornerRadius: 9))
        .overlay(RoundedRectangle(cornerRadius: 9).stroke(tone.codeBorder, lineWidth: 1))
    }
}

enum MarkdownRenderer {
    struct Block: Identifiable {
        enum Kind {
            case markdown(String)
            case code(language: String?, content: String)
            case table(headers: [String], rows: [[String]])
            case heading(level: Int, content: String)
            case quote(String)
            case list(ordered: Bool, items: [String])
            case divider
        }

        let id: Int
        let kind: Kind
    }

    static func blocks(from text: String) -> [Block] {
        var kinds: [Block.Kind] = []
        var remaining = text[...]

        while let opening = remaining.range(of: "```") {
            let leading = String(remaining[..<opening.lowerBound])
            if !leading.isEmpty {
                appendMarkdownAndTables(leading, to: &kinds)
            }
            remaining = remaining[opening.upperBound...]

            let headerEnd = remaining.firstIndex(of: "\n") ?? remaining.endIndex
            let language = String(remaining[..<headerEnd]).trimmingCharacters(in: .whitespacesAndNewlines)
            if headerEnd < remaining.endIndex {
                remaining = remaining[remaining.index(after: headerEnd)...]
            } else {
                remaining = remaining[headerEnd...]
            }

            guard let closing = remaining.range(of: "```") else {
                // An unfinished fence is common while SSE is still streaming.
                // Render the available payload as code immediately instead of
                // flashing raw backticks until the closing fence arrives.
                kinds.append(.code(language: language.isEmpty ? nil : language, content: String(remaining)))
                remaining = "".suffix(0)
                break
            }

            let code = String(remaining[..<closing.lowerBound]).trimmingCharacters(in: .newlines)
            kinds.append(.code(language: language.isEmpty ? nil : language, content: code))
            remaining = remaining[closing.upperBound...]
        }

        if !remaining.isEmpty {
            appendMarkdownAndTables(String(remaining), to: &kinds)
        }
        if kinds.isEmpty { appendMarkdownAndTables(text, to: &kinds) }
        return kinds.enumerated().map { Block(id: $0.offset, kind: $0.element) }
    }

    private static func appendMarkdownAndTables(_ text: String, to result: inout [Block.Kind]) {
        let lines = text.components(separatedBy: "\n")
        var markdownLines: [String] = []
        var index = 0

        func flushMarkdown() {
            guard !markdownLines.isEmpty else { return }
            result.append(.markdown(markdownLines.joined(separator: "\n")))
            markdownLines.removeAll(keepingCapacity: true)
        }

        while index < lines.count {
            if index + 1 < lines.count,
               isTableRow(lines[index]),
               isTableDivider(lines[index + 1]) {
                flushMarkdown()
                let headers = tableCells(lines[index])
                index += 2
                var rows: [[String]] = []
                while index < lines.count, isTableRow(lines[index]), !lines[index].trimmingCharacters(in: .whitespaces).isEmpty {
                    rows.append(tableCells(lines[index]))
                    index += 1
                }
                result.append(.table(headers: headers, rows: rows))
                continue
            }

            let trimmed = lines[index].trimmingCharacters(in: .whitespaces)
            if let heading = heading(in: trimmed) {
                flushMarkdown()
                result.append(.heading(level: heading.level, content: heading.content))
                index += 1
                continue
            }
            if isDivider(trimmed) {
                flushMarkdown()
                result.append(.divider)
                index += 1
                continue
            }
            if trimmed.hasPrefix("> ") || trimmed == ">" {
                flushMarkdown()
                var quoted: [String] = []
                while index < lines.count {
                    let candidate = lines[index].trimmingCharacters(in: .whitespaces)
                    guard candidate.hasPrefix(">") else { break }
                    quoted.append(String(candidate.dropFirst()).trimmingCharacters(in: .whitespaces))
                    index += 1
                }
                result.append(.quote(quoted.joined(separator: "\n")))
                continue
            }
            if let firstItem = listItem(in: trimmed) {
                flushMarkdown()
                var items = [firstItem.content]
                let ordered = firstItem.ordered
                index += 1
                while index < lines.count, let next = listItem(in: lines[index].trimmingCharacters(in: .whitespaces)), next.ordered == ordered {
                    items.append(next.content)
                    index += 1
                }
                result.append(.list(ordered: ordered, items: items))
                continue
            }
            markdownLines.append(lines[index])
            index += 1
        }
        flushMarkdown()
    }

    private static func isTableRow(_ line: String) -> Bool {
        tableCells(line).count > 1
    }

    private static func isTableDivider(_ line: String) -> Bool {
        let cells = tableCells(line)
        guard cells.count > 1 else { return false }
        return cells.allSatisfy { cell in
            let core = cell.trimmingCharacters(in: CharacterSet(charactersIn: " :-"))
            return core.isEmpty && cell.filter { $0 == "-" }.count >= 3
        }
    }

    private static func heading(in line: String) -> (level: Int, content: String)? {
        let markerCount = line.prefix { $0 == "#" }.count
        guard (1...6).contains(markerCount) else { return nil }
        let boundary = line.index(line.startIndex, offsetBy: markerCount)
        guard boundary < line.endIndex, line[boundary] == " " else { return nil }
        return (markerCount, String(line[line.index(after: boundary)...]))
    }

    private static func isDivider(_ line: String) -> Bool {
        let compact = line.filter { !$0.isWhitespace }
        guard compact.count >= 3, let first = compact.first, ["-", "*", "_"].contains(first) else { return false }
        return compact.allSatisfy { $0 == first }
    }

    private static func listItem(in line: String) -> (ordered: Bool, content: String)? {
        if line.hasPrefix("- ") || line.hasPrefix("* ") || line.hasPrefix("+ ") {
            return (false, String(line.dropFirst(2)))
        }
        guard let dot = line.firstIndex(of: "."), dot < line.endIndex else { return nil }
        let number = line[..<dot]
        let afterDot = line.index(after: dot)
        guard !number.isEmpty, number.allSatisfy(\.isNumber), afterDot < line.endIndex, line[afterDot] == " " else { return nil }
        return (true, String(line[line.index(after: afterDot)...]))
    }

    private static func tableCells(_ line: String) -> [String] {
        var value = line.trimmingCharacters(in: .whitespaces)
        if value.hasPrefix("|") { value.removeFirst() }
        if value.hasSuffix("|") { value.removeLast() }
        return value.split(separator: "|", omittingEmptySubsequences: false)
            .map { $0.trimmingCharacters(in: .whitespaces) }
    }

    static func attributedString(from text: String) -> AttributedString? {
        do {
            return try AttributedString(
                markdown: text,
                options: AttributedString.MarkdownParsingOptions(interpretedSyntax: .full)
            )
        } catch {
            return nil
        }
    }
}
