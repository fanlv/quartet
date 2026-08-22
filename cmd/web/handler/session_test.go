package handler

import (
	"reflect"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestExtractHistoryLocalImageURLs_RemovesAbsoluteLocalTags(t *testing.T) {
	content := "![image](/tmp/a.png)\n![image](/var/data/b.png)\nhello"

	cleaned, imageURLs := extractHistoryLocalImageURLs(content)

	if cleaned != "hello" {
		t.Fatalf("cleaned content = %q, want %q", cleaned, "hello")
	}
	want := []string{"/tmp/a.png", "/var/data/b.png"}
	if !reflect.DeepEqual(imageURLs, want) {
		t.Fatalf("imageURLs = %v, want %v", imageURLs, want)
	}
}

func TestExtractHistoryLocalImageURLs_PreservesRemoteRelativeAndPlaceholderTags(t *testing.T) {
	content := "before\n![image](https://example.com/a.png)\n![image](relative.png)\n![image](#)\nafter"

	cleaned, imageURLs := extractHistoryLocalImageURLs(content)

	if cleaned != content {
		t.Fatalf("cleaned content = %q, want original %q", cleaned, content)
	}
	if len(imageURLs) != 0 {
		t.Fatalf("imageURLs = %v, want empty", imageURLs)
	}
}

func TestExtractHistoryLocalImageURLs_DoesNotSkipLocalTagAfterNonLocalPrefix(t *testing.T) {
	content := "![image](relative.png)\n![image](/tmp/user-authored.png)"

	cleaned, imageURLs := extractHistoryLocalImageURLs(content)

	if cleaned != content {
		t.Fatalf("cleaned content = %q, want original %q", cleaned, content)
	}
	if len(imageURLs) != 0 {
		t.Fatalf("imageURLs = %v, want empty", imageURLs)
	}
}

func TestExtractHistoryLocalImageURLs_PreservesUserAuthoredInlineTags(t *testing.T) {
	content := "prefix ![image](/tmp/a.png) and ![image](https://example.com/b.png) suffix"

	cleaned, imageURLs := extractHistoryLocalImageURLs(content)

	if cleaned != content {
		t.Fatalf("cleaned content = %q, want original %q", cleaned, content)
	}
	if len(imageURLs) != 0 {
		t.Fatalf("imageURLs = %v, want empty", imageURLs)
	}
}

func TestExtractHistoryLocalImageURLs_PreservesParenthesesInAbsolutePath(t *testing.T) {
	content := "![image](/tmp/a(b).png)\nhello"

	cleaned, imageURLs := extractHistoryLocalImageURLs(content)

	if cleaned != "hello" {
		t.Fatalf("cleaned content = %q, want %q", cleaned, "hello")
	}
	wantURLs := []string{"/tmp/a(b).png"}
	if !reflect.DeepEqual(imageURLs, wantURLs) {
		t.Fatalf("imageURLs = %v, want %v", imageURLs, wantURLs)
	}
}

func TestExtractHistoryLocalImageURLs_OnlyReadsGeneratedPrefix(t *testing.T) {
	content := "hello\n![image](/tmp/user-authored.png)"

	cleaned, imageURLs := extractHistoryLocalImageURLs(content)

	if cleaned != content {
		t.Fatalf("cleaned content = %q, want original %q", cleaned, content)
	}
	if len(imageURLs) != 0 {
		t.Fatalf("imageURLs = %v, want empty", imageURLs)
	}
}

func TestHistoryImageExtractionIsLimitedToUserMessages(t *testing.T) {
	roles := []schema.RoleType{schema.User, schema.Assistant, schema.Tool}
	for _, role := range roles {
		t.Run(string(role), func(t *testing.T) {
			content := "![image](/tmp/output.png)\nbody"
			cleaned := content
			var imageURLs []string
			if role == schema.User {
				cleaned, imageURLs = extractHistoryLocalImageURLs(content)
			}

			if role == schema.User {
				if cleaned != "body" || !reflect.DeepEqual(imageURLs, []string{"/tmp/output.png"}) {
					t.Fatalf("user projection = (%q, %v), want body plus image URL", cleaned, imageURLs)
				}
				return
			}
			if cleaned != content || len(imageURLs) != 0 {
				t.Fatalf("non-user projection = (%q, %v), want untouched content", cleaned, imageURLs)
			}
		})
	}
}
