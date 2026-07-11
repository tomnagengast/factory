package linearhook

import (
	"regexp"
	"strings"
	"time"
)

var factoryMarker = regexp.MustCompile("^🐘 `codex-do:[A-Z][A-Z0-9]*-[1-9][0-9]*:[^`\\r\\n]+`$")

type Event struct {
	DeliveryID      string    `json:"deliveryId"`
	CommentID       string    `json:"commentId"`
	IssueID         string    `json:"issueId"`
	IssueIdentifier string    `json:"issueIdentifier,omitempty"`
	ParentID        string    `json:"parentId,omitempty"`
	URL             string    `json:"url,omitempty"`
	ReceivedAt      time.Time `json:"receivedAt"`
}

func FactoryAuthored(body string) bool {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		return line == "🐘" || factoryMarker.MatchString(line)
	}
	return false
}
