package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/model"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	CookieName         = "quartet_session"
	CSRFHeader         = "X-CSRF-Token"
	UserStatusActive   = "active"
	UserStatusDisabled = "disabled"
	UserStatusDeleted  = "deleted"
	AdminRoleID        = "admin"
	MemberRoleID       = "member"
	ViewerRoleID       = "viewer"
	// PersistentCookieMaxAge only controls how long clients retain the cookie.
	// Auth sessions themselves do not expire and remain valid until explicitly
	// revoked (logout, password reset/change, user disable, or user deletion).
	PersistentCookieMaxAge = 10 * 365 * 24 * time.Hour

	loginFailureWindow        = 10 * time.Minute
	loginFailureBlockDuration = 10 * time.Minute
	loginIPFailureLimit       = 10
	loginAccountFailureLimit  = 5
	loginFailureCleanupPeriod = time.Minute
	maxTrackedLoginFailures   = 8192
)

type State string

const (
	StateUninitialized State = "uninitialized"
	StateReady         State = "ready"
	StateRecovery      State = "recovery"
)

var (
	ErrUninitialized      = errors.New("authentication is not initialized")
	ErrRecovery           = errors.New("authentication configuration requires recovery")
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrUserDisabled       = errors.New("user is disabled")
	ErrUnauthorized       = errors.New("authentication required")
	ErrForbidden          = errors.New("permission denied")
	ErrRateLimited        = errors.New("too many failed login attempts")
	ErrConflict           = errors.New("record version conflict")
	ErrNotFound           = errors.New("record not found")
)

type Permission string

const (
	PermissionWorkspaceRead   Permission = "workspace.read"
	PermissionWorkspaceWrite  Permission = "workspace.write"
	PermissionJobRead         Permission = "job.read"
	PermissionJobExecute      Permission = "job.execute"
	PermissionJobManage       Permission = "job.manage"
	PermissionJobShare        Permission = "job.share"
	PermissionWorkflowRead    Permission = "workflow.read"
	PermissionWorkflowWrite   Permission = "workflow.write"
	PermissionWorkflowExecute Permission = "workflow.execute"
	PermissionScheduleRead    Permission = "schedule.read"
	PermissionScheduleWrite   Permission = "schedule.write"
	PermissionScheduleExecute Permission = "schedule.execute"
	PermissionFileRead        Permission = "file.read"
	PermissionFileWrite       Permission = "file.write"
	PermissionFileShare       Permission = "file.share"
	PermissionAgentRead       Permission = "agent.read"
	PermissionAgentManage     Permission = "agent.manage"
	PermissionConfigRead      Permission = "config.read"
	PermissionConfigWrite     Permission = "config.write"
	PermissionIMRead          Permission = "im.read"
	PermissionIMManage        Permission = "im.manage"
	PermissionIMSend          Permission = "im.send"
	PermissionStatsRead       Permission = "stats.read"
	PermissionLogsRead        Permission = "logs.read"
	PermissionLogsManage      Permission = "logs.manage"
	PermissionLogsReport      Permission = "logs.report"
	PermissionSkillsRead      Permission = "skills.read"
	PermissionSkillsManage    Permission = "skills.manage"
	PermissionSystemManage    Permission = "system.manage"
	PermissionUsersRead       Permission = "users.read"
	PermissionUsersManage     Permission = "users.manage"
	PermissionRolesRead       Permission = "roles.read"
	PermissionRolesManage     Permission = "roles.manage"
)

var permissionDescriptions = map[Permission]string{
	PermissionWorkspaceRead: "查看工作空间和最近目录", PermissionWorkspaceWrite: "管理工作空间和最近目录",
	PermissionJobRead: "查看 Job、会话和运行事件", PermissionJobExecute: "创建和控制 Job 运行", PermissionJobManage: "管理 Job", PermissionJobShare: "管理 Job 分享",
	PermissionWorkflowRead: "查看和校验工作流", PermissionWorkflowWrite: "管理工作流", PermissionWorkflowExecute: "运行工作流",
	PermissionScheduleRead: "查看定时任务", PermissionScheduleWrite: "管理定时任务", PermissionScheduleExecute: "立即运行定时任务",
	PermissionFileRead: "浏览和读取文件", PermissionFileWrite: "写入和上传文件", PermissionFileShare: "管理文件分享",
	PermissionAgentRead: "查看 Agent", PermissionAgentManage: "管理 Agent", PermissionConfigRead: "查看实例配置", PermissionConfigWrite: "修改实例配置",
	PermissionIMRead: "查看即时通讯状态", PermissionIMManage: "管理即时通讯连接", PermissionIMSend: "发送主动消息",
	PermissionStatsRead: "查看用量统计", PermissionLogsRead: "查看日志", PermissionLogsManage: "管理日志", PermissionLogsReport: "上报前端日志",
	PermissionSkillsRead: "查看 Skill", PermissionSkillsManage: "管理 Skill", PermissionSystemManage: "管理系统运行",
	PermissionUsersRead: "查看用户", PermissionUsersManage: "管理用户", PermissionRolesRead: "查看角色和权限", PermissionRolesManage: "管理角色",
}

var permissionDependencies = map[Permission][]Permission{
	PermissionWorkspaceWrite:  {PermissionWorkspaceRead},
	PermissionJobExecute:      {PermissionJobRead, PermissionAgentRead, PermissionWorkspaceRead},
	PermissionJobManage:       {PermissionJobRead},
	PermissionJobShare:        {PermissionJobRead},
	PermissionWorkflowWrite:   {PermissionWorkflowRead},
	PermissionWorkflowExecute: {PermissionWorkflowRead, PermissionJobExecute},
	PermissionScheduleWrite:   {PermissionScheduleRead, PermissionWorkflowRead, PermissionWorkspaceRead},
	PermissionScheduleExecute: {PermissionScheduleRead, PermissionWorkflowExecute},
	PermissionFileWrite:       {PermissionFileRead},
	PermissionFileShare:       {PermissionFileRead},
	PermissionAgentManage:     {PermissionAgentRead},
	PermissionConfigWrite:     {PermissionConfigRead},
	PermissionIMManage:        {PermissionIMRead},
	PermissionIMSend:          {PermissionIMRead},
	PermissionLogsManage:      {PermissionLogsRead},
	PermissionSkillsManage:    {PermissionSkillsRead},
	PermissionUsersManage:     {PermissionUsersRead, PermissionRolesRead},
	PermissionRolesManage:     {PermissionRolesRead},
}

type Service struct {
	mu             sync.RWMutex
	repo           *repository.AuthRepo
	state          State
	stateErr       error
	system         *model.AuthSystem
	users          map[string]*model.User
	roles          map[string]*model.Role
	loginFailures  map[string]*loginFailure
	loginCleanupAt time.Time
}

type loginFailure struct {
	Count        int
	WindowStart  time.Time
	BlockedUntil time.Time
	LastAttempt  time.Time
}

type loginFailureScope struct {
	key   string
	limit int
}

type loginRateLimitError struct {
	retryAt time.Time
}

func (e *loginRateLimitError) Error() string {
	return fmt.Sprintf("%s; retry after %s", ErrRateLimited, e.retryAt.Format(time.RFC3339))
}

func (e *loginRateLimitError) Unwrap() error { return ErrRateLimited }
func (e *loginRateLimitError) RetryAfter() time.Time {
	return e.retryAt
}

func NewService() (*Service, error) {
	repo, err := repository.NewAuthRepo()
	if err != nil {
		return nil, err
	}
	s := &Service{repo: repo, users: map[string]*model.User{}, roles: map[string]*model.Role{}, loginFailures: map[string]*loginFailure{}}
	s.load()
	return s, nil
}

func (s *Service) load() {
	exists, err := s.repo.ConfigEntriesExist()
	if err != nil {
		s.setRecovery(err)
		return
	}
	if !exists {
		s.state = StateUninitialized
		return
	}
	system, err := s.repo.LoadSystem()
	if err != nil {
		s.setRecovery(err)
		return
	}
	if !system.Initialized {
		s.setRecovery(errors.New("authentication system marker is not initialized"))
		return
	}
	if system.SchemaVersion != 1 {
		s.setRecovery(fmt.Errorf("unsupported authentication schema version %d", system.SchemaVersion))
		return
	}
	users, err := s.repo.ListUsers()
	if err != nil {
		s.setRecovery(err)
		return
	}
	roles, err := s.repo.ListRoles()
	if err != nil {
		s.setRecovery(err)
		return
	}
	s.system = system
	roleNames := map[string]string{}
	for _, role := range roles {
		if role.ID == "" || strings.TrimSpace(role.Name) == "" {
			s.setRecovery(errors.New("role id and name are required"))
			return
		}
		nameKey := strings.ToLower(strings.TrimSpace(role.Name))
		if prior := roleNames[nameKey]; prior != "" {
			s.setRecovery(fmt.Errorf("duplicate role name %s in roles %s and %s", nameKey, prior, role.ID))
			return
		}
		roleNames[nameKey] = role.ID
		if s.roles[role.ID] != nil {
			s.setRecovery(fmt.Errorf("duplicate role id %s", role.ID))
			return
		}
		s.roles[role.ID] = role
	}
	for _, user := range users {
		if s.users[user.ID] != nil {
			s.setRecovery(fmt.Errorf("duplicate user id %s", user.ID))
			return
		}
		s.users[user.ID] = user
	}
	if err := s.validateLocked(); err != nil {
		s.setRecovery(err)
		return
	}
	s.state = StateReady
}

func (s *Service) setRecovery(err error) { s.state, s.stateErr = StateRecovery, err }
func (s *Service) Status() (State, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.stateErr != nil {
		return s.state, s.stateErr.Error()
	}
	return s.state, ""
}

func permissions() []model.PermissionInfo {
	out := make([]model.PermissionInfo, 0, len(permissionDescriptions))
	for id, desc := range permissionDescriptions {
		out = append(out, model.PermissionInfo{ID: string(id), Description: desc})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func allPermissionIDs() []string {
	items := permissions()
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}

func memberPermissions() []string {
	return []string{
		"workspace.read", "workspace.write", "job.read", "job.execute", "job.manage", "job.share",
		"workflow.read", "workflow.write", "workflow.execute", "schedule.read", "schedule.write", "schedule.execute",
		"file.read", "file.write", "file.share", "agent.read", "config.read", "stats.read", "logs.report", "skills.read",
	}
}
func viewerPermissions() []string {
	return []string{
		"workspace.read", "job.read", "workflow.read", "schedule.read", "file.read", "agent.read", "config.read", "stats.read", "logs.report", "skills.read",
	}
}

func builtInRoles(now time.Time) []*model.Role {
	return []*model.Role{
		{ID: AdminRoleID, Name: "Administrator", Description: "All current and future permissions", Permissions: allPermissionIDs(), BuiltIn: true, Version: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: "system", UpdatedBy: "system"},
		{ID: MemberRoleID, Name: "Member", Description: "Daily shared-instance use", Permissions: memberPermissions(), BuiltIn: true, Version: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: "system", UpdatedBy: "system"},
		{ID: ViewerRoleID, Name: "Viewer", Description: "Read-only shared-instance access", Permissions: viewerPermissions(), BuiltIn: true, Version: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: "system", UpdatedBy: "system"},
	}
}

func (s *Service) Initialize(req model.InitAdminRequest) (string, *model.AuthPrincipal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateUninitialized {
		return "", nil, fmt.Errorf("%w: current state is %s", ErrConflict, s.state)
	}
	if req.Password != req.ConfirmPassword {
		return "", nil, errors.New("password confirmation does not match")
	}
	username, err := normalizeUsername(req.Username)
	if err != nil {
		return "", nil, err
	}
	if err := validatePassword(req.Password); err != nil {
		return "", nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return "", nil, err
	}
	now := time.Now().UTC()
	for _, role := range builtInRoles(now) {
		if err := s.repo.SaveRole(role); err != nil {
			return "", nil, err
		}
		s.roles[role.ID] = role
	}
	user := &model.User{ID: "user-" + uuid.NewString(), Username: username, DisplayName: displayName(req.DisplayName, username), PasswordHash: string(hash), RoleIDs: []string{AdminRoleID}, Status: UserStatusActive, Version: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: "system", UpdatedBy: "system"}
	if err := s.repo.SaveUser(user); err != nil {
		return "", nil, err
	}
	system := &model.AuthSystem{SchemaVersion: 1, Initialized: true, UpdatedAt: now}
	if err := s.repo.SaveSystem(system); err != nil {
		return "", nil, err
	}
	s.users[user.ID], s.system, s.state = user, system, StateReady
	return s.createSessionLocked(user)
}

func (s *Service) Login(username, password, source string) (string, *model.AuthPrincipal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireReadyLocked(); err != nil {
		return "", nil, err
	}
	username = strings.ToLower(strings.TrimSpace(username))
	now := time.Now().UTC()
	failureScopes := loginFailureScopes(username, source)
	if retryAt := s.loginBlockedUntilLocked(failureScopes, now); !retryAt.IsZero() {
		return "", nil, &loginRateLimitError{retryAt: retryAt}
	}
	var user *model.User
	for _, candidate := range s.users {
		if candidate.Username == username {
			user = candidate
			break
		}
	}
	if user == nil || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		s.recordLoginFailureLocked(failureScopes, now)
		return "", nil, ErrInvalidCredentials
	}
	if user.Status != UserStatusActive {
		s.recordLoginFailureLocked(failureScopes, now)
		return "", nil, ErrUserDisabled
	}
	// A valid login proves the account credential, so clear the account scope.
	// Keep the source-IP history: otherwise one known credential could reset the
	// IP bucket between password-spraying attempts against other accounts.
	delete(s.loginFailures, loginAccountFailureKey(username))
	return s.createSessionLocked(user)
}

func (s *Service) createSessionLocked(user *model.User) (string, *model.AuthPrincipal, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", nil, err
	}
	csrf, err := randomToken(24)
	if err != nil {
		return "", nil, err
	}
	session := &model.AuthSession{TokenHash: tokenHash(token), UserID: user.ID, CSRFToken: csrf, CreatedAt: time.Now().UTC()}
	if err := s.repo.SaveSession(session); err != nil {
		return "", nil, err
	}
	principal, err := s.principalLocked(user, csrf)
	if err != nil {
		_ = s.repo.DeleteSession(session.TokenHash)
		return "", nil, err
	}
	return token, principal, nil
}

func (s *Service) Authenticate(token string) (*model.AuthPrincipal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireReadyLocked(); err != nil {
		return nil, err
	}
	if token == "" {
		return nil, ErrUnauthorized
	}
	session, err := s.repo.LoadSession(tokenHash(token))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	user := s.users[session.UserID]
	if user == nil || user.Status != UserStatusActive {
		return nil, ErrUnauthorized
	}
	return s.principalLocked(user, session.CSRFToken)
}

func loginFailureScopes(username, source string) []loginFailureScope {
	return []loginFailureScope{
		{key: loginIPFailureKey(source), limit: loginIPFailureLimit},
		{key: loginAccountFailureKey(username), limit: loginAccountFailureLimit},
	}
}

func loginIPFailureKey(source string) string {
	return "ip:" + tokenHash(strings.TrimSpace(source))
}

func loginAccountFailureKey(username string) string {
	return "account:" + tokenHash(strings.ToLower(strings.TrimSpace(username)))
}

func (s *Service) loginBlockedUntilLocked(scopes []loginFailureScope, now time.Time) time.Time {
	s.cleanupLoginFailuresLocked(now)
	var retryAt time.Time
	for _, scope := range scopes {
		failure := s.activeLoginFailureLocked(scope.key, now)
		if failure != nil && now.Before(failure.BlockedUntil) && failure.BlockedUntil.After(retryAt) {
			retryAt = failure.BlockedUntil
		}
	}
	return retryAt
}

func (s *Service) recordLoginFailureLocked(scopes []loginFailureScope, now time.Time) {
	for _, scope := range scopes {
		failure := s.activeLoginFailureLocked(scope.key, now)
		if failure == nil {
			s.makeLoginFailureCapacityLocked(now)
			failure = &loginFailure{WindowStart: now}
			s.loginFailures[scope.key] = failure
		}
		failure.Count++
		failure.LastAttempt = now
		if failure.Count >= scope.limit {
			failure.BlockedUntil = now.Add(loginFailureBlockDuration)
		}
	}
}

func (s *Service) activeLoginFailureLocked(key string, now time.Time) *loginFailure {
	failure := s.loginFailures[key]
	if failure == nil {
		return nil
	}
	if !now.Before(failure.BlockedUntil) && now.Sub(failure.WindowStart) >= loginFailureWindow {
		delete(s.loginFailures, key)
		return nil
	}
	return failure
}

func (s *Service) cleanupLoginFailuresLocked(now time.Time) {
	if len(s.loginFailures) < maxTrackedLoginFailures && now.Before(s.loginCleanupAt) {
		return
	}
	for key := range s.loginFailures {
		s.activeLoginFailureLocked(key, now)
	}
	s.loginCleanupAt = now.Add(loginFailureCleanupPeriod)
}

func (s *Service) makeLoginFailureCapacityLocked(now time.Time) {
	if len(s.loginFailures) < maxTrackedLoginFailures {
		return
	}
	s.cleanupLoginFailuresLocked(now)
	if len(s.loginFailures) < maxTrackedLoginFailures {
		return
	}
	var oldestKey string
	var oldestAttempt time.Time
	for key, failure := range s.loginFailures {
		if oldestKey == "" || failure.LastAttempt.Before(oldestAttempt) {
			oldestKey = key
			oldestAttempt = failure.LastAttempt
		}
	}
	delete(s.loginFailures, oldestKey)
}

func (s *Service) Logout(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == "" {
		return nil
	}
	return s.repo.DeleteSession(tokenHash(token))
}

func (s *Service) principalLocked(user *model.User, csrf string) (*model.AuthPrincipal, error) {
	set := map[string]struct{}{}
	for _, roleID := range user.RoleIDs {
		role := s.roles[roleID]
		if role == nil {
			return nil, fmt.Errorf("user %s references unknown role %s", user.ID, roleID)
		}
		for _, permission := range role.Permissions {
			set[permission] = struct{}{}
		}
		if roleID == AdminRoleID {
			for _, permission := range allPermissionIDs() {
				set[permission] = struct{}{}
			}
		}
	}
	permissions := make([]string, 0, len(set))
	for permission := range set {
		permissions = append(permissions, permission)
	}
	sort.Strings(permissions)
	return &model.AuthPrincipal{User: userView(user), Permissions: permissions, CSRFToken: csrf}, nil
}

func (s *Service) Permissions() []model.PermissionInfo { return permissions() }

func (s *Service) HasPermission(principal *model.AuthPrincipal, permission Permission) bool {
	if principal == nil {
		return false
	}
	for _, current := range principal.Permissions {
		if current == string(permission) {
			return true
		}
	}
	return false
}

func (s *Service) ListUsers() []model.UserView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	users := make([]*model.User, 0, len(s.users))
	for _, user := range s.users {
		users = append(users, user)
	}
	repository.SortUsers(users)
	out := make([]model.UserView, 0, len(users))
	for _, user := range users {
		out = append(out, userView(user))
	}
	return out
}
func (s *Service) GetUser(id string) (model.UserView, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	user := s.users[id]
	if user == nil {
		return model.UserView{}, ErrNotFound
	}
	return userView(user), nil
}

func (s *Service) CreateUser(req model.CreateUserRequest, actor string) (model.UserView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireReadyLocked(); err != nil {
		return model.UserView{}, err
	}
	username, err := normalizeUsername(req.Username)
	if err != nil {
		return model.UserView{}, err
	}
	if s.usernameExistsLocked(username, "") {
		return model.UserView{}, errors.New("username already exists")
	}
	if err := validatePassword(req.Password); err != nil {
		return model.UserView{}, err
	}
	roles := req.RoleIDs
	if len(roles) == 0 {
		roles = []string{MemberRoleID}
	}
	if err := s.validateRoleIDsLocked(roles); err != nil {
		return model.UserView{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return model.UserView{}, err
	}
	now := time.Now().UTC()
	user := &model.User{ID: "user-" + uuid.NewString(), Username: username, DisplayName: displayName(req.DisplayName, username), PasswordHash: string(hash), RoleIDs: uniqueSorted(roles), Status: UserStatusActive, MustChangePassword: true, Version: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: actor, UpdatedBy: actor}
	if err := s.repo.SaveUser(user); err != nil {
		return model.UserView{}, err
	}
	s.users[user.ID] = user
	return userView(user), nil
}

func (s *Service) UpdateUser(id string, req model.UpdateUserRequest, actor string) (model.UserView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user := s.users[id]
	if user == nil {
		return model.UserView{}, ErrNotFound
	}
	if user.Status == UserStatusDeleted {
		return model.UserView{}, errors.New("deleted users cannot be modified or restored")
	}
	if req.Version != user.Version {
		return model.UserView{}, ErrConflict
	}
	next := *user
	next.RoleIDs = append([]string(nil), user.RoleIDs...)
	if req.Username != nil {
		username, err := normalizeUsername(*req.Username)
		if err != nil {
			return model.UserView{}, err
		}
		if s.usernameExistsLocked(username, id) {
			return model.UserView{}, errors.New("username already exists")
		}
		next.Username = username
	}
	if req.DisplayName != nil {
		next.DisplayName = displayName(*req.DisplayName, user.Username)
	}
	if req.RoleIDs != nil {
		if len(req.RoleIDs) == 0 {
			return model.UserView{}, errors.New("at least one role is required")
		}
		if err := s.validateRoleIDsLocked(req.RoleIDs); err != nil {
			return model.UserView{}, err
		}
		next.RoleIDs = uniqueSorted(req.RoleIDs)
	}
	if req.Status != nil {
		if !validStatus(*req.Status) {
			return model.UserView{}, fmt.Errorf("invalid user status %q", *req.Status)
		}
		next.Status = *req.Status
	}
	if !s.hasActiveAdminAfterLocked(&next) {
		return model.UserView{}, errors.New("at least one active administrator is required")
	}
	next.Version++
	next.UpdatedAt = time.Now().UTC()
	next.UpdatedBy = actor
	if err := s.repo.SaveUser(&next); err != nil {
		return model.UserView{}, err
	}
	s.users[id] = &next
	if next.Status != UserStatusActive {
		if err := s.revokeUserSessionsLocked(id); err != nil {
			return model.UserView{}, fmt.Errorf("revoke user sessions: %w", err)
		}
	}
	return userView(&next), nil
}
func (s *Service) DeleteUser(id string, version int64, actor string) (model.UserView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user := s.users[id]
	if user == nil {
		return model.UserView{}, ErrNotFound
	}
	if user.Status == UserStatusDeleted {
		return model.UserView{}, errors.New("user is already deleted")
	}
	if version != user.Version {
		return model.UserView{}, ErrConflict
	}
	next := *user
	next.Status = UserStatusDeleted
	// A deleted identity remains for audit attribution and username reuse
	// protection, but it no longer needs authorization assignments. Clearing
	// them prevents a deleted account from permanently blocking deletion of a
	// custom role that no active account references.
	next.RoleIDs = nil
	if !s.hasActiveAdminAfterLocked(&next) {
		return model.UserView{}, errors.New("at least one active administrator is required")
	}
	next.Version++
	next.UpdatedAt = time.Now().UTC()
	next.UpdatedBy = actor
	if err := s.repo.SaveUser(&next); err != nil {
		return model.UserView{}, err
	}
	s.users[id] = &next
	if err := s.revokeUserSessionsLocked(id); err != nil {
		return model.UserView{}, fmt.Errorf("revoke user sessions: %w", err)
	}
	return userView(&next), nil
}
func (s *Service) ResetPassword(id string, version int64, password, actor string) (model.UserView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validatePassword(password); err != nil {
		return model.UserView{}, err
	}
	user := s.users[id]
	if user == nil {
		return model.UserView{}, ErrNotFound
	}
	if user.Status == UserStatusDeleted {
		return model.UserView{}, errors.New("deleted users cannot have their password reset")
	}
	if version != user.Version {
		return model.UserView{}, ErrConflict
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return model.UserView{}, err
	}
	next := *user
	next.PasswordHash = string(hash)
	next.MustChangePassword = true
	next.Version++
	next.UpdatedAt = time.Now().UTC()
	next.UpdatedBy = actor
	if err := s.repo.SaveUser(&next); err != nil {
		return model.UserView{}, err
	}
	s.users[id] = &next
	if err := s.revokeUserSessionsLocked(id); err != nil {
		return model.UserView{}, fmt.Errorf("revoke user sessions: %w", err)
	}
	return userView(&next), nil
}
func (s *Service) UpdateProfile(id, display, actor string) (model.UserView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user := s.users[id]
	if user == nil {
		return model.UserView{}, ErrNotFound
	}
	next := *user
	next.DisplayName = displayName(display, user.Username)
	next.Version++
	next.UpdatedAt = time.Now().UTC()
	next.UpdatedBy = actor
	if err := s.repo.SaveUser(&next); err != nil {
		return model.UserView{}, err
	}
	s.users[id] = &next
	return userView(&next), nil
}
func (s *Service) ChangePassword(id, current, nextPassword string) (string, *model.AuthPrincipal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user := s.users[id]
	if user == nil {
		return "", nil, ErrNotFound
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(current)) != nil {
		return "", nil, ErrInvalidCredentials
	}
	if err := validatePassword(nextPassword); err != nil {
		return "", nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(nextPassword), bcrypt.DefaultCost)
	if err != nil {
		return "", nil, err
	}
	next := *user
	next.PasswordHash = string(hash)
	next.MustChangePassword = false
	next.Version++
	next.UpdatedAt = time.Now().UTC()
	next.UpdatedBy = id
	if err := s.repo.SaveUser(&next); err != nil {
		return "", nil, err
	}
	s.users[id] = &next
	if err := s.revokeUserSessionsLocked(id); err != nil {
		return "", nil, fmt.Errorf("revoke user sessions: %w", err)
	}
	return s.createSessionLocked(&next)
}

func (s *Service) ListRoles() []model.Role {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]*model.Role, 0, len(s.roles))
	for _, role := range s.roles {
		items = append(items, role)
	}
	repository.SortRoles(items)
	out := make([]model.Role, 0, len(items))
	for _, role := range items {
		out = append(out, *cloneRole(role))
	}
	return out
}
func (s *Service) GetRole(id string) (model.Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	role := s.roles[id]
	if role == nil {
		return model.Role{}, ErrNotFound
	}
	return *cloneRole(role), nil
}
func (s *Service) CreateRole(req model.CreateRoleRequest, actor string) (model.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return model.Role{}, errors.New("role name is required")
	}
	for _, r := range s.roles {
		if strings.EqualFold(r.Name, name) {
			return model.Role{}, errors.New("role name already exists")
		}
	}
	perms, err := validatePermissions(req.Permissions)
	if err != nil {
		return model.Role{}, err
	}
	now := time.Now().UTC()
	role := &model.Role{ID: "role-" + uuid.NewString(), Name: name, Description: strings.TrimSpace(req.Description), Permissions: perms, Version: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: actor, UpdatedBy: actor}
	if err := s.repo.SaveRole(role); err != nil {
		return model.Role{}, err
	}
	s.roles[role.ID] = role
	return *cloneRole(role), nil
}
func (s *Service) UpdateRole(id string, req model.UpdateRoleRequest, actor string) (model.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	role := s.roles[id]
	if role == nil {
		return model.Role{}, ErrNotFound
	}
	if role.BuiltIn {
		return model.Role{}, errors.New("built-in roles cannot be edited")
	}
	if req.Version != role.Version {
		return model.Role{}, ErrConflict
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return model.Role{}, errors.New("role name is required")
	}
	for otherID, r := range s.roles {
		if otherID != id && strings.EqualFold(r.Name, name) {
			return model.Role{}, errors.New("role name already exists")
		}
	}
	perms, err := validatePermissions(req.Permissions)
	if err != nil {
		return model.Role{}, err
	}
	next := *role
	next.Name = name
	next.Description = strings.TrimSpace(req.Description)
	next.Permissions = perms
	next.Version++
	next.UpdatedAt = time.Now().UTC()
	next.UpdatedBy = actor
	if err := s.repo.SaveRole(&next); err != nil {
		return model.Role{}, err
	}
	s.roles[id] = &next
	return *cloneRole(&next), nil
}
func (s *Service) DeleteRole(id string, version int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	role := s.roles[id]
	if role == nil {
		return ErrNotFound
	}
	if role.BuiltIn {
		return errors.New("built-in roles cannot be deleted")
	}
	if version != role.Version {
		return ErrConflict
	}
	for _, user := range s.users {
		for _, roleID := range user.RoleIDs {
			if roleID == id {
				return fmt.Errorf("role is assigned to user %s", user.Username)
			}
		}
	}
	if err := s.repo.DeleteRole(id); err != nil {
		return err
	}
	delete(s.roles, id)
	return nil
}

func (s *Service) validateLocked() error {
	if len(s.users) == 0 {
		return errors.New("initialized authentication has no users")
	}
	for _, id := range []string{AdminRoleID, MemberRoleID, ViewerRoleID} {
		role := s.roles[id]
		if role == nil {
			return fmt.Errorf("missing built-in role %s", id)
		}
		if !role.BuiltIn {
			return fmt.Errorf("role %s must be marked built-in", id)
		}
	}
	seen := map[string]string{}
	activeAdmin := false
	for _, u := range s.users {
		if u.ID == "" || u.Username == "" {
			return errors.New("user id and username are required")
		}
		normalized := strings.ToLower(strings.TrimSpace(u.Username))
		if normalized != u.Username {
			return fmt.Errorf("user %s has non-normalized username %q", u.ID, u.Username)
		}
		if !validStatus(u.Status) {
			return fmt.Errorf("user %s has invalid status %q", u.ID, u.Status)
		}
		if u.Status != UserStatusDeleted && len(u.RoleIDs) == 0 {
			return fmt.Errorf("user %s has no roles", u.ID)
		}
		if strings.TrimSpace(u.PasswordHash) == "" {
			return fmt.Errorf("user %s has no password hash", u.ID)
		}
		if prior := seen[normalized]; prior != "" {
			return fmt.Errorf("duplicate username %s in users %s and %s", normalized, prior, u.ID)
		}
		seen[normalized] = u.ID
		if u.Status != UserStatusDeleted {
			if err := s.validateRoleIDsLocked(u.RoleIDs); err != nil {
				return fmt.Errorf("user %s: %w", u.ID, err)
			}
		}
		if u.Status == UserStatusActive && contains(u.RoleIDs, AdminRoleID) {
			activeAdmin = true
		}
	}
	if !activeAdmin {
		return errors.New("no active administrator")
	}
	for _, role := range s.roles {
		if _, err := validatePermissions(role.Permissions); err != nil {
			return fmt.Errorf("role %s: %w", role.ID, err)
		}
	}
	return nil
}
func (s *Service) requireReadyLocked() error {
	if s.state == StateReady {
		return nil
	}
	if s.state == StateRecovery {
		return fmt.Errorf("%w: %v", ErrRecovery, s.stateErr)
	}
	return ErrUninitialized
}
func (s *Service) validateRoleIDsLocked(ids []string) error {
	for _, id := range uniqueSorted(ids) {
		if s.roles[id] == nil {
			return fmt.Errorf("unknown role %s", id)
		}
	}
	return nil
}
func (s *Service) usernameExistsLocked(name, except string) bool {
	for id, u := range s.users {
		if id != except && u.Username == name {
			return true
		}
	}
	return false
}
func (s *Service) hasActiveAdminAfterLocked(updated *model.User) bool {
	for id, u := range s.users {
		candidate := u
		if id == updated.ID {
			candidate = updated
		}
		if candidate.Status == UserStatusActive && contains(candidate.RoleIDs, AdminRoleID) {
			return true
		}
	}
	return false
}
func (s *Service) revokeUserSessionsLocked(userID string) error {
	sessions, err := s.repo.ListSessions()
	if err != nil {
		return err
	}
	var errs []string
	for _, session := range sessions {
		if session.UserID == userID {
			if err := s.repo.DeleteSession(session.TokenHash); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}
func normalizeUsername(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < 3 || len(value) > 64 {
		return "", errors.New("username must be between 3 and 64 characters")
	}
	for _, r := range value {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.') {
			return "", errors.New("username may only contain letters, numbers, dot, underscore, and hyphen")
		}
	}
	return value, nil
}
func validatePassword(value string) error {
	if len(value) < 8 {
		return errors.New("password must contain at least 8 characters")
	}
	hasLetter, hasDigit := false, false
	for _, r := range value {
		hasLetter = hasLetter || unicode.IsLetter(r)
		hasDigit = hasDigit || unicode.IsDigit(r)
	}
	if !hasLetter || !hasDigit {
		return errors.New("password must contain at least one letter and one number")
	}
	return nil
}
func validatePermissions(values []string) ([]string, error) {
	values = uniqueSorted(values)
	set := map[Permission]bool{}
	for _, value := range values {
		p := Permission(value)
		if _, ok := permissionDescriptions[p]; !ok {
			return nil, fmt.Errorf("unknown permission %s", value)
		}
		set[p] = true
	}
	missing := []string{}
	for p, dependencies := range permissionDependencies {
		if !set[p] {
			continue
		}
		for _, dependency := range dependencies {
			if !set[dependency] {
				missing = append(missing, string(dependency))
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("missing required permissions: %s", strings.Join(uniqueSorted(missing), ", "))
	}
	return values, nil
}
func userView(u *model.User) model.UserView {
	roleIDs := append([]string{}, u.RoleIDs...)
	return model.UserView{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, RoleIDs: roleIDs, Status: u.Status, MustChangePassword: u.MustChangePassword, Version: u.Version, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt, CreatedBy: u.CreatedBy, UpdatedBy: u.UpdatedBy}
}
func cloneRole(role *model.Role) *model.Role {
	next := *role
	next.Permissions = append([]string(nil), role.Permissions...)
	return &next
}
func uniqueSorted(values []string) []string {
	set := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			set[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func displayName(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
func randomToken(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func validStatus(status string) bool {
	return status == UserStatusActive || status == UserStatusDisabled || status == UserStatusDeleted
}
