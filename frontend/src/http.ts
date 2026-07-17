export async function getJSON<T>(url: string, label: string): Promise<T> {
  const response = await fetch(url, {
    cache: "no-store",
    credentials: "same-origin",
  });
  if (!response.ok) {
    throw new Error(`${label} failed with ${response.status}`);
  }
  return response.json() as Promise<T>;
}

export class HTTPError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly body: unknown,
  ) {
    super(message);
  }
}

type SendJSONOptions = {
  method: string;
  body?: unknown;
  headers?: Record<string, string>;
};

export async function sendJSON<T>(
  url: string,
  label: string,
  options: SendJSONOptions,
): Promise<T> {
  const response = await fetch(url, {
    method: options.method,
    cache: "no-store",
    credentials: "same-origin",
    headers:
      options.body === undefined
        ? options.headers
        : { "Content-Type": "application/json", ...options.headers },
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
  });
  const text = await response.text();
  let body: unknown = text || undefined;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      // Preserve endpoint-specific plain-text errors.
    }
  }
  if (!response.ok) {
    const detail = typeof body === "string" ? body.trim() : "";
    throw new HTTPError(
      detail || `${label} failed with ${response.status}`,
      response.status,
      body,
    );
  }
  return body as T;
}
