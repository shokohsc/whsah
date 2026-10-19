// API base URL. In production this is the api subdomain (set at build time via
// VITE_API_BASE); empty in dev so the Vite proxy routes /auth and the API calls.
export const API_BASE = import.meta.env.VITE_API_BASE || ""

// api performs an authenticated JSON request. It attaches the stored OIDC ID
// token and throws with the HTTP status on failure.
export async function api(path, opts = {}) {
  const headers = { ...opts.headers }
  const token = localStorage.getItem("token")
  if (token) headers.Authorization = "Bearer " + token
  const res = await fetch(API_BASE + path, { ...opts, headers })
  if (!res.ok) {
    const err = new Error((await res.text()) || res.statusText)
    err.status = res.status
    throw err
  }
  return res.status === 204 ? null : res.json()
}
