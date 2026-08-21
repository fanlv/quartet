package graph

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fanlv/quartet/pkg/strutil"
	"github.com/fanlv/quartet/types/model"
)

// Job 标题生成的输入组装（供 HTTP 层调用）。
//
// 早期实现把整份 workflow JSON 丢给标题模型，噪音极大：layout 坐标、节点 ID、
// 边、canvas viewport、hook 脚本等结构性字段占了绝大部分 token，而真正表达
// "这个工作流要干什么" 的只有 Prompt 节点的提示词。这里改成先把提示词按执行
// 顺序提炼成一个列表，再交给模型，既省 token 又让标题更贴题。

const (
	// titleSummaryPerNodeRunes 单个提示词保留的最大字符数。提示词开头通常就是
	// 任务目标，后面多是输出格式约束、重试要求等工程细节，对标题无用。
	titleSummaryPerNodeRunes = 400
	// titleSummaryTotalRunes 整个标题输入的字符预算，超出后不再追加节点。
	titleSummaryTotalRunes = 4000
	// titleSummaryMaxPromptNodes 最多列出的提示词节点数。节点再多也无助于概括
	// 整体目标，前几个（按执行顺序）已经足够。
	titleSummaryMaxPromptNodes = 10
	// titleSummaryAuxLineRunes 辅助节点（Shell/条件/循环）单行保留的字符数。
	titleSummaryAuxLineRunes = 80
	// titleSummaryNodeTitleRunes 节点标题保留的字符数。
	titleSummaryNodeTitleRunes = 60
	// titleSummaryMaxAuxNodes 最多列出的辅助节点数。
	titleSummaryMaxAuxNodes = 8
	// titleFallbackSeedRunes 兜底标题种子保留的字符数，交由调用方再做清洗截断。
	titleFallbackSeedRunes = 120
)

// TitleInput 是标题生成所需的两份文本：Summary 交给模型，FallbackSeed 在模型
// 不可用/连续失败时用于派生一个"够用"的标题。二者都可能为空（例如工作流里只有
// start/end 节点），调用方需要自行判空。
type TitleInput struct {
	// Summary 是按执行顺序整理出的提示词列表（含少量结构信息），作为标题模型的
	// 用户消息。
	Summary string
	// FallbackSeed 是第一个提示词节点的开头内容，模型失败时用它派生标题。
	FallbackSeed string
}

// BuildTitleInput 从 workflow 配置中提炼标题生成输入：
//
//   - 只保留承载业务语义的内容：Prompt / Clarify 节点的提示词是主体，Shell、
//     条件、循环节点只留一行摘要（标题或脚本/条件的首行），start/end 忽略；
//   - 节点按执行顺序（拓扑序，循环体紧随其循环节点）排列，让模型能分辨"取数据
//     → 处理 → 通知"这样的流程走向；
//   - 工作流级变量会代入提示词，未定义的（运行时才产生的节点输出）保持
//     {{name}} 字面量；
//   - 单条提示词和整体输入都有字符预算，超出部分截断并显式标注，模型据此知道
//     自己看到的是片段。
func BuildTitleInput(cfg model.GraphConfig) TitleInput {
	disabled := make(map[string]struct{}, len(cfg.DisabledVars))
	for _, k := range cfg.DisabledVars {
		disabled[k] = struct{}{}
	}

	nodes := titleOrderedNodes(cfg)

	var (
		promptLines []string
		auxLines    []string
		fallback    string
		budget      = titleSummaryTotalRunes
		promptCount int
		auxCount    int
		promptOmit  int
		auxOmit     int
	)

	for _, n := range nodes {
		switch {
		case isAgent(n.Type):
			text := strings.TrimSpace(substituteVariables(n.Config.Prompt, cfg.Variables, disabled))
			if text == "" {
				continue
			}
			if fallback == "" {
				fallback = truncateRunes(text, titleFallbackSeedRunes)
			}
			if promptCount >= titleSummaryMaxPromptNodes || budget <= 0 {
				promptOmit++
				continue
			}
			body := truncateRunes(text, min(titleSummaryPerNodeRunes, budget))
			budget -= len([]rune(body))
			promptCount++
			promptLines = append(promptLines, fmt.Sprintf("### %d. %s%s\n%s", promptCount, titleNodeKind(n.Type), titleNodeName(n), body))
		case n.Type == model.GraphNodeTypeShell, n.Type == model.GraphNodeTypeIfElse, n.Type == model.GraphNodeTypeLoop:
			line := titleAuxLine(n, cfg.Variables, disabled)
			if line == "" {
				continue
			}
			if auxCount >= titleSummaryMaxAuxNodes {
				auxOmit++
				continue
			}
			auxCount++
			auxLines = append(auxLines, line)
		}
	}

	if len(promptLines) == 0 && len(auxLines) == 0 {
		return TitleInput{}
	}

	var b strings.Builder
	if len(promptLines) > 0 {
		b.WriteString("## Prompt 节点提示词（按执行顺序）\n\n")
		b.WriteString(strings.Join(promptLines, "\n\n"))
		if promptOmit > 0 {
			fmt.Fprintf(&b, "\n\n（另有 %d 个提示词节点未列出）", promptOmit)
		}
	} else {
		b.WriteString("## 说明\n\n该工作流没有 Prompt 节点，只有以下执行节点。")
	}
	if len(auxLines) > 0 {
		b.WriteString("\n\n## 其他节点（按执行顺序）\n\n")
		b.WriteString(strings.Join(auxLines, "\n"))
		if auxOmit > 0 {
			fmt.Fprintf(&b, "\n（另有 %d 个节点未列出）", auxOmit)
		}
	}

	if fallback == "" {
		// 没有任何提示词时，用第一条辅助节点摘要做兜底种子。
		fallback = strings.TrimSpace(strings.TrimPrefix(auxLines[0], "- "))
	}

	return TitleInput{Summary: b.String(), FallbackSeed: fallback}
}

// titleNodeKind 给出节点类型的中文标签，帮助模型区分"模型执行"和"人工澄清"。
func titleNodeKind(t model.GraphNodeType) string {
	switch t {
	case model.GraphNodeTypePrompt:
		return "Prompt 节点"
	case model.GraphNodeTypeClarify:
		return "澄清节点"
	case model.GraphNodeTypeShell:
		return "Shell 节点"
	case model.GraphNodeTypeIfElse:
		return "条件判断节点"
	case model.GraphNodeTypeLoop:
		return "循环节点"
	default:
		return string(t)
	}
}

// titleNodeName 返回节点标题的展示后缀（无标题时为空）。节点标题是用户手写的，
// 往往比提示词更直白，所以放在提示词前面。
func titleNodeName(n model.GraphNode) string {
	title := strings.TrimSpace(n.Title)
	if title == "" {
		return ""
	}
	return "「" + truncateRunes(title, titleSummaryNodeTitleRunes) + "」"
}

// titleAuxLine 把非提示词节点压缩成一行：优先用用户手写的节点标题，没有标题时
// 退化为脚本/条件的首行，避免把整段脚本塞进标题输入。
func titleAuxLine(n model.GraphNode, vars map[string]string, disabled map[string]struct{}) string {
	detail := ""
	switch n.Type {
	case model.GraphNodeTypeShell:
		detail = firstMeaningfulLine(substituteVariables(n.Config.Script, vars, disabled))
	case model.GraphNodeTypeIfElse:
		detail = firstMeaningfulLine(n.Config.Condition)
	case model.GraphNodeTypeLoop:
		switch n.Config.LoopMode {
		case model.GraphLoopModeFixed:
			if n.Config.FixedCount > 0 {
				detail = fmt.Sprintf("固定循环 %d 次", n.Config.FixedCount)
			}
		default:
			detail = firstMeaningfulLine(n.Config.UntilCondition)
		}
	}
	name := titleNodeName(n)
	detail = truncateRunes(detail, titleSummaryAuxLineRunes)
	switch {
	case name == "" && detail == "":
		return ""
	case detail == "":
		return "- " + titleNodeKind(n.Type) + name
	case name == "":
		return "- " + titleNodeKind(n.Type) + "：" + detail
	default:
		return "- " + titleNodeKind(n.Type) + name + "：" + detail
	}
}

// firstMeaningfulLine 取文本第一行有内容的非注释行，用于给脚本/条件生成摘要。
func firstMeaningfulLine(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") || line == "set -e" {
			continue
		}
		return line
	}
	return ""
}

// truncateRunes 截断到 max 个字符，并在截断时显式标注，让标题模型知道自己只看到
// 了片段（提示词后半段通常是格式约束，缺失不影响概括）。
func truncateRunes(text string, max int) string {
	if max <= 0 {
		return ""
	}
	out := strutil.TruncateRunes(text, max)
	if out == text {
		return text
	}
	return strings.TrimRight(out, " \t\n") + " …（已截断）"
}

// titleOrderedNodes 把配置里的节点排成执行顺序：每个作用域（主图 + 每个循环体）
// 内做拓扑排序，循环体节点紧跟在它的循环节点之后。存在环或数据异常时退化为配置
// 顺序，保证所有节点都会出现且函数不会漏节点。
func titleOrderedNodes(cfg model.GraphConfig) []model.GraphNode {
	byScope := make(map[string][]model.GraphNode, 2)
	order := make(map[string]int, len(cfg.Nodes))
	for i, n := range cfg.Nodes {
		byScope[n.ParentID] = append(byScope[n.ParentID], n)
		order[n.ID] = i
	}

	out := make([]model.GraphNode, 0, len(cfg.Nodes))
	visited := make(map[string]bool, len(byScope))
	var walk func(scope string)
	walk = func(scope string) {
		if visited[scope] {
			return
		}
		visited[scope] = true
		for _, n := range titleTopoSort(byScope[scope], cfg.Edges, order) {
			out = append(out, n)
			if n.Type == model.GraphNodeTypeLoop {
				walk(n.ID)
			}
		}
	}
	walk("")

	// 兜底：ParentID 指向不存在/已删除节点的孤立作用域，按配置顺序补在末尾。
	emitted := make(map[string]bool, len(out))
	for _, n := range out {
		emitted[n.ID] = true
	}
	for _, n := range cfg.Nodes {
		if !emitted[n.ID] {
			out = append(out, n)
		}
	}
	return out
}

// titleTopoSort 对同一作用域内的节点做 Kahn 拓扑排序，就绪集合按配置顺序取最小
// 下标以保证结果稳定。成环的节点（正常校验会拒绝，但历史数据可能存在）按配置顺
// 序追加在后面。
func titleTopoSort(nodes []model.GraphNode, edges []model.GraphEdge, order map[string]int) []model.GraphNode {
	if len(nodes) <= 1 {
		return nodes
	}
	inScope := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		inScope[n.ID] = true
	}
	inDeg := make(map[string]int, len(nodes))
	outEdges := make(map[string][]string, len(nodes))
	for _, e := range edges {
		if !inScope[e.SourceNodeID] || !inScope[e.TargetNodeID] || e.SourceNodeID == e.TargetNodeID {
			continue
		}
		outEdges[e.SourceNodeID] = append(outEdges[e.SourceNodeID], e.TargetNodeID)
		inDeg[e.TargetNodeID]++
	}

	byID := make(map[string]model.GraphNode, len(nodes))
	var ready []string
	for _, n := range nodes {
		byID[n.ID] = n
		if inDeg[n.ID] == 0 {
			ready = append(ready, n.ID)
		}
	}
	sortByConfigOrder(ready, order)

	sorted := make([]model.GraphNode, 0, len(nodes))
	done := make(map[string]bool, len(nodes))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		sorted = append(sorted, byID[id])
		done[id] = true
		var unlocked []string
		for _, dst := range outEdges[id] {
			inDeg[dst]--
			if inDeg[dst] == 0 {
				unlocked = append(unlocked, dst)
			}
		}
		if len(unlocked) > 0 {
			ready = append(ready, unlocked...)
			sortByConfigOrder(ready, order)
		}
	}
	for _, n := range nodes {
		if !done[n.ID] {
			sorted = append(sorted, n)
		}
	}
	return sorted
}

func sortByConfigOrder(ids []string, order map[string]int) {
	sort.SliceStable(ids, func(i, j int) bool { return order[ids[i]] < order[ids[j]] })
}
