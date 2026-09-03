/**
 * Shared email validation — strict format check used across every
 * customer-facing email input (newsletter, checkout, order tracking).
 *
 * Rejects obvious junk: missing TLD, spaces, double @, local-only,
 * and placeholder-style addresses like "test@" or "test@test".
 */
export const EMAIL_RE = /^[A-Za-z0-9._%+-]+@[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)+$/

/** Domain part must carry at least one dot of length ≥2 (blocks "test@test"). */
export function isValidEmail(value: string): boolean {
  const email = value.trim()
  if (!EMAIL_RE.test(email)) return false
  const domain = email.split("@")[1] ?? ""
  return domain.split(".").every((part) => part.length >= 2)
}