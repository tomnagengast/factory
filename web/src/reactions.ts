export function reactionEmojisText(emojis: string[]) {
  return emojis.join("\n");
}

export function parseReactionEmojis(value: string) {
  return value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
}

export function reactionOptions(configured: string[], active: string[]) {
  const options = [...configured];
  for (const emoji of active) {
    if (!options.includes(emoji)) options.push(emoji);
  }
  return options;
}
