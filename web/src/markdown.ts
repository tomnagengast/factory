import { marked, Renderer } from "marked";

function newTabRenderer() {
  const renderer = new Renderer();
  const renderLink = renderer.link.bind(renderer);
  renderer.link = (token) => {
    const link = renderLink(token);
    if (!link.startsWith("<a ")) return link;
    return link.replace(">", ' target="_blank" rel="noreferrer">');
  };
  return renderer;
}

export function renderMarkdown(content: string, inline = false) {
  const options = {
    async: false as const,
    gfm: true,
    breaks: true,
    renderer: newTabRenderer(),
  };
  return inline
    ? marked.parseInline(content, options)
    : marked.parse(content, options);
}
