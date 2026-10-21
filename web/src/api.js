// API base URL. The API lives on the api.<host> subdomain of the domain the
// frontend is served from, so it is derived at runtime: one image works on any
// domain. Empty in dev so the Vite proxy routes /auth and the API calls.
export const API_BASE = import.meta.env.DEV ? "" : `${location.protocol}//api.${location.host}`

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
