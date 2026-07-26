# Feature 文档规范

本目录用于保存 feature/PRD 文档。文档只描述功能、目标、边界和验收方式，不写代码实现细节；在不丢失关键约束的前提下保持简洁。

## 工作流程

1. 收到 feature 描述后，先判断信息是否足够。
2. 只有关键上下文不明确时，提出 3-5 个必要澄清问题；问题使用编号，选项使用 `A/B/C/D`，方便用户回复 `1A, 2C`。
3. 根据用户回答生成结构化 PRD。
4. 保存为 `feature-YYYY-MM-DD-[feature-name].md`，文件名使用 kebab-case。
5. 只生成文档，不直接开始实现。

澄清问题优先覆盖：

- 问题/目标：要解决什么问题。
- 核心功能：用户能做哪些关键动作。
- 范围/边界：明确不做什么。
- 验收标准：怎样算完成。

## 推荐结构

### 1. Introduction/Overview

简要说明 feature、背景和要解决的问题。

### 2. Goals

列出具体、可衡量的目标。

### 3. User Stories

每个 user story 应足够小，可以独立实现和验收。

```markdown
### US-001: [Title]

**Description:** As a [user], I want [feature] so that [benefit].

**Acceptance Criteria:**
- [ ] 具体且可验证的验收项
- [ ] Typecheck/lint passes
- [ ] UI 变更需在浏览器中验证
```

验收标准必须可验证，避免使用"正常工作"、"体验良好"这类模糊描述。涉及 UI 的 story 必须包含浏览器验证项。

### 4. Functional Requirements

使用编号列出明确的功能要求，例如：

- FR-1: 系统必须允许用户执行某个动作。
- FR-2: 当用户点击某个入口时，系统必须展示某个结果。

### 5. Non-Goals (Out of Scope)

明确本 feature 不包含哪些内容，防止范围扩张。

### 6. Design Considerations

可选。记录 UI/UX 要求、可复用组件、交互限制或相关设计稿。

### 7. Technical Considerations

可选。记录已知约束、依赖关系、集成点、性能要求或兼容边界；保持功能层描述，不展开代码细节。

### 8. Success Metrics

说明成功如何衡量，例如完成时间、错误率、转化率、回归通过范围等。

### 9. Open Questions

记录仍需确认的问题。

## 写作要求

- 面向初级开发者或 AI agent，表达要明确、可执行。
- 避免术语堆叠；必要术语需要解释。
- 需求、验收项和功能要求都要可追踪、可验证。
- 多文件方案可以使用同名子目录 `feature-YYYY-MM-DD-[feature-name]/`，子文件用 `00-overview.md`、`01-architecture.md` 等编号排序。

## 保存前检查

- [ ] 已提出必要澄清问题，并吸收用户回答。
- [ ] User stories 小而清晰。
- [ ] Acceptance Criteria 具体可验证。
- [ ] Functional Requirements 编号明确。
- [ ] Non-Goals 定义清楚。
- [ ] 文件保存到 `docs/feature/`，命名符合 `feature-YYYY-MM-DD-[feature-name].md`。
