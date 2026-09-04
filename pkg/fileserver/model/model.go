// Package model defines the Quartet-local file and JSONL request/result
// types. The fileserver adapter converts these to the current upstream sandbox
// SDK types, which keeps the rest of the codebase decoupled from that SDK.
//
// Only File* and JSONL* types live here; Agent runtime types stay in their own
// execution package.
package model

type FileReadRequest struct {
	File   string `json:"file" vd:"len($)>0"`
	Base64 bool   `json:"base64,omitempty"`
}

type FileReadResult struct {
	Content string `json:"content"`
}

type FileWriteRequest struct {
	File    string `json:"file" vd:"len($)>0"`
	Content string `json:"content"`
	Base64  bool   `json:"base64,omitempty"`
	Mode    uint32 `json:"mode,omitempty"`
	Atomic  bool   `json:"atomic,omitempty"`
}

type FileListRequest struct {
	Path string `json:"path" vd:"len($)>0"`
}

type FileListResult struct {
	Files []FileInfo `json:"files"`
}

type FileInfo struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	IsDir       bool   `json:"is_dir"`
	Mode        string `json:"mode"`
	ModTimeUnix int64  `json:"mod_time_unix"`
}

type FileDeleteRequest struct {
	Path string `json:"path" vd:"len($)>0"`
}

type FileMoveRequest struct {
	Source      string `json:"source" vd:"len($)>0"`
	Destination string `json:"destination" vd:"len($)>0"`
}

type FileCopyRequest struct {
	Source      string `json:"source" vd:"len($)>0"`
	Destination string `json:"destination" vd:"len($)>0"`
}

type MkDirRequest struct {
	Path string `json:"path" vd:"len($)>0"`
}

type FileExistsResult struct {
	Exists bool `json:"exists"`
}

type FileUploadResult struct {
	File string `json:"file"`
	Size int64  `json:"size"`
}

type FileDownloadRequest struct {
	File string `json:"file" query:"file" vd:"len($)>0"`
}

type FileCreateTempRequest struct {
	Dir     string `json:"dir,omitempty"`
	Pattern string `json:"pattern,omitempty"`
	Content string `json:"content,omitempty"`
	Base64  bool   `json:"base64,omitempty"`
	Mode    uint32 `json:"mode,omitempty"`
}

type FileCreateTempResult struct {
	File string `json:"file"`
}

type FileGlobRequest struct {
	Path    string `json:"path,omitempty"`
	Pattern string `json:"pattern" vd:"len($)>0"`
	Limit   int    `json:"limit,omitempty"`
}

type FileGlobResult struct {
	Files     []string `json:"files"`
	Count     int      `json:"count"`
	Truncated bool     `json:"truncated"`
	Output    string   `json:"output,omitempty"`
}

type FileEvalSymlinksRequest struct {
	Path string `json:"path" vd:"len($)>0"`
}

type FileEvalSymlinksResult struct {
	ResolvedPath string `json:"resolved_path"`
}

type FileAppendRequest struct {
	File    string `json:"file" vd:"len($)>0"`
	Content string `json:"content"`
}

type FileStatRequest struct {
	Path string `json:"path" vd:"len($)>0"`
}

type FileStatResult struct {
	Exists      bool   `json:"exists"`
	IsDir       bool   `json:"is_dir"`
	Size        int64  `json:"size"`
	Mode        string `json:"mode"`
	ModTimeUnix int64  `json:"mod_time_unix"`
}

type TempDirResult struct {
	Path string `json:"path"`
}

type UserHomeDirResult struct {
	Path string `json:"path"`
}

type JSONLCountRequest struct {
	File string `json:"file" vd:"len($)>0"`
}

type JSONLCountResult struct {
	Lines int `json:"lines"`
}

type JSONLReadRequest struct {
	File      string `json:"file" vd:"len($)>0"`
	StartLine int    `json:"start_line" vd:"$>=0"`
	Count     *int   `json:"count"`
}

type JSONLReadResult struct {
	Lines []string `json:"lines"`
}

// JSONLTailRequest reads complete JSONL records immediately before a byte
// offset. BeforeOffset <= 0 means the current end of file. Unlike
// JSONLReadRequest this shape is suitable for reverse pagination because the
// local implementation does not need to scan every preceding line.
type JSONLTailRequest struct {
	File         string `json:"file" vd:"len($)>0"`
	BeforeOffset int64  `json:"before_offset,omitempty"`
	Count        int    `json:"count" vd:"$>0"`
}

type JSONLTailResult struct {
	Lines       []string `json:"lines"`
	LineOffsets []int64  `json:"line_offsets"`
	StartOffset int64    `json:"start_offset"`
	EndOffset   int64    `json:"end_offset"`
	FileSize    int64    `json:"file_size"`
	HasMore     bool     `json:"has_more"`
}

type JSONLAppendRequest struct {
	File       string   `json:"file" vd:"len($)>0"`
	JSONString []string `json:"json_string" vd:"len($)>0"`
}
