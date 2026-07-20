import { describe, expect, test } from "bun:test";
import { renderMarkdown } from "./markdown";

const newTabAttributes = 'target="_blank" rel="noreferrer"';

describe("renderMarkdown links", () => {
  test.each([
    [
      "issue comment inline",
      "PR: [#43](https://github.com/tomnagengast/factory/pull/43)",
      true,
      'PR: <a href="https://github.com/tomnagengast/factory/pull/43" target="_blank" rel="noreferrer">#43</a>',
    ],
    [
      "issue comment block",
      "PR: [#43](https://github.com/tomnagengast/factory/pull/43)",
      false,
      '<p>PR: <a href="https://github.com/tomnagengast/factory/pull/43" target="_blank" rel="noreferrer">#43</a></p>\n',
    ],
    [
      "reference link",
      "See [the pull request][pr].\n\n[pr]: https://github.com/tomnagengast/factory/pull/43",
      false,
      '<p>See <a href="https://github.com/tomnagengast/factory/pull/43" target="_blank" rel="noreferrer">the pull request</a>.</p>\n',
    ],
    [
      "angle-bracket autolink",
      "<https://example.com/review>",
      true,
      '<a href="https://example.com/review" target="_blank" rel="noreferrer">https://example.com/review</a>',
    ],
    [
      "GFM bare URL",
      "https://example.com/review",
      true,
      '<a href="https://example.com/review" target="_blank" rel="noreferrer">https://example.com/review</a>',
    ],
    [
      "relative Factory URL",
      "[task](/tasks/3681)",
      true,
      '<a href="/tasks/3681" target="_blank" rel="noreferrer">task</a>',
    ],
    [
      "fragment URL",
      "[details](#details)",
      true,
      '<a href="#details" target="_blank" rel="noreferrer">details</a>',
    ],
    [
      "title and formatted label",
      '[Review **A & B**](https://example.com/review "Open & inspect")',
      true,
      '<a href="https://example.com/review" title="Open &amp; inspect" target="_blank" rel="noreferrer">Review <strong>A &amp; B</strong></a>',
    ],
  ])("renders %s in a new tab", (_name, source, inline, expected) => {
    expect(renderMarkdown(source, inline)).toBe(expected);
  });

  test("decorates every Markdown link in surrounding content", () => {
    const html = renderMarkdown("Before [one](/one), **between**, and [two](#two).", true);

    expect(html).toBe(
      `Before <a href="/one" ${newTabAttributes}>one</a>, <strong>between</strong>, and <a href="#two" ${newTabAttributes}>two</a>.`,
    );
  });
});

describe("renderMarkdown boundaries", () => {
  test("preserves trusted raw HTML anchor attributes", () => {
    expect(renderMarkdown('<a href="/authored" target="_self">Authored</a>', true)).toBe(
      '<a href="/authored" target="_self">Authored</a>',
    );
  });

  test("changes only Markdown anchors when raw HTML and Markdown are mixed", () => {
    expect(renderMarkdown('<a href="/authored">Authored</a> and [generated](/generated)', true)).toBe(
      `<a href="/authored">Authored</a> and <a href="/generated" ${newTabAttributes}>generated</a>`,
    );
  });

  test("retains non-link Markdown and line-break behavior", () => {
    expect(renderMarkdown("**Bold**  \n`code`", false)).toBe(
      "<p><strong>Bold</strong><br><code>code</code></p>\n",
    );
  });
});
