package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/services/auth"
	"github.com/fanlv/quartet/types/model"
)

func (h *Handler) AuthStatus() (auth.State, string) { return h.authService.Status() }

func (h *Handler) AuthInit(_ context.Context, c *app.RequestContext) {
	var req model.InitAdminRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	token, principal, err := h.authService.Initialize(req)
	if err != nil {
		logger.Warnf(context.Background(), "[auth.audit] actor=anonymous action=auth.initialize target=%q result=failure err=%v", req.Username, err)
		writeAuthError(c, err)
		return
	}
	setSessionCookie(c, token)
	logger.Infof(context.Background(), "[auth] initialized administrator userId=%s username=%s remote=%s", principal.User.ID, principal.User.Username, c.ClientIP())
	c.JSON(http.StatusOK, principal)
}

func (h *Handler) AuthLogin(_ context.Context, c *app.RequestContext) {
	var req model.LoginRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	token, principal, err := h.authService.Login(req.Username, req.Password, c.ClientIP())
	if err != nil {
		logger.Warnf(context.Background(), "[auth.audit] actor=anonymous action=auth.login target=%q remote=%s result=failure err=%v", req.Username, c.ClientIP(), err)
		writeAuthError(c, err)
		return
	}
	setSessionCookie(c, token)
	logger.Infof(context.Background(), "[auth] login succeeded userId=%s username=%s remote=%s", principal.User.ID, principal.User.Username, c.ClientIP())
	c.JSON(http.StatusOK, principal)
}

func (h *Handler) AuthLogout(_ context.Context, c *app.RequestContext) {
	if err := h.authService.Logout(string(c.Cookie(auth.CookieName))); err != nil {
		httputil.InternalError(c, err.Error())
		return
	}
	clearSessionCookie(c)
	c.JSON(http.StatusOK, map[string]any{"code": 0})
}

func (h *Handler) AuthMe(_ context.Context, c *app.RequestContext) {
	principal, ok := CurrentPrincipal(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, httputil.ErrResponse{Code: -1, Msg: auth.ErrUnauthorized.Error()})
		return
	}
	c.JSON(http.StatusOK, principal)
}

func (h *Handler) AuthUpdateProfile(_ context.Context, c *app.RequestContext) {
	principal, ok := CurrentPrincipal(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, httputil.ErrResponse{Code: -1, Msg: auth.ErrUnauthorized.Error()})
		return
	}
	var req model.UpdateProfileRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	user, err := h.authService.UpdateProfile(principal.User.ID, req.DisplayName, principal.User.ID)
	if err != nil {
		logger.Warnf(context.Background(), "[auth.audit] actor=%s action=user.profile-update target=%s result=failure err=%v", principal.User.ID, principal.User.ID, err)
		writeAuthError(c, err)
		return
	}
	logger.Infof(context.Background(), "[auth.audit] actor=%s action=user.profile-update target=%s result=success", principal.User.ID, principal.User.ID)
	c.JSON(http.StatusOK, map[string]any{"user": user})
}

func (h *Handler) AuthChangePassword(_ context.Context, c *app.RequestContext) {
	principal, ok := CurrentPrincipal(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, httputil.ErrResponse{Code: -1, Msg: auth.ErrUnauthorized.Error()})
		return
	}
	var req model.ChangePasswordRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	token, next, err := h.authService.ChangePassword(principal.User.ID, req.CurrentPassword, req.NewPassword)
	if err != nil {
		logger.Warnf(context.Background(), "[auth.audit] actor=%s action=user.change-password target=%s result=failure err=%v", principal.User.ID, principal.User.ID, err)
		writeAuthError(c, err)
		return
	}
	setSessionCookie(c, token)
	logger.Infof(context.Background(), "[auth.audit] actor=%s action=user.change-password target=%s result=success", principal.User.ID, principal.User.ID)
	c.JSON(http.StatusOK, next)
}

func (h *Handler) UserList(_ context.Context, c *app.RequestContext) {
	c.JSON(http.StatusOK, map[string]any{"users": h.authService.ListUsers()})
}
func (h *Handler) UserGet(_ context.Context, c *app.RequestContext) {
	user, err := h.authService.GetUser(c.Param("userId"))
	if err != nil {
		writeAuthError(c, err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"user": user})
}
func (h *Handler) UserCreate(_ context.Context, c *app.RequestContext) {
	var req model.CreateUserRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	user, err := h.authService.CreateUser(req, currentUserID(c))
	if err != nil {
		logger.Warnf(context.Background(), "[auth.audit] actor=%s action=user.create target=%q result=failure err=%v", currentUserID(c), req.Username, err)
		writeAuthError(c, err)
		return
	}
	logger.Infof(context.Background(), "[auth.audit] actor=%s action=user.create target=%s result=success", currentUserID(c), user.ID)
	c.JSON(http.StatusCreated, map[string]any{"user": user})
}
func (h *Handler) UserUpdate(_ context.Context, c *app.RequestContext) {
	var req model.UpdateUserRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	user, err := h.authService.UpdateUser(c.Param("userId"), req, currentUserID(c))
	if err != nil {
		logger.Warnf(context.Background(), "[auth.audit] actor=%s action=user.update target=%s result=failure err=%v", currentUserID(c), c.Param("userId"), err)
		writeAuthError(c, err)
		return
	}
	logger.Infof(context.Background(), "[auth.audit] actor=%s action=user.update target=%s result=success", currentUserID(c), user.ID)
	c.JSON(http.StatusOK, map[string]any{"user": user})
}
func (h *Handler) UserDelete(_ context.Context, c *app.RequestContext) {
	var req model.DeleteAuthRecordRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	user, err := h.authService.DeleteUser(c.Param("userId"), req.Version, currentUserID(c))
	if err != nil {
		logger.Warnf(context.Background(), "[auth.audit] actor=%s action=user.delete target=%s result=failure err=%v", currentUserID(c), c.Param("userId"), err)
		writeAuthError(c, err)
		return
	}
	logger.Infof(context.Background(), "[auth.audit] actor=%s action=user.delete target=%s result=success", currentUserID(c), user.ID)
	c.JSON(http.StatusOK, map[string]any{"user": user})
}
func (h *Handler) UserResetPassword(_ context.Context, c *app.RequestContext) {
	var req model.ResetPasswordRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	user, err := h.authService.ResetPassword(c.Param("userId"), req.Version, req.Password, currentUserID(c))
	if err != nil {
		logger.Warnf(context.Background(), "[auth.audit] actor=%s action=user.reset-password target=%s result=failure err=%v", currentUserID(c), c.Param("userId"), err)
		writeAuthError(c, err)
		return
	}
	logger.Infof(context.Background(), "[auth.audit] actor=%s action=user.reset-password target=%s result=success", currentUserID(c), user.ID)
	c.JSON(http.StatusOK, map[string]any{"user": user})
}
func (h *Handler) PermissionList(_ context.Context, c *app.RequestContext) {
	c.JSON(http.StatusOK, map[string]any{"permissions": h.authService.Permissions()})
}
func (h *Handler) RoleList(_ context.Context, c *app.RequestContext) {
	c.JSON(http.StatusOK, map[string]any{"roles": h.authService.ListRoles()})
}
func (h *Handler) RoleGet(_ context.Context, c *app.RequestContext) {
	role, err := h.authService.GetRole(c.Param("roleId"))
	if err != nil {
		writeAuthError(c, err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"role": role})
}
func (h *Handler) RoleCreate(_ context.Context, c *app.RequestContext) {
	var req model.CreateRoleRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	role, err := h.authService.CreateRole(req, currentUserID(c))
	if err != nil {
		logger.Warnf(context.Background(), "[auth.audit] actor=%s action=role.create target=%q result=failure err=%v", currentUserID(c), req.Name, err)
		writeAuthError(c, err)
		return
	}
	logger.Infof(context.Background(), "[auth.audit] actor=%s action=role.create target=%s result=success", currentUserID(c), role.ID)
	c.JSON(http.StatusCreated, map[string]any{"role": role})
}
func (h *Handler) RoleUpdate(_ context.Context, c *app.RequestContext) {
	var req model.UpdateRoleRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	role, err := h.authService.UpdateRole(c.Param("roleId"), req, currentUserID(c))
	if err != nil {
		logger.Warnf(context.Background(), "[auth.audit] actor=%s action=role.update target=%s result=failure err=%v", currentUserID(c), c.Param("roleId"), err)
		writeAuthError(c, err)
		return
	}
	logger.Infof(context.Background(), "[auth.audit] actor=%s action=role.update target=%s result=success", currentUserID(c), role.ID)
	c.JSON(http.StatusOK, map[string]any{"role": role})
}
func (h *Handler) RoleDelete(_ context.Context, c *app.RequestContext) {
	roleID := c.Param("roleId")
	var req model.DeleteAuthRecordRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	if err := h.authService.DeleteRole(roleID, req.Version); err != nil {
		logger.Warnf(context.Background(), "[auth.audit] actor=%s action=role.delete target=%s result=failure err=%v", currentUserID(c), roleID, err)
		writeAuthError(c, err)
		return
	}
	logger.Infof(context.Background(), "[auth.audit] actor=%s action=role.delete target=%s result=success", currentUserID(c), roleID)
	c.JSON(http.StatusOK, map[string]any{"code": 0})
}

func CurrentPrincipal(c *app.RequestContext) (*model.AuthPrincipal, bool) {
	value, ok := c.Get("authPrincipal")
	if !ok {
		return nil, false
	}
	principal, ok := value.(*model.AuthPrincipal)
	return principal, ok
}
func currentUserID(c *app.RequestContext) string {
	principal, _ := CurrentPrincipal(c)
	if principal == nil {
		return "system"
	}
	return principal.User.ID
}
func requestIsSecure(c *app.RequestContext) bool {
	return strings.EqualFold(string(c.URI().Scheme()), "https") || strings.EqualFold(strings.TrimSpace(string(c.GetHeader("X-Forwarded-Proto"))), "https")
}
func setSessionCookie(c *app.RequestContext, token string) {
	c.SetCookie(auth.CookieName, token, int(auth.PersistentCookieMaxAge/time.Second), "/", "", protocol.CookieSameSiteStrictMode, requestIsSecure(c), true)
}
func clearSessionCookie(c *app.RequestContext) {
	c.SetCookie(auth.CookieName, "", -1, "/", "", protocol.CookieSameSiteStrictMode, requestIsSecure(c), true)
}
func writeAuthError(c *app.RequestContext, err error) {
	switch {
	case errors.Is(err, auth.ErrUnauthorized), errors.Is(err, auth.ErrInvalidCredentials):
		c.JSON(http.StatusUnauthorized, httputil.ErrResponse{Code: -1, Msg: err.Error()})
	case errors.Is(err, auth.ErrForbidden), errors.Is(err, auth.ErrUserDisabled):
		c.JSON(http.StatusForbidden, httputil.ErrResponse{Code: -1, Msg: err.Error()})
	case errors.Is(err, auth.ErrConflict):
		httputil.Conflict(c, err.Error())
	case errors.Is(err, auth.ErrRateLimited):
		c.JSON(http.StatusTooManyRequests, httputil.ErrResponse{Code: -1, Msg: err.Error()})
	case errors.Is(err, auth.ErrNotFound):
		httputil.NotFound(c, err.Error())
	case errors.Is(err, auth.ErrUninitialized), errors.Is(err, auth.ErrRecovery):
		c.JSON(http.StatusServiceUnavailable, httputil.ErrResponse{Code: -1, Msg: err.Error()})
	default:
		httputil.BadRequest(c, err.Error())
	}
}
