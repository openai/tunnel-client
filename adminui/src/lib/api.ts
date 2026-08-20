export interface TextResponse {
  ok: boolean;
  status: number;
  text: string;
}

export async function fetchJSONWithInit<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, { cache: "no-store", ...init });
  if (!res.ok) {
    throw new Error(`${res.status} ${res.statusText}`);
  }
  return (await res.json()) as T;
}

export async function fetchJSON<T>(path: string): Promise<T> {
  return fetchJSONWithInit<T>(path);
}

export async function fetchTextResponse(path: string): Promise<TextResponse> {
  const res = await fetch(path, { cache: "no-store" });
  return {
    ok: res.ok,
    status: res.status,
    text: await res.text(),
  };
}

export async function fetchText(path: string): Promise<string> {
  const res = await fetchTextResponse(path);
  if (!res.ok) {
    throw new Error(`${res.status}`);
  }
  return res.text;
}

export async function postJSON<T>(path: string, body: unknown): Promise<T> {
  return fetchJSONWithInit<T>(path, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  });
}
