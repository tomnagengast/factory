import { describe, expect, test } from "bun:test";
import { insertMediaMarkup, mediaKind, mediaMarkup } from "./media";

describe("mediaKind", () => {
  test.each(["image/png", "image/jpeg", "image/gif", "image/webp"])("classifies %s as an image", (type) => {
    expect(mediaKind(type)).toBe("image");
  });

  test.each(["video/mp4", "video/webm", "video/quicktime"])("classifies %s as video", (type) => {
    expect(mediaKind(type)).toBe("video");
  });

  test("rejects other MIME types", () => {
    expect(mediaKind("image/svg+xml")).toBeUndefined();
    expect(() => mediaMarkup({ name: "bad.svg", contentType: "image/svg+xml", url: "/api/media/1" })).toThrow();
  });
});

describe("mediaMarkup", () => {
  test("creates escaped image Markdown", () => {
    expect(mediaMarkup({
      name: "a [draft]\\crop\n.png", contentType: "image/png", url: "/api/media/7",
    })).toBe("![a \\[draft\\]\\\\crop .png](/api/media/7)");
  });

  test("creates escaped video HTML", () => {
    expect(mediaMarkup({
      name: 'clip "one" & <two>.mp4', contentType: "video/mp4", url: "/api/media/8?x=1&y=2",
    })).toBe('<video controls preload="metadata" src="/api/media/8?x=1&amp;y=2" title="clip &quot;one&quot; &amp; &lt;two&gt;.mp4"></video>');
  });
});

describe("insertMediaMarkup", () => {
  test.each([
    ["start", 0, 0, "[media]body"],
    ["middle", 2, 2, "bo[media]dy"],
    ["end", 4, 4, "body[media]"],
    ["selection", 1, 3, "b[media]y"],
  ])("inserts at the %s", (_name, start, end, expected) => {
    const result = insertMediaMarkup("body", start as number, end as number, ["[media]"]);
    expect(result.value).toBe(expected);
    expect(result.caret).toBe((start as number) + 7);
  });

  test("keeps multiple files in order with blank lines", () => {
    expect(insertMediaMarkup("before after", 7, 7, ["first", "second", "third"])).toEqual({
      value: "before first\n\nsecond\n\nthirdafter",
      caret: 27,
    });
  });
});
