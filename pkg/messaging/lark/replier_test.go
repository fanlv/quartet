package lark

import (
	"encoding/json"
	"testing"
)

func TestBuildPostContent(t *testing.T) {
	got, err := buildPostContent("hello\nworld")
	if err != nil {
		t.Fatalf("buildPostContent returned error: %v", err)
	}

	var payload struct {
		ZhCN struct {
			Title   string `json:"title"`
			Content [][]struct {
				Tag  string `json:"tag"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"zh_cn"`
		EnUS struct {
			Title   string `json:"title"`
			Content [][]struct {
				Tag  string `json:"tag"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"en_us"`
	}
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("payload is not valid json: %v", err)
	}

	if payload.ZhCN.Title != "" {
		t.Fatalf("expected empty title, got %q", payload.ZhCN.Title)
	}
	if len(payload.ZhCN.Content) != 1 || len(payload.ZhCN.Content[0]) != 1 {
		t.Fatalf("expected one paragraph with one element, got %#v", payload.ZhCN.Content)
	}
	if payload.ZhCN.Content[0][0].Tag != "md" {
		t.Fatalf("expected md tag, got %q", payload.ZhCN.Content[0][0].Tag)
	}
	if payload.ZhCN.Content[0][0].Text != "hello\nworld" {
		t.Fatalf("expected original text content to be preserved, got %q", payload.ZhCN.Content[0][0].Text)
	}
	if payload.EnUS.Content[0][0].Text != payload.ZhCN.Content[0][0].Text {
		t.Fatalf("expected en_us content to mirror zh_cn, got %q", payload.EnUS.Content[0][0].Text)
	}
}
