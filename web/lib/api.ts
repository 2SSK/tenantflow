const API_URL = process.env.NEXT_PUBLIC_API_URL || "http://localhost:9090";

// Error that preserves the upstream HTTP status code so proxy routes can pass
// it through accurately (e.g. a 409 Conflict from the Go API stays a 409).
export class ApiFetchError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
    this.name = "ApiFetchError";
  }
}

export async function apiFetch<T>(
  path: string,
  accessToken: string,
  options: RequestInit = {},
): Promise<T> {
  const url = `${API_URL}${path}`;

  const response = await fetch(url, {
    ...options,
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${accessToken}`,
      ...options.headers,
    },
  });

  if (!response.ok) {
    // Extract a friendly message from the API's JSON error body (e.g.
    // {"error":"upgrade already in progress"}) instead of stringifying the
    // whole object into "[object Object]".
    let message = `API error ${response.status}`;
    try {
      const body = await response.json();
      if (body && typeof body.error === "string" && body.error.length > 0) {
        message = body.error;
      }
    } catch {
      // Non-JSON response body; fall back to the HTTP status text.
      message = `API error ${response.status}`;
    }
    throw new ApiFetchError(response.status, message);
  }

  return response.json();
}
