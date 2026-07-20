import type { MediaUpload } from "./types";

const acceptedImageTypes = ["image/png", "image/jpeg", "image/gif", "image/webp"];
const acceptedVideoTypes = ["video/mp4", "video/webm", "video/quicktime"];
const imageTypes = new Set(acceptedImageTypes);
const videoTypes = new Set(acceptedVideoTypes);

export const mediaAccept = [...acceptedImageTypes, ...acceptedVideoTypes].join(",");

export type MediaKind = "image" | "video";

type MediaFileTransfer = {
  files?: ArrayLike<File> | Iterable<File>;
  items?: ArrayLike<Pick<DataTransferItem, "kind" | "getAsFile">>
    | Iterable<Pick<DataTransferItem, "kind" | "getAsFile">>;
};

export function mediaFiles(transfer?: MediaFileTransfer | null): File[] {
  if (!transfer) return [];
  const itemFiles = Array.from(transfer.items ?? [])
    .filter((item) => item.kind === "file")
    .map((item) => item.getAsFile())
    .filter((file): file is File => Boolean(file));
  return itemFiles.length ? itemFiles : Array.from(transfer.files ?? []);
}

export function mediaKind(contentType: string): MediaKind | undefined {
  if (imageTypes.has(contentType)) return "image";
  if (videoTypes.has(contentType)) return "video";
  return undefined;
}

export function mediaMarkup(media: Pick<MediaUpload, "name" | "contentType" | "url">): string {
  const kind = mediaKind(media.contentType);
  if (kind === "image") return `![${escapeMarkdownAlt(media.name)}](${media.url})`;
  if (kind === "video") {
    return `<video controls preload="metadata" src="${escapeHTML(media.url)}" title="${escapeHTML(media.name)}"></video>`;
  }
  throw new Error(`Unsupported media type: ${media.contentType || "unknown"}`);
}

export function insertMediaMarkup(
  value: string,
  selectionStart: number,
  selectionEnd: number,
  markups: string[],
): { value: string; caret: number } {
  const start = Math.max(0, Math.min(selectionStart, value.length));
  const end = Math.max(start, Math.min(selectionEnd, value.length));
  const insertion = markups.join("\n\n");
  return {
    value: value.slice(0, start) + insertion + value.slice(end),
    caret: start + insertion.length,
  };
}

function escapeMarkdownAlt(value: string): string {
  return value.replace(/[\r\n]+/g, " ").replace(/([\\\[\]])/g, "\\$1");
}

function escapeHTML(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}
