// Talking to the console's API.
//
// Reading (the live stream) needs no token. Every change is a POST and needs
// the shared access token in an Authorization header.

const TOKEN_KEY = "cluster-console-token";

// The token is kept in sessionStorage: it survives a page reload but is
// forgotten when the browser tab is closed.
export function loadToken() {
  return sessionStorage.getItem(TOKEN_KEY) || "";
}

export function saveToken(token) {
  if (token) {
    sessionStorage.setItem(TOKEN_KEY, token);
  } else {
    sessionStorage.removeItem(TOKEN_KEY);
  }
}

// post sends one change to the console and always resolves (never throws) to
//   { ok, status, message, error }
// so callers can simply show `message` or `error`.
export async function post(path, body, token) {
  let response;
  try {
    response = await fetch(path, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch {
    return { ok: false, status: 0, message: "", error: "Could not reach the console." };
  }

  let data = {};
  try {
    data = await response.json();
  } catch {
    // Not JSON (for example an error page from a proxy). Fall through.
  }
  return {
    ok: response.ok && data.ok !== false,
    status: response.status,
    message: data.message || "",
    error: data.error || (response.ok ? "" : `The console answered ${response.status}.`),
  };
}

// listFiles fetches the list of CSV files that can be downloaded.
export async function listFiles() {
  try {
    const response = await fetch("/api/files");
    const data = await response.json();
    return data.files || [];
  } catch {
    return [];
  }
}
