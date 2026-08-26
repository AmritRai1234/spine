import { useCallback, useEffect, useState, type FormEvent } from "react"
import { CreditCard, Percent, Save, Store } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { useSpineStateTick } from "@/hooks/use-spine"
import { toast } from "sonner"
import { adminClient } from "@/lib/admin"
import { fetchStoreSettings, saveSetting } from "@/lib/store"

/**
 * Store settings — KV rows in store_settings consumed across the app
 * (store name in the header, currency symbol in every price, low-stock
 * threshold on the dashboard).
 */
export default function AdminSettings() {
  const [storeName, setStoreName] = useState("")
  const [currency, setCurrency] = useState("")
  const [lowStock, setLowStock] = useState("")
  const [loaded, setLoaded] = useState(false)

  const [stripeKey, setStripeKey] = useState("")
  const [stripeHook, setStripeHook] = useState("")
  const [connectedLabel, setConnectedLabel] = useState("")

  const [couponCode, setCouponCode] = useState("")
  const [couponPercent, setCouponPercent] = useState("")

  const tick = useSpineStateTick("SETTING_SAVED")

  const load = useCallback(async () => {
    const s = await fetchStoreSettings()
    setStoreName(s.store_name)
    setCurrency(s.currency_symbol)
    setLowStock(String(s.low_stock_threshold))
    setLoaded(true)
  }, [])

  useEffect(() => {
    load()
  }, [load, tick])

  async function submit(e: FormEvent) {
    e.preventDefault()
    await saveSetting("store_name", storeName.trim())
    await saveSetting("currency_symbol", currency.trim() || "$")
    await saveSetting("low_stock_threshold", String(Number(lowStock) || 10))
    toast.success("Settings saved")
  }

  async function connectStripe(e: FormEvent) {
    e.preventDefault()
    const secret = stripeKey.trim()
    if (!secret.startsWith("sk_test_") && !secret.startsWith("sk_live_") && !secret.startsWith("rk_")) {
      toast.error("Enter a Stripe secret key (sk_test_… or sk_live_…)")
      return
    }
    const res = await adminClient().emit("STRIPE_CONNECT", {
      stripe_secret: secret,
      webhook_secret: stripeHook.trim(),
    })
    if (res.status === "ok") {
      const tail = secret.slice(-4)
      setConnectedLabel(`${secret.startsWith("sk_live") ? "live" : "test"} ••••${tail}`)
      setStripeKey("")
      setStripeHook("")
      toast.success("Stripe connected — checkout is live for this session")
    } else {
      toast.error(res.error ?? "Could not connect Stripe")
    }
  }

  async function disconnectStripe() {
    const res = await adminClient().emit("STRIPE_DISCONNECT", {})
    if (res.status === "ok") {
      setConnectedLabel("")
      toast.success("Stripe disconnected")
    } else {
      toast.error(res.error ?? "Disconnect failed")
    }
  }

  async function createCoupon(e: FormEvent) {
    e.preventDefault()
    const code = couponCode.trim().toUpperCase()
    if (!code) return
    const res = await adminClient().emit("CREATE_COUPON", {
      code,
      percent_off: Number(couponPercent) || 0,
      fixed_off: 0,
      active: "true",
      actor: "admin@panel",
    })
    if (res.status === "ok") {
      toast.success(`Coupon ${code} created (${couponPercent}% off)`)
      setCouponCode("")
      setCouponPercent("")
    } else {
      toast.error(res.error ?? "Coupon failed")
    }
  }

  return (
    <div className="animate-in fade-in slide-in-from-bottom-2 w-full space-y-4 duration-300">
      <div>
        <h2 className="text-xl font-semibold tracking-tight">Settings</h2>
        <p className="text-sm text-muted-foreground">Store-wide configuration (KV table)</p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <Store className="h-4 w-4" /> Store
          </CardTitle>
          <CardDescription>Applied live across storefront and admin.</CardDescription>
        </CardHeader>
        <CardContent>
          {loaded && (
            <form onSubmit={submit} className="space-y-3">
              <label className="block space-y-1.5">
                <span className="text-sm font-medium">Store name</span>
                <Input value={storeName} onChange={(e) => setStoreName(e.target.value)} placeholder="Spine Shop" />
              </label>
              <div className="grid grid-cols-2 gap-3">
                <label className="block space-y-1.5">
                  <span className="text-sm font-medium">Currency symbol</span>
                  <Input value={currency} onChange={(e) => setCurrency(e.target.value)} placeholder="$" maxLength={3} />
                </label>
                <label className="block space-y-1.5">
                  <span className="text-sm font-medium">Low-stock threshold</span>
                  <Input type="number" min="0" value={lowStock} onChange={(e) => setLowStock(e.target.value)} />
                </label>
              </div>
              <Button type="submit" className="w-full">
                <Save className="mr-2 h-4 w-4" /> Save settings
              </Button>
            </form>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <CreditCard className="h-4 w-4" /> Stripe payments
          </CardTitle>
          <CardDescription>
            Paste a secret key from dashboard.stripe.com → Developers → API keys to
            turn on checkout. Only a masked hint is stored — the key itself lives in
            engine memory for this session (env vars still take precedence).
          </CardDescription>
        </CardHeader>
        <CardContent>
          {connectedLabel ? (
            <div className="flex flex-wrap items-center gap-3">
              <Badge>connected · {connectedLabel}</Badge>
              <Button variant="outline" size="sm" onClick={disconnectStripe}>
                Disconnect
              </Button>
              <span className="text-xs text-muted-foreground">
                Card payments collect automatically at checkout.
              </span>
            </div>
          ) : (
            <form onSubmit={connectStripe} className="space-y-3">
              <label className="block space-y-1.5">
                <span className="text-sm font-medium">Secret key</span>
                <Input
                  type="password"
                  value={stripeKey}
                  onChange={(e) => setStripeKey(e.target.value)}
                  placeholder="sk_test_…"
                  autoComplete="off"
                  required
                />
              </label>
              <label className="block space-y-1.5">
                <span className="text-sm font-medium">
                  Webhook signing secret <span className="text-muted-foreground">(optional)</span>
                </span>
                <Input
                  type="password"
                  value={stripeHook}
                  onChange={(e) => setStripeHook(e.target.value)}
                  placeholder="whsec_…"
                  autoComplete="off"
                />
              </label>
              <Button type="submit" variant="outline" className="w-full">
                Connect Stripe
              </Button>
            </form>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <Percent className="h-4 w-4" /> Discount codes
          </CardTitle>
          <CardDescription>
            Codes upsert by name; shoppers validate them at checkout (server-side).
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={createCoupon} className="flex items-end gap-3">
            <label className="flex-1 space-y-1.5">
              <span className="text-sm font-medium">Code</span>
              <Input
                value={couponCode}
                onChange={(e) => setCouponCode(e.target.value)}
                placeholder="SUMMER20"
                className="uppercase"
                required
              />
            </label>
            <label className="w-32 space-y-1.5">
              <span className="text-sm font-medium">% off</span>
              <Input
                type="number"
                min="1"
                max="100"
                value={couponPercent}
                onChange={(e) => setCouponPercent(e.target.value)}
                required
              />
            </label>
            <Button type="submit" variant="outline">
              Create coupon
            </Button>
          </form>
          <p className="mt-3 flex items-center gap-2 text-xs text-muted-foreground">
            <Badge variant="outline">tip</Badge>
            Deactivate a code by creating it again later via the API with active=false.
          </p>
        </CardContent>
      </Card>
    </div>
  )
}
