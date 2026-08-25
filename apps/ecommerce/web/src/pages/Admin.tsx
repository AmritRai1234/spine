import { useCallback, useState, type FormEvent } from "react"
import { KeyRound } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { clearAdminKey, getBaseUrl, setAdminKey } from "@/lib/admin"
import type { AdminTab } from "@/pages/admin/tabs"
import AdminLayout from "@/pages/admin/AdminLayout"

type PanelRole = "admin" | "staff"

const ROLE_KEY = "spine_admin_role"

/**
 * Backend admin panel: gated by API key. The key's capabilities are probed
 * at unlock — keys that may not emit PUBLISH_PRODUCT enter staff mode
 * (fulfilment only), everything else gets the full panel.
 */
export default function Admin({ onStorefront }: { onStorefront?: () => void }) {
  const [unlocked, setUnlocked] = useState(!!localStorage.getItem("spine_admin_key"))
  const [role, setRole] = useState<PanelRole>(() => (localStorage.getItem(ROLE_KEY) as PanelRole) ?? "admin")
  const [tab, setTab] = useState<AdminTab>("dashboard")

  const lock = useCallback(() => {
    clearAdminKey()
    localStorage.removeItem(ROLE_KEY)
    setUnlocked(false)
  }, [])

  if (!unlocked) return <UnlockGate onUnlock={(r) => { setRole(r); setUnlocked(true) }} />

  return <AdminLayout tab={tab} onTab={setTab} onLock={lock} role={role} onStorefront={onStorefront} />
}

function UnlockGate({ onUnlock }: { onUnlock: (role: PanelRole) => void }) {
  const [key, setKeyValue] = useState("")
  const [error, setError] = useState<string | null>(null)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setAdminKey(key.trim())
    // Validate the key via a real authenticated probe: PUBLISH_PRODUCT with
    // an empty payload dies in validation (400) for a valid admin key, is
    // rejected by RLAC (403) for a valid staff key, and is refused (401) for
    // an invalid or missing key. (Table reads are open, so they can't
    // validate anything — the old queryTable check accepted ANY key.)
    let res: Response
    try {
      res = await fetch(`${getBaseUrl()}/emit`, {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-API-Key": key.trim() },
        body: JSON.stringify({ event: "PUBLISH_PRODUCT", payload: {} }),
      })
    } catch {
      clearAdminKey()
      setError("Cannot reach the server — check that the backend is running.")
      return
    }
    if (res.status === 401) {
      clearAdminKey()
      setError("Admin key rejected by server — copy the ADMIN_SECRET value from apps/ecommerce/.env")
      return
    }
    const detectedRole: PanelRole = res.status === 403 ? "staff" : "admin"
    localStorage.setItem(ROLE_KEY, detectedRole)
    onUnlock(detectedRole)
  }

  return (
    <Card className="animate-in fade-in zoom-in-95 mx-auto max-w-sm duration-300">
      <CardHeader>
        <div className="flex items-center gap-2">
          <KeyRound className="h-5 w-5" />
          <CardTitle>Admin access</CardTitle>
        </div>
        <CardDescription>
          Enter the admin key ($ADMIN_SECRET) to manage the store, or a staff
          key ($STAFF_SECRET) for fulfilment-only access.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit} className="space-y-3">
          <Input
            type="password"
            placeholder="sk_…"
            value={key}
            onChange={(e) => setKeyValue(e.target.value)}
            required
            autoFocus
          />
          {error && <p className="text-sm text-destructive">{error}</p>}
          <Button type="submit" className="w-full">Unlock panel</Button>
        </form>
      </CardContent>
    </Card>
  )
}
