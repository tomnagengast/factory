package workflow

import (
	"strings"
	"testing"
)

func TestDefaultMarkdownIncludesLifecycleSafeguards(t *testing.T) {
	t.Parallel()

	markdown := DefaultMarkdown()
	for _, required := range []string{
		"The principal never merges",
		"exact verified",
		"Every Factory-authored Linear comment must end",
		"```text\n🐘\n```",
		"🐘 `codex-do:TEAM-123:phase:r1`",
		"Create a merge commit",
		"squash or rebase",
		`~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"`,
		"## Cross-repository authority",
		"FACTORY_REPOSITORIES",
		"that blocker class does not exist",
	} {
		if !strings.Contains(markdown, required) {
			t.Errorf("compiled default is missing %q", required)
		}
	}
	if strings.Contains(markdown, "bin/network-app") {
		t.Error("compiled default contains stale bin/network-app command")
	}
}
