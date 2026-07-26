package handler

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
)

// SkillList returns installed skills (project or global) from cache.
func (h *Handler) SkillList(ctx context.Context, c *app.RequestContext) {
	global := string(c.Query("global")) == "true"
	skills, ready := h.skillsService.List(global)
	c.JSON(200, map[string]any{
		"code":   0,
		"skills": skills,
		"ready":  ready,
	})
}

// SkillAddRequest is the request body for adding a skill.
type SkillAddRequest struct {
	Package string   `json:"package"`
	Global  bool     `json:"global"`
	Agents  []string `json:"agents"`
	Skills  []string `json:"skills"`
}

// SkillAdd installs a skill package.
func (h *Handler) SkillAdd(ctx context.Context, c *app.RequestContext) {
	var req SkillAddRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	if req.Package == "" {
		httputil.BadRequest(c, "package is required")
		return
	}

	args := []string{"skills", "add", req.Package, "-y"}
	if req.Global {
		args = append(args, "-g")
	}
	if len(req.Agents) > 0 {
		for _, agent := range req.Agents {
			args = append(args, "-a", agent)
		}
	}
	if len(req.Skills) > 0 {
		args = append(args, "-s")
		args = append(args, req.Skills...)
	}

	cmdCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "npx", args...)
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := stderr.String()
		if errMsg == "" {
			errMsg = stdout.String()
		}
		if errMsg == "" {
			errMsg = err.Error()
		}
		c.JSON(http.StatusInternalServerError, map[string]any{
			"code":   -1,
			"msg":    "failed to add skill: " + stripAnsi(errMsg),
			"output": stripAnsi(stdout.String()),
		})
		return
	}

	h.skillsService.Invalidate()

	c.JSON(http.StatusOK, map[string]any{
		"code":   0,
		"output": stripAnsi(stdout.String()),
	})
}

// SkillRemoveRequest is the request body for removing a skill.
type SkillRemoveRequest struct {
	Name   string `json:"name"`
	Global bool   `json:"global"`
}

// SkillRemove uninstalls a skill.
func (h *Handler) SkillRemove(ctx context.Context, c *app.RequestContext) {
	var req SkillRemoveRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	if req.Name == "" {
		httputil.BadRequest(c, "name is required")
		return
	}

	args := []string{"skills", "remove", req.Name, "-y"}
	if req.Global {
		args = append(args, "-g")
	}

	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "npx", args...)
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		c.JSON(http.StatusInternalServerError, map[string]any{
			"code":   -1,
			"msg":    "failed to remove skill: " + stripAnsi(stderr.String()),
			"output": stripAnsi(stdout.String()),
		})
		return
	}

	h.skillsService.Invalidate()

	c.JSON(http.StatusOK, map[string]any{
		"code":   0,
		"output": stripAnsi(stdout.String()),
	})
}

// SkillCheck checks for available updates.
func (h *Handler) SkillCheck(ctx context.Context, c *app.RequestContext) {
	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "npx", "skills", "check")
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	_ = cmd.Run() // check may exit non-zero when updates are available

	c.JSON(http.StatusOK, map[string]any{
		"code":   0,
		"output": stripAnsi(stdout.String()),
	})
}

// SkillUpdate updates all skills to latest versions.
func (h *Handler) SkillUpdate(ctx context.Context, c *app.RequestContext) {
	cmdCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "npx", "skills", "update")
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		c.JSON(http.StatusInternalServerError, map[string]any{
			"code":   -1,
			"msg":    "failed to update skills: " + stripAnsi(stderr.String()),
			"output": stripAnsi(stdout.String()),
		})
		return
	}

	h.skillsService.Invalidate()

	c.JSON(http.StatusOK, map[string]any{
		"code":   0,
		"output": stripAnsi(stdout.String()),
	})
}

// SkillFindResult represents a search result item.
type SkillFindResult struct {
	Name     string `json:"name"`
	Installs string `json:"installs"`
	URL      string `json:"url"`
}

// SkillFind searches for available skills.
func (h *Handler) SkillFind(ctx context.Context, c *app.RequestContext) {
	query := string(c.Query("query"))
	if query == "" {
		httputil.BadRequest(c, "query is required")
		return
	}

	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "npx", "skills", "find", query)
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	_ = cmd.Run() // find may exit non-zero if no results

	results := parseSkillFindOutput(stdout.String())

	c.JSON(http.StatusOK, map[string]any{
		"code":    0,
		"results": results,
	})
}

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\].*?\x07|\x1b\(B`)

func stripAnsi(s string) string {
	cleaned := ansiRegex.ReplaceAllString(s, "")
	var lines []string
	for _, line := range strings.Split(cleaned, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		skip := true
		for _, r := range line {
			if r != '◒' && r != '◐' && r != '◓' && r != '◑' {
				skip = false
				break
			}
		}
		if skip {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// parseSkillFindOutput parses the ANSI-colored output of `npx skills find`.
// Expected format per result:
//
//	<name> <installs>
//	└ <url>
func parseSkillFindOutput(raw string) []SkillFindResult {
	clean := stripAnsi(raw)
	lines := strings.Split(clean, "\n")

	var results []SkillFindResult
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		// Skip empty lines, banner, and instruction lines
		if line == "" || strings.Contains(line, "╗") || strings.Contains(line, "╝") ||
			strings.Contains(line, "╚") || strings.Contains(line, "║") ||
			strings.Contains(line, "╔") || strings.HasPrefix(line, "Install with") {
			continue
		}
		// Look for lines with "installs" keyword
		if strings.Contains(line, "installs") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				name := parts[0]
				installs := parts[1]
				if len(parts) >= 3 {
					installs = parts[len(parts)-2] // e.g. "253.9K"
				}

				url := ""
				// Next non-empty line should be the URL
				for j := i + 1; j < len(lines); j++ {
					nextLine := strings.TrimSpace(stripAnsi(lines[j]))
					nextLine = strings.TrimPrefix(nextLine, "└ ")
					nextLine = strings.TrimPrefix(nextLine, "└")
					nextLine = strings.TrimSpace(nextLine)
					if nextLine != "" {
						url = nextLine
						i = j
						break
					}
				}

				results = append(results, SkillFindResult{
					Name:     name,
					Installs: installs,
					URL:      url,
				})
			}
		}
	}
	return results
}
