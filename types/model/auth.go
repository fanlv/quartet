package model

import "time"

type AuthSystem struct {
	SchemaVersion int       `json:"schemaVersion"`
	Initialized   bool      `json:"initialized"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type User struct {
	ID                 string    `json:"id"`
	Username           string    `json:"username"`
	DisplayName        string    `json:"displayName"`
	PasswordHash       string    `json:"passwordHash"`
	RoleIDs            []string  `json:"roleIds"`
	Status             string    `json:"status"`
	MustChangePassword bool      `json:"mustChangePassword"`
	Version            int64     `json:"version"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
	CreatedBy          string    `json:"createdBy"`
	UpdatedBy          string    `json:"updatedBy"`
}

type Role struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Permissions []string  `json:"permissions"`
	BuiltIn     bool      `json:"builtIn"`
	Version     int64     `json:"version"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	CreatedBy   string    `json:"createdBy"`
	UpdatedBy   string    `json:"updatedBy"`
}

type AuthSession struct {
	TokenHash string    `json:"tokenHash"`
	UserID    string    `json:"userId"`
	CSRFToken string    `json:"csrfToken"`
	CreatedAt time.Time `json:"createdAt"`
}

type UserView struct {
	ID                 string    `json:"id"`
	Username           string    `json:"username"`
	DisplayName        string    `json:"displayName"`
	RoleIDs            []string  `json:"roleIds"`
	Status             string    `json:"status"`
	MustChangePassword bool      `json:"mustChangePassword"`
	Version            int64     `json:"version"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
	CreatedBy          string    `json:"createdBy"`
	UpdatedBy          string    `json:"updatedBy"`
}

type PermissionInfo struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

type AuthPrincipal struct {
	User        UserView `json:"user"`
	Permissions []string `json:"permissions"`
	CSRFToken   string   `json:"csrfToken"`
}

type InitAdminRequest struct {
	Username        string `json:"username"`
	DisplayName     string `json:"displayName"`
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirmPassword"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type UpdateProfileRequest struct {
	DisplayName string `json:"displayName"`
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

type CreateUserRequest struct {
	Username    string   `json:"username"`
	DisplayName string   `json:"displayName"`
	Password    string   `json:"password"`
	RoleIDs     []string `json:"roleIds"`
}

type UpdateUserRequest struct {
	Version     int64    `json:"version"`
	Username    *string  `json:"username,omitempty"`
	DisplayName *string  `json:"displayName,omitempty"`
	RoleIDs     []string `json:"roleIds,omitempty"`
	Status      *string  `json:"status,omitempty"`
}

type ResetPasswordRequest struct {
	Version  int64  `json:"version"`
	Password string `json:"password"`
}

type DeleteAuthRecordRequest struct {
	Version int64 `json:"version"`
}

type CreateRoleRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}

type UpdateRoleRequest struct {
	Version     int64    `json:"version"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}
