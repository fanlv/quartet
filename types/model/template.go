package model

import (
	"fmt"
	"time"
)

type LoopTemplate struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Config    LoopConfig `json:"config"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt,omitempty"`
}

func NewTemplateID() string {
	t := time.Now()
	return fmt.Sprintf("tmpl-%s-%06d", t.Format("20060102-150405"), t.Nanosecond()/1000)
}

// Request/Response types for template API

type SaveTemplateRequest struct {
	// Save always allocates a fresh ID server-side; clients cannot choose it.
	// Use Update (PUT /template/:id) to modify an existing template.
	Name   string     `json:"name"`
	Config LoopConfig `json:"config"`
}

type SaveTemplateResponse struct {
	Code     int           `json:"code"`
	Template *LoopTemplate `json:"template,omitempty"`
}

type UpdateTemplateRequest struct {
	Name   string     `json:"name"`
	Config LoopConfig `json:"config"`
}

type UpdateTemplateResponse struct {
	Code     int           `json:"code"`
	Template *LoopTemplate `json:"template,omitempty"`
}

type ListTemplatesResponse struct {
	Code      int             `json:"code"`
	Templates []*LoopTemplate `json:"templates"`
}

type DeleteTemplateRequest struct {
	ID string `json:"id"`
}

type DeleteTemplateResponse struct {
	Code int `json:"code"`
}
