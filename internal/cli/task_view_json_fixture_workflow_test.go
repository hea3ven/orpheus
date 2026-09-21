//go:build integration

package cli_test

import "time"

// These independent DTOs describe the JSON protocol asserted by command workflows.
type taskViewJSONRepository struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	TaskIDPrefix string `json:"task_id_prefix,omitempty"`
}

type taskViewJSONDetail struct {
	Kind      string   `json:"kind"`
	URL       string   `json:"url,omitempty"`
	ID        string   `json:"id,omitempty"`
	IDs       []string `json:"ids,omitempty"`
	Attempt   int      `json:"attempt,omitempty"`
	State     string   `json:"state,omitempty"`
	Step      string   `json:"step,omitempty"`
	Count     int      `json:"count,omitempty"`
	Source    string   `json:"source,omitempty"`
	Operation string   `json:"operation,omitempty"`
	Message   string   `json:"message,omitempty"`
}

type taskViewJSONEpicProgress struct {
	Completed int `json:"completed"`
	Total     int `json:"total"`
}

type taskViewJSONTaskEntry struct {
	Kind         string                    `json:"kind"`
	Repository   taskViewJSONRepository    `json:"repository"`
	ID           string                    `json:"id"`
	Title        string                    `json:"title"`
	IssueType    string                    `json:"issue_type"`
	Priority     int                       `json:"priority"`
	Status       string                    `json:"status"`
	Detail       taskViewJSONDetail        `json:"detail"`
	EpicProgress *taskViewJSONEpicProgress `json:"epic_progress,omitempty"`
	CreatedAt    *time.Time                `json:"created_at"`
	UpdatedAt    *time.Time                `json:"updated_at"`
}

type taskViewJSONRepoFailureEntry struct {
	Kind       string                 `json:"kind"`
	Repository taskViewJSONRepository `json:"repository"`
	Status     string                 `json:"status"`
	Detail     taskViewJSONDetail     `json:"detail"`
}
