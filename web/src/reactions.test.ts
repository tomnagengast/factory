import { describe, expect, test } from "bun:test";
import { parseReactionEmojis, reactionEmojisText, reactionOptions } from "./reactions";

describe("reaction emoji settings", () => {
  test("renders saved order as lines", () => {
    expect(reactionEmojisText(["🎉", "👍🏻", "🧑🏽‍💻"])).toBe("🎉\n👍🏻\n🧑🏽‍💻");
  });

  test("trims and removes blank lines while preserving order and duplicates", () => {
    expect(parseReactionEmojis("  🎉  \r\n\r\n👍🏻\n🎉\n  plain text  ")).toEqual([
      "🎉", "👍🏻", "🎉", "plain text",
    ]);
  });

  test("puts configured choices before active retired values", () => {
    expect(reactionOptions(["🎉", "👍"], ["👀", "🎉", "🤔", "👀"])).toEqual([
      "🎉", "👍", "👀", "🤔",
    ]);
  });
});
