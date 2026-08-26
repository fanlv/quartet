package handler

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/safe"
	graphsvc "github.com/fanlv/quartet/services/graph"
	"github.com/fanlv/quartet/types/model"
)

const titleCreatePrompt = `你是一个标题生成器。根据用户输入的消息内容，生成一个简洁、准确的标题（title）。

## 规则

1. **长度**：标题控制在 5-20 个字符之间（中文）或 3-10 个单词之间（英文）。
2. **语言**：标题语言与用户输入的主要语言保持一致。
3. **准确性**：标题必须精准概括消息的核心意图或主题，不得引入消息中未提及的内容。
4. **简洁性**：去除冗余修饰词，优先使用名词短语或动宾结构。
5. **可读性**：标题应自然流畅，像人类手动拟定的标题。
6. **格式**：
   - 不使用标点符号（引号、句号、感叹号等）。
   - 不使用 "关于"、"请问"、"帮我" 等口语化前缀。
   - 不输出任何解释，仅输出标题本身。
   - 输出格式是纯文本，不要 markdown 格式，也不要其他任何标点符号。

## 策略

- 如果消息是一个**提问**，提取核心问题作为标题。
- 如果消息是一个**任务指令**，提取任务目标作为标题。
- 如果消息是一段**描述或讨论**，提取核心主题作为标题。
- 如果消息包含**代码或技术内容**，提取技术主题和动作作为标题。
- 如果消息**过短或语义模糊**（如 "你好"、"嗯"），直接使用原文或输出 "闲聊"。
用户的内容如下（帮我把把下面内容生成一个标题，只要标题内容，不要输出其他东西，我会把你输出的内容做标题）：
`

// titleCreateFromGraphPrompt 用于 Graph Workflow 运行生成 Job 标题。输入不再是
// 整份 workflow JSON（结构字段噪音大、token 贵），而是由 graph 服务按执行顺序
// 提炼出的 Prompt 节点提示词列表 + 其他节点的一行摘要。
const titleCreateFromGraphPrompt = `你是一个标题生成器。下面会给你一个 Graph Workflow（图工作流）中各节点的提示词列表（按执行顺序），请据此生成一个简洁、准确的标题（title），用于概括这个工作流要完成的事情。

## 规则

1. **长度**：标题控制在 5-20 个字符之间（中文）或 3-10 个单词之间（英文）。
2. **语言**：标题语言与提示词的主要语言保持一致。
3. **准确性**：标题必须概括整个工作流的核心目标，不得引入提示词中未提及的内容。
4. **简洁性**：去除冗余修饰词，优先使用动宾结构或名词短语。
5. **可读性**：标题应自然流畅，像人类手动拟定的标题。
6. **格式**：
   - 不使用标点符号（引号、句号、感叹号等）。
   - 不使用 "关于"、"请问"、"帮我"、"工作流" 等口语化或空洞的前缀。
   - 不输出任何解释，仅输出标题本身。
   - 输出格式是纯文本，不要 markdown 格式，也不要其他任何标点符号。

## 策略

- 提示词按执行顺序给出：靠前的通常是取数据、准备素材，靠后的通常是加工、产出、通知。标题要落在**整体最终产出的那件事**上，而不是某一个中间步骤。
- 只有一个提示词时，等价于给这一条指令起标题。
- 提示词里的 {{变量}} 是运行时占位符，按占位符理解即可，不要把变量名写进标题。
- 忽略提示词中与目标无关的通用工程约束（输出格式要求、字数要求、语气要求、重试与异常处理、工具使用说明等）。
- 标注了"已截断"的提示词，只根据可见部分概括。
- "其他节点" 里的 Shell / 条件 / 循环只是辅助信息（比如推送渠道、循环次数），可用于判断工作流的用途，但不要让它主导标题；只有在完全没有 Prompt 节点时，才依据它们概括。
- 如果全部内容过短或语义模糊，输出 "工作流运行"。

下面是这个工作流的节点提示词（帮我根据它生成一个标题，只要标题内容，不要输出其他东西，我会把你输出的内容做标题）：
`

// titleGenerationTimeout bounds a single title-generation attempt. Title
// generation is a short-text task (< 50 tokens output) so 360s is generous
// enough for any provider;
// 模型有时候会执行很慢，所以要 360，不然可能会一直超时。
const titleGenerationTimeout = 360 * time.Second

// titleCircuitBreakerThreshold is the number of consecutive failures across
// all jobs before title generation is temporarily disabled. This prevents
// wasting resources when the underlying provider is unreachable.
const titleCircuitBreakerThreshold = 3

// titleCircuitBreakerCooldown is the duration after which a tripped circuit
// breaker enters half-open state and allows a single probe request through.
const titleCircuitBreakerCooldown = 5 * time.Minute

// asyncUpdateJobTitle schedules title generation for an interactive Job using the
// default title prompt, falling back to the raw message when generation fails.
func (h *Handler) asyncUpdateJobTitle(ctx context.Context, jobID string, userMessage string) {
	h.asyncUpdateJobTitleWith(ctx, jobID, userMessage, titleCreatePrompt, fallbackTitleFromMessage(userMessage))
}

// asyncUpdateGraphJobTitle schedules title generation for a Graph Workflow Job.
// The "message" sent to the LLM is the distilled prompt list built by
// graphsvc.BuildTitleInput (no longer the raw workflow JSON), and the fallback
// is derived from the first Prompt node's opening text.
func (h *Handler) asyncUpdateGraphJobTitle(ctx context.Context, jobID string, in graphsvc.TitleInput) {
	h.asyncUpdateJobTitleWith(ctx, jobID, in.Summary, titleCreateFromGraphPrompt, fallbackTitleFromMessage(in.FallbackSeed))
}

// asyncUpdateJobTitleWith runs title generation in the background using the
// given system prompt. fallbackTitle is applied when every attempt fails; pass
// "" to skip the fallback (i.e. leave the default title in place).
func (h *Handler) asyncUpdateJobTitleWith(ctx context.Context, jobID string, userMessage string, systemPrompt string, fallbackTitle string) {
	if userMessage == "" {
		return
	}

	// Circuit breaker: skip if recent attempts have consistently failed,
	// unless the cooldown period has elapsed (half-open state allows one probe).
	isProbe := false
	if failCount := h.titleFailCount.Load(); failCount >= titleCircuitBreakerThreshold {
		openSince := h.titleOpenSince.Load()
		if openSince > 0 && time.Since(time.Unix(openSince, 0)) < titleCircuitBreakerCooldown {
			logger.Warnf(ctx, "[title] circuit open: skipping title generation, jobId=%s consecutiveFailures=%d openSince=%ds_ago",
				jobID, failCount, time.Now().Unix()-openSince)
			return
		}
		// Cooldown elapsed → enter half-open state. CAS ensures only ONE
		// goroutine probes; others are rejected until the probe completes.
		if !h.titleProbing.CompareAndSwap(false, true) {
			logger.Infof(ctx, "[title] circuit half-open: another probe already in-flight, skipping jobId=%s", jobID)
			return
		}
		isProbe = true
		logger.Infof(ctx, "[title] circuit half-open: allowing probe request, jobId=%s consecutiveFailures=%d openFor=%ds",
			jobID, failCount, time.Now().Unix()-openSince)
	}

	// Dedup: prevent concurrent title generation goroutines for the same job.
	if _, loaded := h.titleInFlight.LoadOrStore(jobID, struct{}{}); loaded {
		if isProbe {
			h.titleProbing.Store(false) // release probe slot since we won't actually run
		}
		logger.Debugf(ctx, "[title] skipped: already in-flight, jobId=%s", jobID)
		return
	}

	logger.Debugf(ctx, "[title] schedule: jobId=%s msgLen=%d isProbe=%v", jobID, len(userMessage), isProbe)

	safe.Go(ctx, func() {
		defer h.titleInFlight.Delete(jobID)
		if isProbe {
			defer h.titleProbing.Store(false)
		}

		const maxRetries = 2

		// Detach from the per-request ctx but still honour shutdown:
		// h.rootCtx is cancelled by the process-level signal handler so
		// retry loops stop instead of lingering past server shutdown.
		ctx := h.rootCtx
		if ctx == nil {
			ctx = context.Background()
		}

		logger.Debugf(ctx, "[title] goroutine entered: jobId=%s rootCtxNil=%v rootCtxErr=%v", jobID, h.rootCtx == nil, ctx.Err())

		// Log resolved agent config once at start so operators can correlate
		// timeout/failure logs with the active title agent configuration.
		settings, settingsErr := h.settingsService.GetSettings()
		if settingsErr != nil {
			logger.Errorf(ctx, "[title] get settings failed in goroutine: jobId=%s err=%v", jobID, settingsErr)
			return
		}
		if settings.TitleGenerationAgent != nil {
			logger.Debugf(ctx, "[title] using agent: jobId=%s agentId=%s timeout=%s maxRetries=%d",
				jobID, settings.TitleGenerationAgent.AgentID, titleGenerationTimeout, maxRetries)
		} else {
			logger.Infof(ctx, "[title] no title agent configured, skipping: jobId=%s", jobID)
			return
		}

		startAll := time.Now()
		var lastErr error
		const totalAttempts = maxRetries + 1
		for attempt := 1; attempt <= totalAttempts; attempt++ {
			if attempt > 1 {
				logger.Infof(ctx, "[title] backoff before retry: jobId=%s attempt=%d/%d", jobID, attempt, totalAttempts)
				delay := time.Duration(1+rand.Intn(5)) * time.Second
				select {
				case <-ctx.Done():
					logger.Warnf(ctx, "[title] cancelled during backoff: jobId=%s attempt=%d/%d err=%v", jobID, attempt, totalAttempts, ctx.Err())
					return
				case <-time.After(delay):
				}
			}

			if err := ctx.Err(); err != nil {
				logger.Warnf(ctx, "[title] ctx cancelled before attempt: jobId=%s attempt=%d/%d err=%v", jobID, attempt, totalAttempts, err)
				return
			}

			logger.Debugf(ctx, "[title] calling doUpdateJobTitle: jobId=%s attempt=%d/%d userMsgLen=%d", jobID, attempt, totalAttempts, len(userMessage))
			lastErr = h.doUpdateJobTitle(ctx, jobID, userMessage, systemPrompt)
			if lastErr == nil {
				h.titleFailCount.Store(0) // reset circuit breaker on success
				h.titleOpenSince.Store(0) // close circuit
				logger.Infof(ctx, "[title] success: jobId=%s attempt=%d/%d elapsed=%s", jobID, attempt, totalAttempts, time.Since(startAll).Round(time.Millisecond))
				return
			}
			logger.Warnf(ctx, "[title] attempt failed: jobId=%s attempt=%d/%d err=%v", jobID, attempt, totalAttempts, lastErr)
		}

		newFail := h.titleFailCount.Add(1)
		if newFail >= titleCircuitBreakerThreshold {
			h.titleOpenSince.Store(time.Now().Unix()) // reset cooldown timer so circuit stays open
		}
		logger.Errorf(ctx, "[title] retries exhausted: jobId=%s attempts=%d totalElapsed=%s consecutiveFailures=%d lastErr=%v",
			jobID, totalAttempts, time.Since(startAll).Round(time.Second), newFail, lastErr)
		if lastErr != nil {
			if err := h.recordTitleGenerationError(jobID, lastErr.Error()); err != nil {
				logger.Errorf(ctx, "[title] persist final error failed: jobId=%s err=%v", jobID, err)
			}
		}

		// Fallback so the user doesn't end up with an empty title.
		if ctx.Err() != nil {
			logger.Infof(ctx, "[title] ctx done after retries, skipping fallback: jobId=%s", jobID)
			return
		}
		if fallbackTitle == "" {
			logger.Infof(ctx, "[title] no fallback available, giving up: jobId=%s", jobID)
			return
		}
		if err := h.applyJobTitle(jobID, fallbackTitle); err != nil {
			logger.Warnf(ctx, "[title] apply fallback failed: jobId=%s err=%v", jobID, err)
			return
		}
		logger.Infof(ctx, "[title] applied fallback after %d failed attempts: jobId=%s title=%q", totalAttempts, jobID, fallbackTitle)
	})
}

func (h *Handler) doUpdateJobTitle(ctx context.Context, jobID string, userMessage string, systemPrompt string) error {
	logger.Debugf(ctx, "[title] doUpdateJobTitle enter: jobId=%s", jobID)

	settings, err := h.settingsService.GetSettings()
	if err != nil {
		return fmt.Errorf("get settings failed: %w", err)
	}

	titleAgent := settings.TitleGenerationAgent
	if titleAgent == nil || titleAgent.AgentID == "" {
		logger.Infof(ctx, "[title] no title agent configured, returning nil: jobId=%s", jobID)
		return nil
	}

	logger.Debugf(ctx, "[title] creating timeout ctx: jobId=%s agentId=%s timeout=%s", jobID, titleAgent.AgentID, titleGenerationTimeout)

	ctx, cancel := context.WithTimeout(ctx, titleGenerationTimeout)
	defer cancel()

	logger.Debugf(ctx, "[title] calling generateText: jobId=%s agentId=%s", jobID, titleAgent.AgentID)
	start := time.Now()
	title, err := h.generateText(ctx, titleAgent.AgentID, titleAgent.ModelID, titleAgent.ACPThoughtLevel, []*schema.Message{
		schema.SystemMessage(systemPrompt),
		schema.UserMessage(userMessage),
	})
	if err != nil {
		return fmt.Errorf("generate title failed (agentId=%s elapsed=%s): %w", titleAgent.AgentID, time.Since(start).Round(time.Millisecond), err)
	}

	logger.Infof(ctx, "[title] generated: jobId=%s title=%q", jobID, title)

	if err := h.applyJobTitle(jobID, title); err != nil {
		logger.Warnf(ctx, "[title] apply job title failed: jobId=%s err=%v", jobID, err)
		return err
	}
	if err := h.jobService.UpdateTitleGenerationError(jobID, ""); err != nil {
		logger.Warnf(ctx, "[title] clear persisted error failed: jobId=%s err=%v", jobID, err)
	} else {
		h.jobService.Publish(jobID, &model.CustomEvent{
			BaseEvent: model.BaseEvent{
				Type:      model.EventTypeCustom,
				JobID:     jobID,
				Timestamp: time.Now().UnixMilli(),
			},
			Name:  "job_title_generation_error_cleared",
			Value: map[string]any{},
		})
	}

	logger.Infof(ctx, "[title] applied: jobId=%s title=%q", jobID, title)
	return nil
}

func (h *Handler) recordTitleGenerationError(jobID, message string) error {
	if err := h.jobService.UpdateTitleGenerationError(jobID, message); err != nil {
		return err
	}
	h.jobService.Publish(jobID, &model.CustomEvent{
		BaseEvent: model.BaseEvent{
			Type:      model.EventTypeCustom,
			JobID:     jobID,
			Timestamp: time.Now().UnixMilli(),
		},
		Name: "job_title_generation_failed",
		Value: map[string]any{
			"error": message,
		},
	})
	return nil
}

// applyJobTitle persists the title on the job and notifies SSE subscribers.
// Shared between the LLM-generated path and the fallback path so both stay
// in sync (in particular: both must publish so the frontend updates).
func (h *Handler) applyJobTitle(jobID, title string) error {
	if err := h.jobService.UpdateTitle(jobID, title); err != nil {
		return fmt.Errorf("save job title failed: %w", err)
	}

	h.jobService.Publish(jobID, &model.CustomEvent{
		BaseEvent: model.BaseEvent{
			Type:      model.EventTypeCustom,
			JobID:     jobID,
			Timestamp: time.Now().UnixMilli(),
		},
		Name: "job_title_updated",
		Value: map[string]any{
			"title": title,
		},
	})
	return nil
}

// titlePunctuation is the set of leading/trailing punctuation trimmed from a
// fallback title so it matches the "no punctuation" rule in titleCreatePrompt.
// Covers both the ASCII and the common CJK fullwidth marks (： ， 。 ！ ？ …)
// that show up at the end of a user's first line.
const titlePunctuation = " \t：:，,。.！!？?；;、…~～-—\"'“”‘’《》「」（）()[]【】"

// fallbackTitleFromMessage produces a "good enough" title from the raw user
// message when LLM-generated title generation fails: first non-empty line,
// markdown header/code-fence noise stripped, leading/trailing punctuation
// trimmed to honour the title format rule, truncated to a display-friendly
// length. Returns "" when nothing usable remains so the caller can skip.
func fallbackTitleFromMessage(msg string) string {
	const maxRunes = 30
	for line := range strings.SplitSeq(msg, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "```") {
			continue
		}
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
		// Trim surrounding punctuation BEFORE truncation so a title ending in
		// e.g. "：" doesn't keep the mark, and so truncation budget isn't spent
		// on leading punctuation.
		line = strings.Trim(line, titlePunctuation)
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) > maxRunes {
			line = string(runes[:maxRunes])
			// Truncation can expose a new trailing mark (e.g. cut mid-clause
			// right after a comma); trim once more so the result stays clean.
			line = strings.Trim(line, titlePunctuation)
		}
		return line
	}
	return ""
}
