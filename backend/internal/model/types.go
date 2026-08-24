package model

import "time"

type FileObject struct {
	ID           string    `json:"id,omitempty"`
	Key          string    `json:"key"`
	Name         string    `json:"name"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"last_modified"`
	ContentType  string    `json:"content_type"`
	ETag         string    `json:"etag"`
}

type FolderEntry struct {
	ID     string `json:"id,omitempty"`
	Prefix string `json:"prefix"`
	Name   string `json:"name"`
}

type Pagination struct {
	Offset     int  `json:"offset"`
	Limit      int  `json:"limit"`
	Returned   int  `json:"returned"`
	Total      int  `json:"total"`
	HasMore    bool `json:"has_more"`
	NextOffset *int `json:"next_offset,omitempty"`
}

type ListResponse struct {
	Prefix     string        `json:"prefix"`
	Folders    []FolderEntry `json:"folders"`
	Files      []FileObject  `json:"files"`
	Pagination Pagination    `json:"pagination"`
}

type PresignResponse struct {
	URL       string `json:"url"`
	Key       string `json:"key"`
	ExpiresIn string `json:"expires_in,omitempty"`
}

type SearchResponse struct {
	Query      string       `json:"query"`
	Prefix     string       `json:"prefix"`
	Results    []FileObject `json:"results"`
	Pagination Pagination   `json:"pagination"`
}
