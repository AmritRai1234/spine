// Cart identity: one stable cart per browser. The id travels with every
// cart event so the backend can upsert/delete the right lines without
// server-side sessions.

const KEY = "spine_cart_id"

export function getCartId(): string {
  let id = localStorage.getItem(KEY)
  if (!id) {
    id = "c_" + crypto.randomUUID().slice(0, 8)
    localStorage.setItem(KEY, id)
  }
  return id
}
