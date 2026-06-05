package model

import (
	"fmt"
	"time"
)

type Script struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Content     string    `json:"content"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func NewScriptID() string {
	t := time.Now()
	return fmt.Sprintf("scpt-%s-%06d", t.Format("20060102-150405"), t.Nanosecond()/1000)
}

// Request/Response types for script API

type SaveScriptRequest struct {
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	Content     string   `json:"content"`
	Description string `json:"description"`
}

type SaveScriptResponse struct {
	Code   int     `json:"code"`
	Script *Script `json:"script,omitempty"`
}

type ListScriptsResponse struct {
	Code    int       `json:"code"`
	Scripts []*Script `json:"scripts"`
}

type DeleteScriptResponse struct {
	Code int `json:"code"`
}
