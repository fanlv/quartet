package personalchat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	pkgacp "github.com/fanlv/quartet/pkg/acp"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
	"github.com/google/uuid"
)

type Options struct {
	Root           context.Context
	ResolveHarness func(context.Context, string) (model.AgentRuntimeBinding, error)
	DefaultHarness func(context.Context) (string, error)
	Run            func(context.Context, *model.PersonalChatSession, *model.PersonalChatRun, []*schema.Message, agui.EventHandler) error
	Summarize      func(context.Context, string) (string, error)
	Deliver        func(context.Context, model.PersonalChatRun, model.PersonalChatMessage) error
	Finished       func(context.Context, model.PersonalChatTask, model.PersonalChatSession, model.PersonalChatRun, model.PersonalChatMessage, bool)
}

type Service struct {
	mu           sync.Mutex
	repo         *repository.PersonalChatRepo
	opts         Options
	epoch        string
	seq          uint64
	agents       map[string]model.PersonalAgent
	tasks        map[string]model.PersonalChatTask
	sessions     map[string]model.PersonalChatSession
	runs         map[string]model.PersonalChatRun
	messages     map[string]model.PersonalChatMessage
	receipts     map[string]string
	settings     model.PersonalChatSettings
	warnings     []string
	changed      chan struct{}
	active       map[string]context.CancelFunc
	summarySlots chan struct{}
	wg           sync.WaitGroup
	closed       bool
	fatal        error
	toolTokens   map[string]string
	viewers      map[string]taskViewer
}

func New(opts Options) (*Service, error) {
	if opts.Root == nil {
		return nil, fmt.Errorf("personal chat root context is required")
	}
	repo, commits, warnings, err := repository.NewPersonalChatRepo()
	if err != nil {
		return nil, err
	}
	s := &Service{epoch: uuid.NewString(), repo: repo, opts: opts, agents: map[string]model.PersonalAgent{}, tasks: map[string]model.PersonalChatTask{}, sessions: map[string]model.PersonalChatSession{}, runs: map[string]model.PersonalChatRun{}, messages: map[string]model.PersonalChatMessage{}, receipts: map[string]string{}, settings: model.PersonalChatSettings{IdleMinutes: 30, MaxConcurrent: 8}, warnings: warnings, changed: make(chan struct{}), active: map[string]context.CancelFunc{}, summarySlots: make(chan struct{}, 1)}
	for _, c := range commits {
		s.applyLocked(c)
	}
	if len(s.agents) == 0 {
		err = s.commitLocked(model.PersonalChatCommit{Agents: []model.PersonalAgent{{ID: "main", Name: "主 Agent", Main: true, Revision: 1, SystemPrompt: "You are Quartet's personal assistant. Help the user directly, preserve their confirmed decisions, search relevant past sessions when needed, and delegate independent work to configured business agents using quartet-cli chat delegate. Acknowledge delegated tasks without waiting for them synchronously."}}, Settings: &s.settings})
		if err != nil {
			repo.Close()
			return nil, err
		}
	}
	var recovery model.PersonalChatCommit
	for _, run := range s.runs {
		if run.DeliveryState == "sending" {
			run.DeliveryState = "unknown"
			run.DeliveryError = "Backend restarted during IM delivery; delivery may have succeeded. Review the source chat before retrying."
			recovery.Runs = append(recovery.Runs, run)
		}

		if run.State != "running" && run.State != "stopping" {
			continue
		}
		run.State = "interrupted"
		run.Error = "Execution interrupted: backend restarted while the harness was running; external tool actions were not replayed."
		run.FinishedAt = time.Now().UnixMilli()
		recovery.Runs = append(recovery.Runs, run)
		m := s.messages[run.ReplyID]
		m.State = run.State
		m.Error = run.Error
		input := s.messages[run.MessageID]
		input.State = run.State
		recovery.Messages = append(recovery.Messages, m, input)
		t := s.tasks[run.TaskID]
		t.Paused = true
		t.PriorityRunID = ""
		t.Queue = without(t.Queue, run.ID)
		t.IdleSince = time.Now().UnixMilli()
		recovery.Tasks = append(recovery.Tasks, t)
	}
	if len(recovery.Runs) > 0 {
		if err = s.commitLocked(recovery); err != nil {
			repo.Close()
			return nil, err
		}
	}
	return s, nil
}

// Start resumes only accepted, unstarted work after handler dependencies are ready.
func (s *Service) Start() {
	s.kick()
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.recoverOutputs() }()
}

func (s *Service) applyLocked(c model.PersonalChatCommit) {
	s.seq = c.Seq
	for _, v := range c.Agents {
		s.agents[v.ID] = v
	}
	for _, v := range c.Tasks {
		s.tasks[v.ID] = v
	}
	for _, v := range c.Sessions {
		s.sessions[v.ID] = v
	}
	for _, v := range c.Runs {
		s.runs[v.ID] = v
		s.receipts[v.MessageID] = v.ID
	}
	for _, v := range c.Messages {
		s.messages[v.ID] = v
	}
	if c.Settings != nil {
		s.settings = *c.Settings
	}
}

func (s *Service) commitLocked(c model.PersonalChatCommit) error {
	if s.fatal != nil {
		return s.fatal
	}
	c.Version = 1
	c.Seq = s.seq + 1
	for i := range c.Messages {
		c.Messages[i].Revision = c.Seq
		c.Messages[i].UpdatedAt = time.Now().UnixMilli()
	}
	if err := s.repo.Append(c); err != nil {
		s.fatal = err
		s.warnings = append(s.warnings, err.Error())
		close(s.changed)
		s.changed = make(chan struct{})
		return err
	}
	s.applyLocked(c)
	close(s.changed)
	s.changed = make(chan struct{})
	return nil
}

func (s *Service) Changes() (uint64, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq, s.changed
}

func (s *Service) SaveAgent(a model.PersonalAgent) (model.PersonalAgent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" {
		return a, fmt.Errorf("business Agent name is required")
	}
	if a.ID == "" {
		a.ID = "agent-" + uuid.NewString()
	}
	old, exists := s.agents[a.ID]
	if !exists && a.ID == "main" {
		return a, fmt.Errorf("main Agent already belongs to the system")
	}
	a.Main = a.ID == "main"
	if exists && a.Revision != old.Revision {
		return a, fmt.Errorf("Agent %s changed: expected revision %d, current revision %d; reload before saving", a.ID, a.Revision, old.Revision)
	}
	a.Revision = old.Revision + 1
	if a.Main && a.Deleted {
		return a, fmt.Errorf("the default main Agent cannot be deleted")
	}
	if a.Deleted {
		for _, t := range s.tasks {
			if t.AgentID == a.ID && len(t.Queue) > 0 {
				return a, fmt.Errorf("Agent %s still has queued or running tasks", a.ID)
			}
		}
	}
	err := s.commitLocked(model.PersonalChatCommit{Agents: []model.PersonalAgent{a}})
	return a, err
}

func (s *Service) Settings(v *model.PersonalChatSettings) (model.PersonalChatSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v == nil {
		return s.settings, nil
	}
	if v.IdleMinutes < 1 || v.IdleMinutes > 525600 || v.MaxConcurrent < 1 || v.MaxConcurrent > 64 {
		return s.settings, fmt.Errorf("idleMinutes must be 1..525600; maxConcurrent must be 1..64")
	}
	err := s.commitLocked(model.PersonalChatCommit{Settings: v})
	if err == nil {
		go s.kick()
	}
	return s.settings, err
}

func (s *Service) Send(ctx context.Context, req model.PersonalChatSendRequest, userID string) (model.PersonalChatRun, error) {
	if strings.TrimSpace(req.ClientMessageID) == "" || strings.TrimSpace(req.Content) == "" {
		return model.PersonalChatRun{}, fmt.Errorf("clientMessageId and content are required")
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		return model.PersonalChatRun{}, err
	}
	sum := sha256.Sum256(encoded)
	hash := hex.EncodeToString(sum[:])
	s.mu.Lock()
	if s.closed || s.opts.Root.Err() != nil {
		s.mu.Unlock()
		return model.PersonalChatRun{}, fmt.Errorf("personal chat is shutting down")
	}
	if id, ok := s.receipts[req.ClientMessageID]; ok {
		r := s.runs[id]
		s.mu.Unlock()
		if r.Hash != hash {
			return r, fmt.Errorf("clientMessageId %q already belongs to a different payload", req.ClientMessageID)
		}
		return r, nil
	}
	agentID := req.AgentID
	if agentID == "" {
		agentID = "main"
	}
	var task model.PersonalChatTask
	if req.TaskID != "" {
		var ok bool
		task, ok = s.tasks[req.TaskID]
		if !ok {
			s.mu.Unlock()
			return model.PersonalChatRun{}, fmt.Errorf("task %q does not exist", req.TaskID)
		}
		if req.AgentID == "" {
			agentID = task.AgentID
		} else if task.AgentID != agentID {
			task = model.PersonalChatTask{}
		}
	}
	a, ok := s.agents[agentID]
	if !ok || a.Deleted {
		s.mu.Unlock()
		return model.PersonalChatRun{}, fmt.Errorf("business Agent %q is unavailable", agentID)
	}
	if req.ParentRunID != "" {
		parent, exists := s.runs[req.ParentRunID]
		if !exists {
			s.mu.Unlock()
			return model.PersonalChatRun{}, fmt.Errorf("parent execution %s does not exist", req.ParentRunID)
		}
		depth := 0
		for ancestor := s.tasks[parent.TaskID]; ancestor.ParentTaskID != ""; ancestor = s.tasks[ancestor.ParentTaskID] {
			depth++
			if depth >= 8 {
				s.mu.Unlock()
				return model.PersonalChatRun{}, fmt.Errorf("delegation depth exceeds 8")
			}
		}
		count := 0
		for _, child := range s.tasks {
			if child.ParentRunID == parent.ID {
				count++
			}
		}
		if count >= 32 {
			s.mu.Unlock()
			return model.PersonalChatRun{}, fmt.Errorf("execution %s already has 32 delegated tasks", parent.ID)
		}
	}
	now := time.Now().UnixMilli()
	if req.Independent {
		task = model.PersonalChatTask{ID: "task-" + uuid.NewString(), AgentID: agentID, Title: short(req.Content, 64), ParentTaskID: req.ParentTaskID, ParentRunID: req.ParentRunID, CreatedAt: now}
	}
	if task.ID == "" {
		id := "default-" + agentID
		task = s.tasks[id]
		if task.ID == "" {
			task = model.PersonalChatTask{ID: id, AgentID: agentID, Title: a.Name, CreatedAt: now}
		}
	}
	prev, hasPrev := s.sessions[task.SessionID]
	harness := req.HarnessID
	if harness == "" && hasPrev {
		harness = prev.HarnessID
	}
	if harness == "" {
		harness = a.HarnessID
	}
	if harness == "" {
		harness, err = s.opts.DefaultHarness(ctx)
		if err != nil {
			s.mu.Unlock()
			return model.PersonalChatRun{}, err
		}
	}
	binding, err := s.opts.ResolveHarness(ctx, harness)
	if err != nil {
		s.mu.Unlock()
		return model.PersonalChatRun{}, err
	}
	modelID, mode, thought := req.ModelID, req.Mode, req.ThoughtLevel
	if hasPrev && prev.HarnessID == harness {
		if modelID == "" {
			modelID = prev.Meta.ModelID
		}
		if mode == "" {
			mode = prev.Meta.ACPMode
		}
		if thought == "" && modelID == prev.Meta.ModelID {
			thought = prev.Meta.ACPThoughtLevel
		}
	}
	newSession := task.RotateOnNextMessage || !hasPrev || prev.HarnessID != harness || prev.AgentRevision != a.Revision || prev.Meta.AgentRevision != binding.Revision
	if hasPrev && !task.AwaitingInput && len(task.Queue) == 0 && task.IdleSince > 0 && now-task.IdleSince > int64(s.settings.IdleMinutes)*60000 {
		newSession = true
	}
	if hasPrev && (prev.Meta.ModelID != modelID || prev.Meta.ACPMode != mode || prev.Meta.ACPThoughtLevel != thought) {
		newSession = true
	}
	session := prev
	var sessionChanges []model.PersonalChatSession
	if newSession {
		home, e := os.UserHomeDir()
		if e != nil || home == "" {
			s.mu.Unlock()
			return model.PersonalChatRun{}, fmt.Errorf("resolve harness home directory: %w", e)
		}
		meta := *model.NewSession()
		meta.AgentID = binding.AgentID
		meta.AgentRevision = binding.Revision
		meta.AgentRuntimeKey = binding.RuntimeKey
		meta.AgentDefinition = binding.Definition
		meta.Type = binding.RuntimeKey
		meta.Workdir = home
		meta.JobID = task.ID
		meta.ModelID = modelID
		meta.Title = short(hideReminder(req.Content), 60)
		meta.ACPMode = mode
		meta.ACPThoughtLevel = thought
		session = model.PersonalChatSession{ID: meta.ID, TaskID: task.ID, AgentID: agentID, HarnessID: harness, AgentRevision: a.Revision, SystemPrompt: a.SystemPrompt, PreviousID: task.SessionID, Meta: meta, CreatedAt: now}
		task.SessionID = session.ID
		task.RotateOnNextMessage = false
		sessionChanges = append(sessionChanges, session)
	}
	if !req.Internal {
		task.AwaitingInput = false
		task.WaitReason = ""
	}
	r := model.PersonalChatRun{ID: "run-" + uuid.NewString(), MessageID: req.ClientMessageID, ReplyID: "reply-" + uuid.NewString(), TaskID: task.ID, SessionID: session.ID, AgentID: agentID, UserID: userID, Hash: hash, State: "queued", CreatedAt: now}
	m := model.PersonalChatMessage{ID: req.ClientMessageID, Order: (s.seq + 1) * 2, Role: "user", AgentID: agentID, TaskID: task.ID, SessionID: session.ID, RunID: r.ID, Content: req.Content, State: "queued", Internal: req.Internal || req.ParentRunID != "", Source: req.Source, CreatedAt: now}
	var cancel context.CancelFunc
	runChanges := []model.PersonalChatRun{r}
	messageChanges := []model.PersonalChatMessage{m}
	if req.StopAndSend {
		task.Paused = true
		task.PriorityRunID = r.ID
		index := 0
		if len(task.Queue) > 0 {
			head := s.runs[task.Queue[0]]
			if head.State == "running" || head.State == "stopping" {
				index = 1
				cancel = s.active[head.ID]
				head.State = "stopping"
				runChanges = append(runChanges, head)
				for _, mid := range []string{head.MessageID, head.ReplyID} {
					old := s.messages[mid]
					old.State = "stopping"
					messageChanges = append(messageChanges, old)
				}
			}
		}
		queue := append([]string{}, task.Queue[:index]...)
		queue = append(queue, r.ID)
		queue = append(queue, task.Queue[index:]...)
		task.Queue = queue
	} else {
		task.Queue = append(append([]string{}, task.Queue...), r.ID)
	}
	err = s.commitLocked(model.PersonalChatCommit{Tasks: []model.PersonalChatTask{task}, Sessions: sessionChanges, Runs: runChanges, Messages: messageChanges})
	s.mu.Unlock()
	if err != nil {
		return r, err
	}
	if cancel != nil {
		cancel()
	}
	s.kick()
	return r, nil
}

func (s *Service) kick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.fatal != nil || s.opts.Root.Err() != nil {
		return
	}
	ids := make([]string, 0, len(s.tasks))
	for id := range s.tasks {
		ids = append(ids, id)
	}
	sort.SliceStable(ids, func(i, j int) bool {
		order := func(id string) uint64 {
			q := s.tasks[id].Queue
			if len(q) == 0 {
				return ^uint64(0)
			}
			return s.messages[s.runs[q[0]].MessageID].Order
		}
		if order(ids[i]) == order(ids[j]) {
			return ids[i] < ids[j]
		}
		return order(ids[i]) < order(ids[j])
	})
	for _, id := range ids {
		if len(s.active) >= s.settings.MaxConcurrent {
			return
		}
		t := s.tasks[id]
		if len(t.Queue) == 0 {
			continue
		}
		r := s.runs[t.Queue[0]]
		if r.State != "queued" || t.AwaitingInput || (t.Paused && t.PriorityRunID != r.ID) {
			continue
		}
		r.State = "running"
		m := s.messages[r.MessageID]
		m.State = "running"
		sess := s.sessions[r.SessionID]
		var reminder strings.Builder
		fmt.Fprintf(&reminder, "Business Agent: %s (%s). Task: %s. Run: %s. Harness: %s.\n%s\n", s.agents[r.AgentID].Name, r.AgentID, r.TaskID, r.ID, sess.HarnessID, "")
		if sess.Meta.ACPLastSyncedMessageCount == 0 {
			reminder.WriteString(sess.SystemPrompt + "\n")
		}
		reminder.WriteString("If you need user clarification, call quartet-cli chat wait --parent-run " + r.ID + " --content <question> before ending your turn, so an unanswered question keeps its Session.\n")
		reminder.WriteString("History tools: quartet-cli chat sessions; quartet-cli chat search --query <words>; quartet-cli chat history --session <id>. Delegate: quartet-cli chat delegate --parent-run " + r.ID + " --agent <business-agent-id> --content <task>. Use quartet-cli chat agents to discover business Agents and quartet-cli harness list for execution tools. Delegation returns immediately; results return to this task.\n")
		if sess.PreviousID != "" && sess.Meta.ACPLastSyncedMessageCount == 0 {
			previous := s.sessions[sess.PreviousID]
			if previous.Summary != "" {
				reminder.WriteString("Previous Session summary (verify important details with history):\n" + previous.Summary + "\n")
			}
			coverage := uint64(0)
			if through, ok := s.runs[previous.SummaryThrough]; ok {
				coverage = s.messages[through.ReplyID].Order
			}
			var tail []model.PersonalChatMessage
			for _, message := range s.messages {
				run := s.runs[message.RunID]
				if message.SessionID == previous.ID && !message.Internal && s.messages[run.ReplyID].Order > coverage && run.State != "queued" && run.State != "withdrawn" {
					tail = append(tail, message)
				}
			}
			sort.Slice(tail, func(i, j int) bool { return tail[i].Order < tail[j].Order })
			if len(tail) > 6 {
				tail = tail[len(tail)-6:]
			}
			if len(tail) > 0 {
				reminder.WriteString("Recent unsummarized excerpts; full source remains available through history tools:\n")
			}
			for _, message := range tail {
				fmt.Fprintf(&reminder, "%s | %s | %s: %s\n", message.ID, message.Role, message.State, short(hideReminder(message.Content), 1000))
			}
		}
		reminder.WriteString("Recent session directory (more history is available through the tools):\n")
		all := s.sessionListLocked()
		if len(all) > 12 {
			all = all[:12]
		}
		for _, v := range all {
			fmt.Fprintf(&reminder, "%s | %s | %s\n", v.ID, s.agents[v.AgentID].Name, short(v.Summary, 160))
		}
		m.RawContent = "<system-reminder>\n" + reminder.String() + "</system-reminder>\n\n" + m.Content
		reply := model.PersonalChatMessage{ID: r.ReplyID, Order: (s.seq+1)*2 + 1, Role: "assistant", AgentID: r.AgentID, TaskID: r.TaskID, SessionID: r.SessionID, RunID: r.ID, State: "running", Source: m.Source, CreatedAt: time.Now().UnixMilli()}
		if err := s.commitLocked(model.PersonalChatCommit{Runs: []model.PersonalChatRun{r}, Messages: []model.PersonalChatMessage{m, reply}}); err != nil {
			return
		}
		ctx, cancel := context.WithCancel(s.opts.Root)
		s.active[r.ID] = cancel
		s.wg.Add(1)
		go s.execute(ctx, r, sess, m)
	}
}

func (s *Service) execute(ctx context.Context, r model.PersonalChatRun, sess model.PersonalChatSession, input model.PersonalChatMessage) {
	defer s.wg.Done()
	h := &streamHandler{s: s, run: r}
	flushDone, stopFlush := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(flushDone)
		timer := time.NewTicker(250 * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-stopFlush:
				return
			case <-timer.C:
				if err := h.flush(false); err != nil {
					logger.Errorf(ctx, "[personalchat] flush %s: %v", r.ID, err)
					return
				}
			}
		}
	}()
	err := s.opts.Run(ctx, &sess, &r, []*schema.Message{{Role: schema.User, Content: input.RawContent}}, h)
	close(stopFlush)
	<-flushDone
	if flushErr := h.flush(true); flushErr != nil {
		err = errors.Join(err, flushErr)
	}
	h.mu.Lock()
	err = errors.Join(err, h.reported)
	h.mu.Unlock()
	s.mu.Lock()
	r = s.runs[r.ID]
	r.State = "completed"
	if err != nil {
		r.State = "failed"
		r.Error = err.Error()
	}
	if ctx.Err() != nil {
		r.State = "stopped"
		if err != nil {
			r.Error = err.Error()
		}
	}
	r.FinishedAt = time.Now().UnixMilli()
	if input.Source.Platform != "" {
		r.DeliveryState = "pending"
	}
	reply := s.messages[r.ReplyID]
	reply.State = r.State
	reply.Error = r.Error
	input = s.messages[r.MessageID]
	input.State = r.State
	t := s.tasks[r.TaskID]
	t.Queue = without(t.Queue, r.ID)
	if len(t.Queue) == 0 {
		t.IdleSince = r.FinishedAt
	}
	if r.State == "failed" || r.State == "stopped" {
		t.Paused = true
	}
	if errors.Is(err, pkgacp.ErrCancellationUnconfirmed) {
		t.PriorityRunID = ""
		t.Paused = true
	}
	if t.PriorityRunID == r.ID {
		t.PriorityRunID = ""
		t.Paused = len(t.Queue) > 0
	}
	delete(s.active, r.ID)
	persistErr := s.commitLocked(model.PersonalChatCommit{Runs: []model.PersonalChatRun{r}, Tasks: []model.PersonalChatTask{t}, Messages: []model.PersonalChatMessage{reply, input}})
	s.mu.Unlock()
	if persistErr != nil {
		logger.Errorf(ctx, "[personalchat] finalize %s: %v", r.ID, persistErr)
		return
	}
	if s.opts.Finished != nil {
		s.mu.Lock()
		watched := s.taskWatchedLocked(t.ID)
		s.mu.Unlock()
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.opts.Finished(s.opts.Root, t, sess, r, reply, watched) }()
	}
	s.kick()
	s.returnResult(r)
	if s.opts.Deliver != nil && input.Source.Platform != "" {
		s.deliver(r, reply)
	}
	if s.opts.Summarize != nil && r.State == "completed" {
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.summarize(r) }()
	}
}

func (s *Service) deliver(r model.PersonalChatRun, m model.PersonalChatMessage) {
	s.mu.Lock()
	r = s.runs[r.ID]
	if r.Delivered || r.DeliveryState == "sending" {
		s.mu.Unlock()
		return
	}
	r.DeliveryState = "sending"
	err := s.commitLocked(model.PersonalChatCommit{Runs: []model.PersonalChatRun{r}})
	s.mu.Unlock()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.opts.Root, 30*time.Second)
	defer cancel()
	m.Content = hideReminder(m.Content)
	err = s.opts.Deliver(ctx, r, m)
	s.mu.Lock()
	defer s.mu.Unlock()
	r = s.runs[r.ID]
	r.Delivered = err == nil
	if err != nil {
		r.DeliveryState = "failed"
		r.DeliveryError = err.Error()
		var transport net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.As(err, &transport) {
			r.DeliveryState = "unknown"
			r.DeliveryError = "IM delivery outcome is unknown; check the original chat before retrying. " + err.Error()
		}
	} else {
		r.DeliveryState = "delivered"
		r.DeliveryError = ""
	}
	if e := s.commitLocked(model.PersonalChatCommit{Runs: []model.PersonalChatRun{r}}); e != nil {
		logger.Errorf(ctx, "[personalchat] persist delivery: %v", e)
	}
}

func (s *Service) RetryDelivery(runID string) error {
	s.mu.Lock()
	r, ok := s.runs[runID]
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("personal chat is shutting down")
	}
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("execution %s not found", runID)
	}
	m := s.messages[r.ReplyID]
	if m.Source.Platform == "" || s.opts.Deliver == nil {
		s.mu.Unlock()
		return fmt.Errorf("execution has no IM delivery target")
	}
	if r.State == "running" || r.State == "stopping" || r.State == "queued" {
		s.mu.Unlock()
		return fmt.Errorf("execution %s has not finished", runID)
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() { defer s.wg.Done(); s.deliver(r, m) }()
	return nil
}

func (s *Service) returnResult(r model.PersonalChatRun) {
	s.mu.Lock()
	task := s.tasks[r.TaskID]
	reply := s.messages[r.ReplyID]
	parent, ok := s.runs[task.ParentRunID]
	parentMsg := s.messages[parent.MessageID]
	returned := s.runs[r.ID].ResultReturned
	s.mu.Unlock()
	if !ok || returned {
		return
	}
	result := reply.Content
	if r.Error != "" {
		result += "\nExecution error: " + r.Error
	}
	_, err := s.Send(s.opts.Root, model.PersonalChatSendRequest{ClientMessageID: "result-" + r.ID, TaskID: parent.TaskID, Content: fmt.Sprintf("Subtask %s from business Agent %s finished (%s). Result:\n%s", r.TaskID, r.AgentID, r.State, result), Internal: true, Source: parentMsg.Source}, parent.UserID)
	s.mu.Lock()
	defer s.mu.Unlock()
	r = s.runs[r.ID]
	r.ResultReturned = err == nil
	if err != nil {
		r.ResultError = err.Error()
	} else {
		r.ResultError = ""
	}
	if e := s.commitLocked(model.PersonalChatCommit{Runs: []model.PersonalChatRun{r}}); e != nil {
		logger.Errorf(s.opts.Root, "[personalchat] persist result return: %v", e)
	}
}

func (s *Service) recoverOutputs() {
	s.mu.Lock()
	var runs []model.PersonalChatRun
	for _, r := range s.runs {
		if r.State != "running" && r.State != "stopping" && r.State != "queued" && r.State != "withdrawn" {
			runs = append(runs, r)
		}
	}
	s.mu.Unlock()
	for _, r := range runs {
		if s.opts.Root.Err() != nil {
			return
		}
		if r.DeliveryState == "pending" && s.opts.Deliver != nil {
			s.mu.Lock()
			m := s.messages[r.ReplyID]
			s.mu.Unlock()
			s.deliver(r, m)
		}
		s.returnResult(r)
	}
}

func (s *Service) summarize(r model.PersonalChatRun) {
	select {
	case s.summarySlots <- struct{}{}:
	case <-s.opts.Root.Done():
		return
	}
	defer func() { <-s.summarySlots }()
	for {
		s.mu.Lock()
		busy := len(s.active) > 0
		changed := s.changed
		s.mu.Unlock()
		if !busy {
			break
		}
		select {
		case <-changed:
		case <-s.opts.Root.Done():
			return
		}
	}
	s.mu.Lock()
	for _, candidate := range s.runs {
		if candidate.SessionID == r.SessionID && candidate.State == "completed" && s.messages[candidate.ReplyID].Order > s.messages[r.ReplyID].Order {
			r = candidate
		}
	}
	sess := s.sessions[r.SessionID]
	if sess.SummaryAttempt == r.ID && sess.SummaryError != "" {
		s.mu.Unlock()
		return
	}
	if sess.SummaryThrough != "" {
		old := s.runs[sess.SummaryThrough]
		if s.messages[old.ReplyID].Order >= s.messages[r.ReplyID].Order {
			s.mu.Unlock()
			return
		}
	}
	through := uint64(0)
	if old, ok := s.runs[sess.SummaryThrough]; ok {
		through = s.messages[old.ReplyID].Order
	}
	var turns []model.PersonalChatRun
	end := s.messages[r.ReplyID].Order
	for _, candidate := range s.runs {
		reply := s.messages[candidate.ReplyID]
		if candidate.SessionID == sess.ID && candidate.State == "completed" && reply.Order > through && reply.Order <= end {
			turns = append(turns, candidate)
		}
	}
	sort.Slice(turns, func(i, j int) bool { return s.messages[turns[i].ReplyID].Order < s.messages[turns[j].ReplyID].Order })
	var content strings.Builder
	content.WriteString("Previous summary:\n" + sess.Summary)
	for _, turn := range turns {
		input := s.messages[turn.MessageID]
		reply := s.messages[turn.ReplyID]
		speaker := "User"
		if input.Internal {
			speaker = "Internal task context (not a new user instruction)"
		}
		fmt.Fprintf(&content, "\nTurn %s\n%s:\n%s\nAssistant:\n%s", turn.ID, speaker, hideReminder(input.Content), hideReminder(reply.Content))
	}
	text := content.String()
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(s.opts.Root, 2*time.Minute)
	defer cancel()
	summary, err := s.opts.Summarize(ctx, text)
	s.mu.Lock()
	defer s.mu.Unlock()
	sess = s.sessions[r.SessionID]
	if sess.SummaryThrough != "" {
		old := s.runs[sess.SummaryThrough]
		if s.messages[old.ReplyID].Order >= s.messages[r.ReplyID].Order {
			return
		}
	}
	sess.SummaryAttempt = r.ID
	if err != nil {
		sess.SummaryError = err.Error()
	} else {
		sess.Summary = short(summary, 500)
		sess.SummaryThrough = r.ID
		sess.SummaryError = ""
	}
	if e := s.commitLocked(model.PersonalChatCommit{Sessions: []model.PersonalChatSession{sess}}); e != nil {
		logger.Errorf(ctx, "[personalchat] persist summary: %v", e)
	}
}

func (s *Service) Control(taskID, runID, action string) error {
	s.mu.Lock()
	t, ok := s.tasks[taskID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("task %q not found", taskID)
	}
	var cancel context.CancelFunc
	var c model.PersonalChatCommit
	switch action {
	case "resume":
		t.Paused = false
		t.AwaitingInput = false
		t.WaitReason = ""
	case "stop":
		t.Paused = true
		for _, id := range t.Queue {
			if fn := s.active[id]; fn != nil {
				r := s.runs[id]
				r.State = "stopping"
				c.Runs = append(c.Runs, r)
				cancel = fn
				break
			}
		}
	case "withdraw":
		r, exists := s.runs[runID]
		if !exists || r.TaskID != taskID {
			s.mu.Unlock()
			return fmt.Errorf("run %q not found in task", runID)
		}
		if r.State != "queued" {
			s.mu.Unlock()
			return fmt.Errorf("run %s is %s and cannot be withdrawn", runID, r.State)
		}
		r.State = "withdrawn"
		r.FinishedAt = time.Now().UnixMilli()
		m := s.messages[r.MessageID]
		m.State = r.State
		c.Runs = append(c.Runs, r)
		c.Messages = append(c.Messages, m)
		t.Queue = without(t.Queue, r.ID)
		if t.PriorityRunID == r.ID {
			t.PriorityRunID = ""
		}
		if len(t.Queue) == 0 {
			t.IdleSince = time.Now().UnixMilli()
		}
	default:
		s.mu.Unlock()
		return fmt.Errorf("unknown task action %q", action)
	}
	c.Tasks = []model.PersonalChatTask{t}
	err := s.commitLocked(c)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if cancel != nil {
		cancel()
	}
	s.kick()
	return nil
}

type SnapshotOptions struct {
	Since       uint64
	TaskID      string
	WindowStart uint64
}

func (s *Service) Snapshot(before uint64, limit int, options ...SnapshotOptions) model.PersonalChatSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > 200 {
		limit = 80
	}
	home, homeErr := os.UserHomeDir()
	out := model.PersonalChatSnapshot{Epoch: s.epoch, Workdir: home, Seq: s.seq, Settings: s.settings, Agents: []model.PersonalAgent{}, Tasks: []model.PersonalChatTask{}, Sessions: []model.PersonalChatSession{}, Runs: []model.PersonalChatRun{}, Messages: []model.PersonalChatMessage{}, Warnings: append([]string{}, s.warnings...)}
	if homeErr != nil {
		out.Warnings = append(out.Warnings, homeErr.Error())
	}
	for _, v := range s.agents {
		out.Agents = append(out.Agents, v)
	}
	sort.Slice(out.Agents, func(i, j int) bool {
		if out.Agents[i].Main != out.Agents[j].Main {
			return out.Agents[i].Main
		}
		return out.Agents[i].Name < out.Agents[j].Name
	})
	for _, v := range s.tasks {
		v.Queue = append([]string{}, v.Queue...)
		out.Tasks = append(out.Tasks, v)
	}
	sort.Slice(out.Tasks, func(i, j int) bool { return out.Tasks[i].CreatedAt < out.Tasks[j].CreatedAt })
	for _, v := range s.runs {
		if v.HookError != "" {
			out.Warnings = append(out.Warnings, v.ID+": "+v.HookError)
		}
		if v.ResultError != "" {
			out.Warnings = append(out.Warnings, v.ID+": "+v.ResultError)
		}
		out.Runs = append(out.Runs, v)
	}
	sort.Slice(out.Runs, func(i, j int) bool { return out.Runs[i].CreatedAt > out.Runs[j].CreatedAt })
	if len(out.Runs) > 200 {
		recent := out.Runs[:200:200]
		known := map[string]bool{}
		for _, run := range recent {
			known[run.ID] = true
		}
		for _, run := range out.Runs {
			if !known[run.ID] && (run.State == "queued" || run.State == "running" || run.State == "stopping") {
				recent = append(recent, run)
			}
		}
		out.Runs = recent
	}
	all := s.sessionListLocked()
	if len(all) > 100 {
		all = all[:100]
	}
	seenSession := map[string]bool{}
	for _, v := range all {
		seenSession[v.ID] = true
	}
	target := "default-main"
	if len(options) > 0 && options[0].TaskID != "" {
		target = options[0].TaskID
	}
	if task, ok := s.tasks[target]; ok && !seenSession[task.SessionID] {
		if sess, found := s.sessions[task.SessionID]; found {
			all = append(all, sess)
		}
	}
	for _, v := range all {
		v.SystemPrompt = ""
		if v.SummaryError != "" {
			out.Warnings = append(out.Warnings, v.ID+": "+v.SummaryError)
		}
		out.Sessions = append(out.Sessions, v)
	}
	var msgs []model.PersonalChatMessage
	for _, m := range s.messages {
		if m.Internal && !s.settings.Debug {
			continue
		}
		if before > 0 && m.Order >= before {
			continue
		}
		msgs = append(msgs, s.messageViewLocked(m, false))
	}
	sort.Slice(msgs, func(i, j int) bool {
		if msgs[i].Order == msgs[j].Order {
			return msgs[i].ID < msgs[j].ID
		}
		return msgs[i].Order < msgs[j].Order
	})
	out.HasMore = len(msgs) > limit
	if out.HasMore {
		msgs = msgs[len(msgs)-limit:]
	}
	out.Messages = append(out.Messages, msgs...)
	if len(msgs) > 0 {
		out.Before = msgs[0].Order
	}
	if before == 0 {
		known := map[string]bool{}
		for _, m := range out.Messages {
			known[m.ID] = true
		}
		for _, r := range s.runs {
			if r.State == "running" || r.State == "stopping" {
				for _, id := range []string{r.MessageID, r.ReplyID} {
					m, ok := s.messages[id]
					if ok && !known[id] && (!m.Internal || s.settings.Debug) {
						out.Messages = append(out.Messages, s.messageViewLocked(m, false))
						known[id] = true
					}
				}
			}
		}
		sort.Slice(out.Messages, func(i, j int) bool { return out.Messages[i].Order < out.Messages[j].Order })
	}
	if before == 0 && len(options) > 0 && options[0].Since > 0 && options[0].Since <= s.seq {
		known := map[string]bool{}
		for _, m := range out.Messages {
			known[m.ID] = true
		}
		for _, m := range s.messages {
			if (m.Revision > options[0].Since || (s.settings.Debug && m.Internal && options[0].WindowStart > 0 && m.Order >= options[0].WindowStart)) && !known[m.ID] && (!m.Internal || s.settings.Debug) {
				out.Messages = append(out.Messages, s.messageViewLocked(m, false))
			}
		}
		sort.Slice(out.Messages, func(i, j int) bool { return out.Messages[i].Order < out.Messages[j].Order })
	}
	return out
}

func (s *Service) Message(id string) (model.PersonalChatMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.messages[id]
	if !ok {
		return m, fmt.Errorf("message %q not found", id)
	}
	return s.messageViewLocked(m, true), nil
}

func (s *Service) messageViewLocked(m model.PersonalChatMessage, details bool) model.PersonalChatMessage {
	m.Tools = append([]model.PersonalChatTool{}, m.Tools...)
	m.ToolCount = len(m.Tools)
	if !s.settings.Debug {
		m.Content = hideReminder(m.Content)
		m.RawContent = ""
		m.Thought = hideReminder(m.Thought)
		for i := range m.Tools {
			m.Tools[i].Args = hideReminder(m.Tools[i].Args)
			m.Tools[i].Result = hideReminder(m.Tools[i].Result)
		}
	}
	if !details {
		m.Thought = ""
		m.Tools = nil
	}
	return m
}

func (s *Service) Sessions(agentID string, offset, limit int) []model.PersonalChatSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.sessionListLocked()
	var out []model.PersonalChatSession
	for _, v := range all {
		if agentID == "" || v.AgentID == agentID {
			v.SystemPrompt = ""
			out = append(out, v)
		}
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(out) {
		return []model.PersonalChatSession{}
	}
	end := offset + limit
	if end > len(out) {
		end = len(out)
	}
	return out[offset:end]
}

func (s *Service) sessionListLocked() []model.PersonalChatSession {
	out := make([]model.PersonalChatSession, 0, len(s.sessions))
	for _, v := range s.sessions {
		if v.Maintenance {
			continue
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out
}

func (s *Service) Search(query, agentID string, after, before int64, offset, limit int, includeTools ...bool) ([]model.PersonalChatSearchHit, int, error) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil, 0, fmt.Errorf("query is required")
	}
	if offset < 0 {
		offset = 0
	}
	if limit < 1 || limit > 100 {
		limit = 25
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []model.PersonalChatMessage
	matchedSessions := map[string]bool{}
	eligible := func(m model.PersonalChatMessage) bool {
		return !m.Internal && m.SessionID != "" && m.State != "withdrawn" && (agentID == "" || m.AgentID == agentID) && (after <= 0 || m.CreatedAt >= after) && (before <= 0 || m.CreatedAt < before)
	}
	matches := func(text string) bool {
		body := strings.ToLower(hideReminder(text))
		for _, word := range words {
			if !strings.Contains(body, word) {
				return false
			}
		}
		return true
	}
	for _, m := range s.messages {
		if !eligible(m) {
			continue
		}
		if matches(m.Content) {
			all = append(all, m)
			matchedSessions[m.SessionID] = true
			continue
		}
		if len(includeTools) > 0 && includeTools[0] {
			for _, tool := range m.Tools {
				body := tool.Name + "\n" + tool.Args + "\n" + tool.Result
				if matches(body) {
					m.Content = body
					all = append(all, m)
					matchedSessions[m.SessionID] = true
					break
				}
			}
		}
	}
	// Session cards are navigation hints. Bind card-only matches to a real
	// message so callers can read and verify the source conversation.
	for _, session := range s.sessions {
		card := session.Meta.Title + "\n" + session.Summary
		if session.Maintenance || matchedSessions[session.ID] || !matches(card) {
			continue
		}
		anchor, ok := s.messages[s.runs[session.SummaryThrough].ReplyID]
		if !ok || !eligible(anchor) {
			ok = false
			for _, candidate := range s.messages {
				if candidate.SessionID == session.ID && eligible(candidate) && (!ok || candidate.Order < anchor.Order) {
					anchor, ok = candidate, true
				}
			}
		}
		if ok {
			anchor.Content, anchor.Role = card, "summary"
			all = append(all, anchor)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Order > all[j].Order })
	total := len(all)
	out := []model.PersonalChatSearchHit{}
	for i := offset; i < total && i < offset+limit; i++ {
		m := all[i]
		out = append(out, model.PersonalChatSearchHit{SessionID: m.SessionID, MessageID: m.ID, AgentID: m.AgentID, Role: m.Role, Excerpt: excerpt(hideReminder(m.Content), words[0], 500), CreatedAt: m.CreatedAt})
	}
	return out, total, nil
}

func (s *Service) History(sessionID, anchor string, offset, limit int, includeTools ...bool) ([]model.PersonalChatMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}
	all := []model.PersonalChatMessage{}
	for _, m := range s.messages {
		if m.SessionID == sessionID && !m.Internal {
			m.Content = hideReminder(m.Content)
			m.RawContent = ""
			m.Thought = ""
			if len(includeTools) > 0 && includeTools[0] {
				m.Tools = append([]model.PersonalChatTool{}, m.Tools...)
				for i := range m.Tools {
					m.Tools[i].Args = hideReminder(m.Tools[i].Args)
					m.Tools[i].Result = hideReminder(m.Tools[i].Result)
				}
			} else {
				m.Tools = nil
			}
			all = append(all, m)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Order < all[j].Order })
	if limit < 1 || limit > 100 {
		limit = 30
	}
	if offset < 0 {
		offset = 0
	}
	if anchor != "" {
		found := false
		for i, m := range all {
			if m.ID == anchor {
				offset = i - limit/2
				if offset < 0 {
					offset = 0
				}
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("message %q does not belong to session %s", anchor, sessionID)
		}
	}
	if offset >= len(all) {
		return []model.PersonalChatMessage{}, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], nil
}

func (s *Service) Get(sessionID string) (*model.Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.sessions[sessionID]
	if !ok {
		return nil, false
	}
	m := v.Meta
	return &m, true
}
func (s *Service) Touch(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.sessions[id]
	if !ok {
		return fmt.Errorf("session %s not found", id)
	}
	v.Meta.UpdatedAt = time.Now()
	return s.commitLocked(model.PersonalChatCommit{Sessions: []model.PersonalChatSession{v}})
}
func (s *Service) UpdateACPState(id, acpID string, fp repository.MessagesFingerprint) error {
	return s.updateACP(id, acpID, fp)
}
func (s *Service) UpdateACPSyncFingerprint(id string, fp repository.MessagesFingerprint) error {
	return s.updateACP(id, "", fp)
}
func (s *Service) updateACP(id, acpID string, fp repository.MessagesFingerprint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.sessions[id]
	if !ok {
		return fmt.Errorf("session %s not found", id)
	}
	if acpID != "" {
		v.Meta.ACPSessionID = acpID
	}
	v.Meta.ACPLastSyncedMessageCount = fp.Count
	v.Meta.ACPLastSyncedMessageHash = fp.Hash
	return s.commitLocked(model.PersonalChatCommit{Sessions: []model.PersonalChatSession{v}})
}
func (s *Service) ChatContextRepo(id string) (repository.ChatContextRepo, error) {
	return repository.NewPersonalChatContextRepo(id)
}
func (s *Service) Run(id string) (model.PersonalChatRun, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	return r, ok
}
func (s *Service) UpdateMessage(m model.PersonalChatMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m.Tools = append([]model.PersonalChatTool{}, m.Tools...)
	m.State = s.messages[m.ID].State
	return s.commitLocked(model.PersonalChatCommit{Messages: []model.PersonalChatMessage{m}})
}
func (s *Service) addWarning(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.warnings = append(s.warnings, err.Error())
	logger.Errorf(s.opts.Root, "[personalchat] %v", err)
	close(s.changed)
	s.changed = make(chan struct{})
}
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	for _, cancel := range s.active {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return s.repo.Close()
}

var reminderPattern = regexp.MustCompile(`</?system-reminder>`)

func hideReminder(text string) string {
	var out strings.Builder
	depth, cursor := 0, 0
	for _, span := range reminderPattern.FindAllStringIndex(text, -1) {
		if depth == 0 {
			out.WriteString(text[cursor:span[0]])
		}
		if text[span[0]:span[1]] == "<system-reminder>" {
			depth++
		} else if depth > 0 {
			depth--
		}
		cursor = span[1]
	}
	if depth == 0 {
		out.WriteString(text[cursor:])
	}
	result := out.String()
	opening := "<system-reminder>"
	for n := len(opening) - 1; n > 0; n-- {
		if strings.HasSuffix(result, opening[:n]) {
			return result[:len(result)-n]
		}
	}
	return result
}
func short(text string, n int) string {
	r := []rune(text)
	if len(r) > n {
		return string(r[:n])
	}
	return text
}
func excerpt(text, word string, n int) string {
	r := []rune(text)
	idx := strings.Index(strings.ToLower(text), word)
	if idx < 0 {
		return short(text, n)
	}
	start := len([]rune(text[:idx])) - 100
	if start < 0 {
		start = 0
	}
	end := start + n
	if end > len(r) {
		end = len(r)
	}
	return string(r[start:end])
}
func without(ids []string, id string) []string {
	out := make([]string, 0, len(ids))
	for _, v := range ids {
		if v != id {
			out = append(out, v)
		}
	}
	return out
}
