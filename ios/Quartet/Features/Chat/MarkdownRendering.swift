import SwiftUI
import UIKit

/// Markdown 块拆分、`AttributedString(markdown:)` 与 JSON 美化都是完整解析，成本远高于
/// 一次视图求值。而 SwiftUI 会在任何一次状态变更后重新求值 body，聊天时间线里已完成的
/// 消息文本却永不改变 —— 所以按源文本记忆化，把重复解析压成一次。
///
/// 流式输出中的那一条消息每个 delta 都是新文本，必然 miss；但同一时刻只有一条，
/// 其余历史消息全部命中。
@MainActor
enum ChatTextCache {
    private final class Entry<Value>: NSObject {
        let value: Value
        init(_ value: Value) { self.value = value }
    }

    private static let blockCache: NSCache<NSString, Entry<[MarkdownRenderer.Block]>> = {
        let cache = NSCache<NSString, Entry<[MarkdownRenderer.Block]>>()
        cache.countLimit = 256
        return cache
    }()

    private static let attributedCache: NSCache<NSString, Entry<AttributedString?>> = {
        let cache = NSCache<NSString, Entry<AttributedString?>>()
        cache.countLimit = 512
        return cache
    }()

    private static let jsonCache: NSCache<NSString, Entry<String?>> = {
        let cache = NSCache<NSString, Entry<String?>>()
        cache.countLimit = 128
        return cache
    }()

    static func blocks(from text: String) -> [MarkdownRenderer.Block] {
        cached(key: text, in: blockCache) { MarkdownRenderer.blocks(from: text) }
    }

    /// 行内代码和加粗的字体随 role/tone 变化，所以缓存键要带上样式标记。字号档也必须进键：
    /// 这些 run 上落的是按动态字号算出的固定磅值，系统字号改了而键不变，旧 run 会一直用老磅值。
    static func attributedString(from text: String, role: MarkdownTextRole, tone: MarkdownTone) -> AttributedString? {
        let sizeCategory = UITraitCollection.current.preferredContentSizeCategory.rawValue
        return cached(key: "\(role.rawValue)|\(tone.cacheToken)|\(sizeCategory)\u{1}\(text)", in: attributedCache) {
            MarkdownRenderer.attributedString(from: text, role: role, tone: tone)
        }
    }

    static func prettyPrintedJSON(from text: String) -> String? {
        // Module-qualified: unqualified lookup resolves to this very method, not the global builder.
        cached(key: text, in: jsonCache) { Quartet.prettyPrintedJSON(text) }
    }

    private static func cached<Value>(
        key: String,
        in cache: NSCache<NSString, Entry<Value>>,
        build: () -> Value
    ) -> Value {
        let key = key as NSString
        if let hit = cache.object(forKey: key) { return hit.value }
        let value = build()
        cache.setObject(Entry(value), forKey: key)
        return value
    }
}

/// 段落的排版角色。标题层级必须在这里落到 `Text` 上 —— 外层 `.font()` 会被
/// `Text` 自身的 `.font()` 覆盖，这是此前 `#`/`##` 全部渲染成正文的原因。
enum MarkdownTextRole: String {
    case body
    case headingLarge
    case headingMedium
    case headingSmall
    case tableHeader

    func font(for tone: MarkdownTone) -> Font {
        switch self {
        case .body: .chat(tone.contentFontSize)
        case .headingLarge: .chat(.large, weight: .bold)
        case .headingMedium: .chat(.headline, weight: .semibold)
        // 正文已经是 `.reading`，标题再用更小的档就会比正文还小，这里只靠字重区分。
        case .headingSmall: .chat(tone.contentFontSize, weight: .semibold)
        case .tableHeader: .chat(tone.contentFontSize, weight: .semibold)
        }
    }

    /// 行内 `code` 用等宽体，但字号跟随所在段落，避免标题里的代码突然缩小。
    func codeFont(for tone: MarkdownTone) -> Font {
        switch self {
        case .body: .chat(tone.contentFontSize, design: .monospaced)
        case .headingLarge: .chat(.large, weight: .bold, design: .monospaced)
        case .headingMedium: .chat(.headline, weight: .semibold, design: .monospaced)
        case .headingSmall, .tableHeader: .chat(tone.contentFontSize, weight: .semibold, design: .monospaced)
        }
    }

    /// `**加粗**` 的字重必须在这里显式落到字体上，见 `applyStrongEmphasisStyle`。
    func strongFont(for tone: MarkdownTone) -> Font {
        switch self {
        case .body, .headingSmall, .tableHeader: .chat(tone.contentFontSize, weight: .bold)
        case .headingLarge: .chat(.large, weight: .bold)
        case .headingMedium: .chat(.headline, weight: .bold)
        }
    }

    /// 标题的段前间距，让长回复形成视觉分组。
    var topSpacing: CGFloat {
        switch self {
        case .headingLarge: 10
        case .headingMedium: 6
        case .headingSmall: 3
        case .body, .tableHeader: 0
        }
    }

    static func heading(level: Int) -> Self {
        switch level {
        case 1, 2: .headingLarge
        case 3: .headingMedium
        default: .headingSmall
        }
    }
}

struct MarkdownMessageView: View {
    let text: String
    var tone: MarkdownTone = .standard

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            ForEach(ChatTextCache.blocks(from: text)) { block in
                switch block.kind {
                case .markdown(let content):
                    MarkdownTextBlock(text: content, tone: tone)
                case .code(let language, let content):
                    CodeBlockView(language: language, code: content, tone: tone)
                case .table(let headers, let rows):
                    MarkdownTableView(headers: headers, rows: rows, tone: tone)
                case .heading(let level, let content):
                    let role = MarkdownTextRole.heading(level: level)
                    MarkdownTextBlock(text: content, tone: tone, role: role)
                        .padding(.top, role.topSpacing)
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
                        ForEach(Array(items.enumerated()), id: \.offset) { _, item in
                            HStack(alignment: .firstTextBaseline, spacing: 8) {
                                Text(ordered ? "\(item.ordinal)." : Self.bullet(level: item.level))
                                    .font(.chat(tone.contentFontSize, weight: .semibold))
                                    .foregroundStyle(tone.secondaryForeground)
                                    .frame(minWidth: ordered ? 20 : 10, alignment: .trailing)
                                MarkdownTextBlock(text: item.content, tone: tone)
                            }
                            .padding(.leading, CGFloat(item.level) * 16)
                        }
                    }
                case .divider:
                    Divider().overlay(tone.codeBorder)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private static func bullet(level: Int) -> String {
        switch level {
        case 0: "•"
        case 1: "◦"
        default: "▪"
        }
    }
}

struct MarkdownTextBlock: View {
    let text: String
    let tone: MarkdownTone
    var role: MarkdownTextRole = .body

    /// 含汉字的行自然行高约 1.40em、纯拉丁行只有约 1.18em（苹方与 SF Pro 的 asc+desc
    /// 实测值），补上这一档行距后中文段落落在 1.7em 左右 —— 手机上读长回复的舒适区间。
    /// `@ScaledMetric` 让行距跟着动态字号一起长，不然放大字号后行会黏在一起。
    @ScaledMetric(relativeTo: .body) private var lineSpacing: CGFloat = 5

    var body: some View {
        content
            .font(role.font(for: tone))
            .foregroundStyle(tone.foreground)
            .lineSpacing(lineSpacing)
            .textSelection(.enabled)
            .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var content: Text {
        if let attributed = ChatTextCache.attributedString(from: text, role: role, tone: tone) {
            Text(attributed)
        } else {
            Text(text)
        }
    }
}

struct CodeBlockView: View {
    let language: String?
    let code: String
    let tone: MarkdownTone

    /// 等宽字重的行距比正文小一档：代码靠缩进读结构，行距太大反而拆散了块。
    @ScaledMetric(relativeTo: .footnote) private var lineSpacing: CGFloat = 3

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text((language?.isEmpty == false ? language! : "code").uppercased())
                    .font(.chat(.compact, weight: .bold, design: .monospaced))
                    .foregroundStyle(codeSecondaryForeground)
                Spacer()
                Button("复制代码") {
                    UIPasteboard.general.string = code
                }
                .font(.chat(.detail))
            }

            // 软换行而不是内嵌横向 ScrollView：手机上长行更易读，同时避免在竖向
            // 滚动列表里塞进几十个 UIScrollView 与外层争抢手势。
            Text(code)
                .font(.chat(.detail, design: .monospaced))
                .foregroundStyle(codeForeground)
                .lineSpacing(lineSpacing)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
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
        // 四种气泡共用一个正文档：此前 Agent 回复用 `.control`、用户气泡用 `.regular`，
        // 同一屏里中文一大一小，混排的字号节奏先垮在这一步。
        .reading
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

    /// 行内 `code` 的淡底色，沿用绿色主题。
    var inlineCodeBackground: Color {
        switch self {
        case .user: QuartetTheme.onAccent.opacity(0.18)
        case .thought: QuartetTheme.accent.opacity(0.13)
        case .standard, .tool: QuartetTheme.accent.opacity(0.11)
        }
    }

    /// 用户气泡是绿底，行内代码保持 `onAccent` 才读得清；其余用深绿区分。
    var inlineCodeForeground: Color? {
        self == .user ? nil : QuartetTheme.accentDeep
    }

    /// 链接色必须显式写到 run 上。SwiftUI 用环境 tint 渲染 `.link`，而全局 tint 正好是
    /// accent 绿、用户气泡底色也是 accent 绿 —— 不覆盖就是绿字压绿底，看着像透明。
    /// 绿底上没有第二个既够对比又不刺眼的颜色，所以链接跟气泡正文同色，靠下划线区分。
    var linkForeground: Color {
        switch self {
        case .user: QuartetTheme.onAccent
        case .standard, .thought, .tool: QuartetTheme.accentDeep
        }
    }

    var cacheToken: String {
        switch self {
        case .standard: "s"
        case .user: "u"
        case .thought: "t"
        case .tool: "o"
        }
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
                        MarkdownTextBlock(text: value, tone: tone, role: .tableHeader)
                            .padding(.horizontal, 10)
                            .padding(.vertical, 8)
                            .background(tone.codeBackground)
                    }
                }
                ForEach(Array(rows.enumerated()), id: \.offset) { rowIndex, row in
                    GridRow {
                        ForEach(0..<headers.count, id: \.self) { index in
                            MarkdownTextBlock(text: index < row.count ? row[index] : "", tone: tone)
                                .padding(.horizontal, 10)
                                .padding(.vertical, 8)
                                // 斑马纹，长表格里更容易横向对齐读行。
                                .background(rowIndex.isMultiple(of: 2) ? Color.clear : tone.codeBackground.opacity(0.5))
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
        /// 列表项带上缩进层级与该层级下的序号，都在解析期算好，渲染时不再推导。
        struct ListItem {
            let level: Int
            let ordinal: Int
            let content: String
        }

        enum Kind {
            case markdown(String)
            case code(language: String?, content: String)
            case table(headers: [String], rows: [[String]])
            case heading(level: Int, content: String)
            case quote(String)
            case list(ordered: Bool, items: [ListItem])
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
                let ordered = firstItem.ordered
                // 缩进宽度必须在 trim 之前量，否则嵌套层级信息就丢了。
                var indents = [indentWidth(of: lines[index])]
                var contents = [firstItem.content]
                index += 1
                while index < lines.count,
                      let next = listItem(in: lines[index].trimmingCharacters(in: .whitespaces)),
                      next.ordered == ordered {
                    indents.append(indentWidth(of: lines[index]))
                    contents.append(next.content)
                    index += 1
                }
                result.append(.list(ordered: ordered, items: listItems(indents: indents, contents: contents)))
                continue
            }
            markdownLines.append(lines[index])
            index += 1
        }
        flushMarkdown()
    }

    /// 把出现过的缩进宽度按大小排名映射成层级，这样 2 空格和 4 空格两种写法都能正确分层。
    /// 有序列表的序号按层级各自计数，进入更深一层时从 1 重新开始。
    private static func listItems(indents: [Int], contents: [String]) -> [Block.ListItem] {
        let ranking = Dictionary(
            uniqueKeysWithValues: Set(indents).sorted().enumerated().map { ($1, min($0, 3)) }
        )
        var counters: [Int] = []
        var items: [Block.ListItem] = []
        items.reserveCapacity(contents.count)

        for (offset, content) in contents.enumerated() {
            let level = ranking[indents[offset]] ?? 0
            if level >= counters.count {
                counters.append(contentsOf: Array(repeating: 0, count: level - counters.count + 1))
            } else {
                counters.removeSubrange((level + 1)...)
            }
            counters[level] += 1
            items.append(Block.ListItem(level: level, ordinal: counters[level], content: content))
        }
        return items
    }

    private static func indentWidth(of line: String) -> Int {
        var width = 0
        for character in line {
            if character == " " {
                width += 1
            } else if character == "\t" {
                width += 4
            } else {
                break
            }
        }
        return width
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

    static func attributedString(from text: String, role: MarkdownTextRole, tone: MarkdownTone) -> AttributedString? {
        guard var attributed = try? AttributedString(
            markdown: text,
            options: AttributedString.MarkdownParsingOptions(interpretedSyntax: .full)
        ) else {
            return nil
        }
        ChatLinkTarget.decorateFileLinks(in: &attributed)
        applyStrongEmphasisStyle(to: &attributed, role: role, tone: tone)
        applyInlineCodeStyle(to: &attributed, role: role, tone: tone)
        applyLinkStyle(to: &attributed, tone: tone)
        return attributed
    }

    /// `**加粗**` 的字重必须自己接管。SwiftUI 是靠给字体加 bold 符号特征来实现强调的，
    /// 而追加符号特征会连带丢掉字体描述符上的 cascade list —— 汉字随即退回系统回退，
    /// 拿到比苹方 Semibold 更重的字面，同一句里中文的加粗比英文重出一档。
    /// 这里直接换成目标字重的字体，并摘掉 strong 标记，避免 SwiftUI 再加一次特征。
    private static func applyStrongEmphasisStyle(
        to attributed: inout AttributedString,
        role: MarkdownTextRole,
        tone: MarkdownTone
    ) {
        // 同样先收集再改写：边遍历 runs 边改属性会让迭代器失效。
        let strong = attributed.runs.compactMap { run -> (Range<AttributedString.Index>, InlinePresentationIntent)? in
            guard let intent = run.inlinePresentationIntent,
                  intent.contains(.stronglyEmphasized) else { return nil }
            return (run.range, intent)
        }
        guard !strong.isEmpty else { return }

        let font = role.strongFont(for: tone)
        for (range, intent) in strong {
            attributed[range].font = font
            // 只摘 strong 位：斜体和行内代码的语义还要留给后面的处理和 SwiftUI。
            let rest = intent.subtracting(.stronglyEmphasized)
            attributed[range].inlinePresentationIntent = rest.isEmpty ? nil : rest
        }
    }

    /// 行内 `code` 此前和正文没有任何视觉差别。run 上的 `font` 优先级高于 `Text.font()`，
    /// 正好用来给这些片段单独换等宽体加底色。
    private static func applyInlineCodeStyle(
        to attributed: inout AttributedString,
        role: MarkdownTextRole,
        tone: MarkdownTone
    ) {
        // 先收集范围再改写：边遍历 runs 边改属性会让迭代器失效。
        let ranges = attributed.runs.compactMap { run -> Range<AttributedString.Index>? in
            guard let intent = run.inlinePresentationIntent, intent.contains(.code) else { return nil }
            return run.range
        }
        guard !ranges.isEmpty else { return }

        let font = role.codeFont(for: tone)
        let background = tone.inlineCodeBackground
        let foreground = tone.inlineCodeForeground
        for range in ranges {
            attributed[range].font = font
            attributed[range].backgroundColor = background
            if let foreground {
                attributed[range].foregroundColor = foreground
            }
        }
    }

    /// 链接的显式配色 + 下划线。run 上的 `foregroundColor` 优先级高于 SwiftUI 给 `.link`
    /// 套的环境 tint，这是把绿底气泡里“隐形”的链接抢回来的唯一入口；下划线负责在颜色
    /// 跟正文一致时仍然保留“可点”的暗示。在 inline code 之后跑，让链接色赢下重叠片段。
    private static func applyLinkStyle(to attributed: inout AttributedString, tone: MarkdownTone) {
        // 同样先收集范围：边遍历 runs 边改属性会让迭代器失效。
        let ranges = attributed.runs.compactMap { run -> Range<AttributedString.Index>? in
            run.link == nil ? nil : run.range
        }
        guard !ranges.isEmpty else { return }

        let foreground = tone.linkForeground
        for range in ranges {
            attributed[range].foregroundColor = foreground
            attributed[range].underlineStyle = Text.LineStyle.single
        }
    }
}
