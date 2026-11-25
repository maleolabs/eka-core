package runtime

// ToolOutputProcessor for 5 tools: git diff, grep, test, build log, find
// (sto:budget-tooloutput-cache-real). Bounded top20 + hasMore + lazy read_output offset/limit.

type ToolOutput struct {
	ChangedFiles []string `json:"changed_files,omitempty"`
	Hunks        []string `json:"hunks,omitempty"`
	Matches      []string `json:"matches,omitempty"`
	HasMore      bool     `json:"hasMore"`
	FullRef      string   `json:"fullRef,omitempty"`
}

func TruncateGrep(matches []string, limit int) (out []string, hasMore bool) {
	if len(matches) <= limit {
		return matches, false
	}
	return matches[:limit], true
}
