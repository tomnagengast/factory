package workflow

import (
	_ "embed"
	"time"
)

//go:embed defaults/full-sdlc.md
var fullSDLCMarkdown string

func Default(now time.Time) Definition {
	return Definition{
		ID:        DefaultID,
		Revision:  1,
		Name:      "Full SDLC",
		Enabled:   true,
		Markdown:  CanonicalizeMarkdown(fullSDLCMarkdown),
		UpdatedAt: now.UTC(),
	}
}

func DefaultMarkdown() string { return CanonicalizeMarkdown(fullSDLCMarkdown) }
