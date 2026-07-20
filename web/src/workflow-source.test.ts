import { describe, expect, test } from "bun:test";
import { highlightWorkflowSource, workflowSourcePlaceholder } from "./workflow-source";

describe("highlightWorkflowSource", () => {
  test("highlights plain JavaScript tokens", () => {
    const source = `// Build the answer.
export function answer(label) {
  if (label === "factory") return 42;
}`;

    const result = highlightWorkflowSource(source);

    expect(result.value).toContain('<span class="hljs-comment">// Build the answer.</span>');
    expect(result.value).toContain('<span class="hljs-keyword">export</span>');
    expect(result.value).toContain('<span class="hljs-string">&quot;factory&quot;</span>');
    expect(result.value).toContain('<span class="hljs-number">42</span>');
    expect(result.value).toContain('class="hljs-title function_"');
  });

  test("retains exact source while escaping rendered HTML", () => {
    const source = "  const markup = '<span>&</span>';\n\n";

    const result = highlightWorkflowSource(source);

    expect(result.code).toBe(source);
    expect(result.value).not.toContain("<span>&</span>");
    expect(result.value).toContain("&lt;span&gt;&amp;&lt;/span&gt;");
  });

  test("accepts incomplete source observed during an authoring write", () => {
    const source = "export const meta = {\n  name: 'unfinished";

    expect(() => highlightWorkflowSource(source)).not.toThrow();
    expect(highlightWorkflowSource(source).code).toBe(source);
  });

  test("highlights the waiting placeholder as a JavaScript comment", () => {
    const result = highlightWorkflowSource("");

    expect(result.code).toBe(workflowSourcePlaceholder);
    expect(result.value).toBe(`<span class="hljs-comment">${workflowSourcePlaceholder}</span>`);
  });

  test("returns only the latest source and token markup", () => {
    const first = highlightWorkflowSource("const version = 'old';");
    const secondSource = "const version = 'new';\n// refreshed";
    const second = highlightWorkflowSource(secondSource);

    expect(first.value).toContain("old");
    expect(second.code).toBe(secondSource);
    expect(second.value).toContain('<span class="hljs-string">&#x27;new&#x27;</span>');
    expect(second.value).toContain('<span class="hljs-comment">// refreshed</span>');
    expect(second.value).not.toContain("old");
  });
});
