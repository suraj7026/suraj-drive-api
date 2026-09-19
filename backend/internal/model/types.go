package model

import "time"

type FileObject struct {
	ID           string     `json:"id,omitempty"`
	Key          string     `json:"key"`
	Name         string     `json:"name"`
	Size         int64      `json:"size"`
	LastModified time.Time  `json:"last_modified"`
	ContentType  string     `json:"content_type"`
	ETag         string     `json:"etag"`
	TrashedAt    *time.Time `json:"trashed_at,omitempty"`
	StarredAt    *time.Time `json:"starred_at,omitempty"`
	LastOpenedAt *time.Time `json:"last_opened_at,omitempty"`
	Shared       bool       `json:"shared,omitempty"`
}

type FolderEntry struct {
	ID           string     `json:"id,omitempty"`
	Prefix       string     `json:"prefix"`
	Name         string     `json:"name"`
	TrashedAt    *time.Time `json:"trashed_at,omitempty"`
	StarredAt    *time.Time `json:"starred_at,omitempty"`
	LastOpenedAt *time.Time `json:"last_opened_at,omitempty"`
	Shared       bool       `json:"shared,omitempty"`
	FolderColor  string     `json:"folder_color,omitempty"`
}

type ShortcutEntry struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	StarredAt    *time.Time `json:"starred_at,omitempty"`
	LastOpenedAt *time.Time `json:"last_opened_at,omitempty"`
	Shared       bool       `json:"shared,omitempty"`
}

type Pagination struct {
	Offset     int    `json:"offset,omitempty"`
	Limit      int    `json:"limit"`
	Returned   int    `json:"returned"`
	Total      int    `json:"total,omitempty"`
	HasMore    bool   `json:"has_more"`
	NextOffset *int   `json:"next_offset,omitempty"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type ListResponse struct {
	CurrentFolderID string          `json:"current_folder_id,omitempty"`
	Prefix          string          `json:"prefix"`
	Folders         []FolderEntry   `json:"folders"`
	Files           []FileObject    `json:"files"`
	Shortcuts       []ShortcutEntry `json:"shortcuts,omitempty"`
	Pagination      Pagination      `json:"pagination"`
}

type PresignResponse struct {
	URL       string `json:"url"`
	Key       string `json:"key,omitempty"`
	ExpiresIn string `json:"expires_in,omitempty"`
}

type PreviewResponse struct {
	Status     string `json:"status"`
	JobID      string `json:"job_id,omitempty"`
	URL        string `json:"url,omitempty"`
	Key        string `json:"key,omitempty"`
	ExpiresIn  string `json:"expires_in,omitempty"`
	RetryAfter int    `json:"retry_after,omitempty"`
}

type SearchResponse struct {
	Query      string          `json:"query"`
	Prefix     string          `json:"prefix"`
	Folders    []FolderEntry   `json:"folders,omitempty"`
	Results    []FileObject    `json:"results"`
	Shortcuts  []ShortcutEntry `json:"shortcuts,omitempty"`
	Pagination Pagination      `json:"pagination"`
}
