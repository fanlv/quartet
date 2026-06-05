// Package model re-exports the non-file request/result types of the upstream
// github.com/deep-agent/sandbox/types/model so that runtime callers (bash exec,
// MCP host, middlewares) can keep a quartet-local import path.
//
// File and JSONL types live in pkg/fileserver/model. Swapping the underlying
// SDK later means changing this file and pkg/fileserver/model/model.go.
package model

import sbmodel "github.com/deep-agent/sandbox/types/model"

// Bash.
type (
	BashExecRequest = sbmodel.BashExecRequest
	BashExecResult  = sbmodel.BashExecResult
)

// Grep.
type (
	GrepRequest = sbmodel.GrepRequest
	GrepResult  = sbmodel.GrepResult
)

// Context / transport.
type (
	Response       = sbmodel.Response
	SandboxContext = sbmodel.SandboxContext
)

// Web.
type (
	WebFetchRequest     = sbmodel.WebFetchRequest
	WebFetchResult      = sbmodel.WebFetchResult
	WebSearchRequest    = sbmodel.WebSearchRequest
	WebSearchResult     = sbmodel.WebSearchResult
	WebSearchResultItem = sbmodel.WebSearchResultItem
)
