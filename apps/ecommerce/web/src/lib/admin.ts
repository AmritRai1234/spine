// Admin API client: a second SpineClient instance carrying the admin key.
// The key is entered at runtime (never bundled) and persisted in
// localStorage so the panel stays unlocked across reloads.

import { SpineClient } from "@/lib/spine"

const STORAGE_KEY = "spine_admin_key"

let baseUrl = import.meta.env.SPINE_URL ?? "http://localhost:8080"

/** Engine base URL the admin client talks to. */
export function getBaseUrl(): string {
  return baseUrl
}

export function getAdminKey(): string {
  return localStorage.getItem(STORAGE_KEY) ?? ""
}

export function setAdminKey(key: string) {
  localStorage.setItem(STORAGE_KEY, key)
}

export function clearAdminKey() {
  localStorage.removeItem(STORAGE_KEY)
}

let cached: { key: string; client: SpineClient } | null = null

/** Returns a SpineClient bound to the stored admin key. */
export function adminClient(): SpineClient {
  const key = getAdminKey()
  if (!cached || cached.key !== key) {
    cached = { key, client: new SpineClient(baseUrl, key) }
  }
  return cached.client
}
