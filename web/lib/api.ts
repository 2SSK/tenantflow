const API_URL = process.env.NEXT_PUBLIC_API_URL || "http://localhost:9090";

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
    const error = await response.json();
    throw new Error(`API error ${response.status}: ${error}`);
  }

  return response.json();
}
