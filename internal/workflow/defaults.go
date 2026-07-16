package workflow

import (
	_ "embed"
	"time"
)

//go:embed defaults/full-sdlc.md
var fullSDLCMarkdown string

//go:embed defaults/full-sdlc-provider-neutral.md
var providerNeutralMarkdown string

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

func ProviderNeutralDefault(now time.Time) Definition {
	return Definition{
		ID:        ProviderNeutralID,
		Revision:  1,
		Name:      "Full SDLC (provider neutral)",
		Enabled:   true,
		Markdown:  CanonicalizeMarkdown(providerNeutralMarkdown),
		UpdatedAt: now.UTC(),
	}
}

func ProviderNeutralMarkdown() string { return CanonicalizeMarkdown(providerNeutralMarkdown) }

func ProviderNeutralDigest() string {
	digest, err := Digest(ProviderNeutralDefault(time.Time{}))
	if err != nil {
		panic(err)
	}
	return digest
}
