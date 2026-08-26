import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react"
import { BadgeCheck, CheckCircle2, CreditCard, Loader2, MapPin, PackageSearch, XCircle } from "lucide-react"

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
import { spine } from "@/lib/spine"
import { CA_PROVINCES } from "@/lib/canada"
import { cityKeyFor, matchCity } from "@/lib/canada-cities"
import { getCartId } from "@/lib/cart"
import { money } from "@/lib/format"
import type { CartItemRow, OrderRow } from "@/types"

interface CheckoutProps {
  onTrackOrders: () => void
}

type Phase = "form" | "placing" | "confirmed"

interface AppliedCoupon {
  code: string
  percentOff: number
}

const EMAIL_KEY = "spine_shopper_email"

/**
 * Checkout — address capture, server-validated coupons, and order placement.
 * The engine re-checks every price, stock level and coupon server-side;
 * ORDER_CREATED arrives as a WebSocket push, not a poll.
 */
export default function Checkout({ onTrackOrders }: CheckoutProps) {
  const [email, setEmail] = useState(() => localStorage.getItem(EMAIL_KEY) ?? "")
  const [shipName, setShipName] = useState("")
  const [address1, setAddress1] = useState("")
  const [city, setCity] = useState("")
  const [country, setCountry] = useState("")
  const [province, setProvince] = useState("")
  const [cityMatched, setCityMatched] = useState(false)
  const [zip, setZip] = useState("")
  const [phase, setPhase] = useState<Phase>("form")
  const [order, setOrder] = useState<OrderRow | null>(null)
  const [error, setError] = useState<string | null>(null)
  const cartId = getCartId()

  // Coupon state — only engine-validated values are ever trusted here
  const [couponInput, setCouponInput] = useState("")
  const [couponChecking, setCouponChecking] = useState(false)
  const [appliedCoupon, setAppliedCoupon] = useState<AppliedCoupon | null>(null)
  const [couponError, setCouponError] = useState<string | null>(null)

  // Lines snapshot taken when checkout opened
  const [lines, setLines] = useState<CartItemRow[]>([])

  // Live shipping/tax estimate for the entered country (server rules; the
  // authoritative numbers are recomputed at PLACE_ORDER).
  const [estimate, setEstimate] = useState<{ shipping: number; taxRate: number } | null>(null)

  const createdTick = useSpineStateTick("ORDER_CREATED")

  useEffect(() => {
    ;(async () => {
      try {
        const res = await spine.queryTable("cart_items", {
          where: `cart_id:${cartId}`,
          limit: 100,
        })
        setLines((res.rows ?? []) as unknown as CartItemRow[])
      } catch {
        /* keep empty */
      }
    })()
  }, [cartId])

  // Fetch shipping + tax rules for the country so the shopper sees an
  // estimate before placing (missing rules → 0 / free).
  useEffect(() => {
    const c = country.trim().toUpperCase()
    if (!c) {
      setEstimate(null)
      return
    }
    let cancelled = false
    ;(async () => {
      try {
        // Mirror the server's lookup precedence exactly:
        //   region row (CA-XX) → country row → "*" default.
        // The "*" row keeps a default shipping price available even when no
        // country/province rule was configured — matching PLACE_ORDER.
        const base = c === "CA" && province ? `CA-${province}` : ""
        const regions = [base, c, "*"].filter(Boolean)
        const [zRes, tRes] = await Promise.all([
          spine.queryTable("shipping_zones", { limit: 500 }),
          spine.queryTable("tax_rules", { limit: 500 }),
        ])
        if (cancelled) return
        const zoneRows = (zRes.rows ?? []) as unknown as { country?: string; rate?: number }[]
        const taxRows = (tRes.rows ?? []) as unknown as { country?: string; rate?: number }[]
        const findRate = (rows: { country?: string; rate?: number }[]) => {
          for (const r of regions) {
            const hit = rows.find((row) => String(row.country ?? "") === r)
            if (hit) return Number(hit.rate ?? 0)
          }
          return 0
        }
        setEstimate({
          shipping: findRate(zoneRows),
          taxRate: findRate(taxRows),
        })
      } catch {
        /* keep previous estimate */
      }
    })()
    return () => {
      cancelled = true
    }
  }, [country, province])

  // Coupon results arrive as COUPON_VALIDATED / COUPON_REJECTED broadcasts,
  // keyed by cart_id so concurrent shoppers don't cross wires.
  useEffect(() => {
    const offValid = spine.onState("COUPON_VALIDATED", (payload) => {
      if (String(payload.cart_id ?? "") !== cartId) return
      setAppliedCoupon({ code: String(payload.coupon_code), percentOff: Number(payload.coupon_percent_off) })
      setCouponError(null)
      setCouponChecking(false)
    })
    const offRejected = spine.onState("COUPON_REJECTED", (payload) => {
      if (String(payload.cart_id ?? "") !== cartId) return
      setCouponChecking(false)
    })
    return () => {
      offValid()
      offRejected()
    }
  }, [cartId])

  const confirm = useCallback(async () => {
    try {
      const res = await spine.queryTable("orders", {
        where: `cart_id:${cartId}`,
        limit: 1,
      })
      const rows = (res.rows ?? []) as unknown as OrderRow[]
      if (rows.length > 0) {
        setOrder(rows[rows.length - 1])
        setPhase("confirmed")
      }
    } catch {
      // keep waiting for next broadcast
    }
  }, [cartId])

  useEffect(() => {
    if (phase === "placing") confirm()
  }, [createdTick, phase, confirm])

  const subtotal = useMemo(
    () => lines.reduce((sum, l) => sum + Number(l.price) * Number(l.qty), 0),
    [lines]
  )
  const discount = appliedCoupon ? (subtotal * appliedCoupon.percentOff) / 100 : 0
  const shippingEst = estimate?.shipping ?? 0
  // Tax applies to goods + shipping (mirrors the server's PLACE_ORDER math).
  const taxEst = ((subtotal + shippingEst) * (estimate?.taxRate ?? 0)) / 100
  // Pre-payment ESTIMATE only — the engine recomputes every dollar at
  // PLACE_ORDER and the confirmed view shows the authoritative total.
  const total = Math.max(0, subtotal - discount + shippingEst + taxEst)

  async function applyCoupon() {
    const code = couponInput.trim().toUpperCase()
    if (!code || couponChecking) return
    setCouponChecking(true)
    setCouponError(null)
    const res = await spine.emit("VALIDATE_COUPON", { cart_id: cartId, code })
    if (res.emitted_states?.includes("COUPON_REJECTED") || (res.status && res.status !== "ok")) {
      setCouponError(res.error ?? "Invalid code")
      setCouponChecking(false)
    }
    // Success path: the COUPON_VALIDATED subscription above flips state
  }

  async function placeOrder(e: FormEvent) {
    e.preventDefault()
    if (!email.trim()) return
    setError(null)
    setPhase("placing")
    localStorage.setItem(EMAIL_KEY, email.trim())

    // Order identity is generated client-side so line items can reference it
    const orderId = crypto.randomUUID()

    // 1) Capture line items FIRST — the server validates price + stock per
    //    line and persists them synchronously (the subtotal at PLACE_ORDER is
    //    computed from these rows, so they must exist before the order).
    let failed = false
    for (const line of lines) {
      const r = await spine.emit("ADD_ORDER_ITEM", {
        order_id: orderId,
        product_id: line.product_id,
        ...(line.variant_id ? { variant_id: line.variant_id, variant_label: line.variant_label ?? "" } : {}),
        name: line.name,
        price: Number(line.price),
        qty: Number(line.qty),
        purchase_mode: line.purchase_mode ?? "onetime",
        plan_id: line.plan_id ?? "",
      })
      if (r.status !== "ok") {
        failed = true
        setError(r.error ?? "An item could not be ordered (price or stock changed).")
        break
      }
    }
    if (failed) {
      setPhase("form")
      return
    }

    // 2) Place the order — totals (subtotal/shipping/tax/discount/total) are
    //    computed SERVER-SIDE; the client only sends facts. The country is
    //    normalized so zone/tax lookups match the seeded uppercase codes.
    const emitRes = await spine.emit("PLACE_ORDER", {
      cart_id: cartId,
      email: email.trim(),
      order_id: orderId,
      ship_name: shipName.trim(),
      address1: address1.trim(),
      city: city.trim(),
      country: country.trim().toUpperCase(),
      // Region code drives the shipping/tax lookup precedence (CA-ON → CA
      // → *). Canadian provinces send CA-XX; everyone else sends the country
      // (or "OTHER" which simply matches nothing and falls through).
      region: country === "CA" && province ? `CA-${province}` : country.trim().toUpperCase(),
      ...(country === "CA" && province ? { province: province.trim().toUpperCase() } : {}),
      // City key gives city-tier shipping (CA-ON-KINGSTON) when the city is
      // configured; unlisted cities send "" and fall back to province rate.
      city_key: country === "CA" && province && city ? cityKeyFor(`CA-${province}`, city) : "",
      zip: zip.trim(),
      ...(appliedCoupon ? { coupon_code: appliedCoupon.code } : {}),
    })
    if (emitRes.status !== "ok") {
      setError(emitRes.error ?? "Order failed")
      setPhase("form")
      return
    }

    // Clear the cart now that lines are captured and the order exists
    for (const line of lines) {
      await spine.emit("REMOVE_FROM_CART", {
        cart_id: cartId,
        product_id: line.product_id,
        variant_id: line.variant_id ?? "",
        purchase_mode: line.purchase_mode ?? "onetime",
      })
    }
    setLines([])
  }

  if (phase === "confirmed" && order) {
    return (
      <Card className="mx-auto max-w-md">
        <CardHeader>
          <div className="flex items-center gap-2">
            <CheckCircle2 className="h-6 w-6 text-green-600" />
            <CardTitle>Order confirmed</CardTitle>
          </div>
          <CardDescription>Pushed live over WebSocket.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-2 text-sm">
          <div className="flex justify-between">
            <span className="text-muted-foreground">Order ID</span>
            <code className="rounded bg-muted px-1">{order.id?.slice(0, 8)}</code>
          </div>
          <div className="flex justify-between">
            <span className="text-muted-foreground">Status</span>
            <Badge>{order.status}</Badge>
          </div>
          <div className="flex justify-between">
            <span className="text-muted-foreground">Confirmation sent to</span>
            <span>{order.email}</span>
          </div>
          {/* Authoritative server-computed totals */}
          {order.total !== undefined && (
            <div className="space-y-1 rounded-md border p-3">
              <div className="flex justify-between text-muted-foreground">
                <span>Subtotal</span>
                <span>{money(Number(order.subtotal))}</span>
              </div>
              <div className="flex justify-between text-muted-foreground">
                <span>Shipping</span>
                <span>{money(Number(order.shipping_cost))}</span>
              </div>
              <div className="flex justify-between text-muted-foreground">
                <span>Tax</span>
                <span>{money(Number(order.tax_amount))}</span>
              </div>
              {Number(order.coupon_discount) > 0 && (
                <div className="flex justify-between text-green-600">
                  <span>Coupon {order.coupon_code}</span>
                  <span>−{money(Number(order.coupon_discount))}</span>
                </div>
              )}
              <div className="flex justify-between border-t pt-1 font-semibold">
                <span>Total</span>
                <span>{money(Number(order.total))}</span>
              </div>
            </div>
          )}
          <Button variant="outline" className="mt-4 w-full" onClick={onTrackOrders}>
            <PackageSearch className="mr-2 h-4 w-4" />
            Track this order
          </Button>
        </CardContent>
      </Card>
    )
  }

  return (
    <Card className="mx-auto max-w-md">
      <CardHeader>
        <div className="flex items-center gap-2">
          <CreditCard className="h-6 w-6" />
          <CardTitle>Checkout</CardTitle>
        </div>
        <CardDescription>
          Cart {cartId} · {lines.length} item{lines.length === 1 ? "" : "s"}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={placeOrder} className="space-y-3">
          <Input
            type="email"
            placeholder="Email — you@example.com"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
          />
          <Input
            placeholder="Full name"
            value={shipName}
            onChange={(e) => setShipName(e.target.value)}
            required
          />
          <Input
            placeholder="Address"
            value={address1}
            onChange={(e) => setAddress1(e.target.value)}
            required
          />
          <div className="grid grid-cols-3 gap-3">
            <Input placeholder="City" value={city} onChange={(e) => { setCity(e.target.value); setCityMatched(false) }} required />
            <Input placeholder="ZIP / Postal code" value={zip} onChange={(e) => setZip(e.target.value)} required />
          </div>
          {country === "CA" && province && city && (
            (() => {
              const m = matchCity(`CA-${province}`, city)
              if (!m) {
                return (
                  <p className="text-xs text-muted-foreground">
                    {city} isn't in our city-rate list yet — you'll get the {CA_PROVINCES.find((p) => p.code === `CA-${province}`)?.name} rate.
                  </p>
                )
              }
              // Auto-select the matched city for exact-rate lookup
              if (!cityMatched) setCityMatched(true)
              return (
                <p className="flex items-center gap-1.5 text-xs text-emerald-600 dark:text-emerald-400">
                  <MapPin className="h-3.5 w-3.5" /> Local shipping rate available for {m.name}
                </p>
              )
            })()
          )}
          <div className="grid grid-cols-2 gap-3">
            <label className="space-y-1">
              <span className="text-xs text-muted-foreground">Country</span>
              <select
                value={country}
                onChange={(e) => { setCountry(e.target.value); setProvince("") }}
                className="h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm"
                required
              >
                <option value="" disabled>Select country…</option>
                <option value="CA">Canada</option>
                <option value="US">United States</option>
                <option value="GB">United Kingdom</option>
                <option value="AU">Australia</option>
                <option value="DE">Germany</option>
                <option value="OTHER">Other</option>
              </select>
            </label>
            {country === "CA" && (
              <label className="space-y-1">
                <span className="text-xs text-muted-foreground">Province / territory</span>
                <select
                  value={province}
                  onChange={(e) => setProvince(e.target.value)}
                  className="h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm"
                  required
                >
                  <option value="" disabled>Select province…</option>
                  {CA_PROVINCES.map((p) => (
                    <option key={p.code} value={p.code.replace("CA-", "")}>{p.name}</option>
                  ))}
                </select>
              </label>
            )}
            {country !== "CA" && country !== "" && (
              <label className="space-y-1">
                <span className="text-xs text-muted-foreground">State / region</span>
                <Input value={province} onChange={(e) => setProvince(e.target.value)} placeholder="Optional" />
              </label>
            )}
          </div>

          {couponError && (
            <p className="flex items-center gap-1.5 text-sm text-destructive">
              <XCircle className="h-4 w-4" /> {couponError}
            </p>
          )}

          {/* Coupon row */}
          <div className="flex items-center gap-2">
            <Input
              placeholder="Discount code"
              value={couponInput}
              onChange={(e) => {
                setCouponInput(e.target.value)
                setAppliedCoupon(null)
                setCouponError(null)
              }}
              disabled={!!appliedCoupon}
              className="uppercase placeholder:normal-case"
            />
            {appliedCoupon ? (
              <Badge variant="secondary" className="gap-1 whitespace-nowrap py-1.5">
                <BadgeCheck className="h-3.5 w-3.5 text-green-600" />
                −{appliedCoupon.percentOff}%
              </Badge>
            ) : (
              <Button type="button" variant="outline" onClick={applyCoupon} disabled={!couponInput.trim() || couponChecking}>
                {couponChecking ? <Loader2 className="h-4 w-4 animate-spin" /> : "Apply"}
              </Button>
            )}
          </div>

          {/* Totals — estimate until the engine confirms; the order row then
              carries the authoritative server-computed numbers */}
          <div className="space-y-1 rounded-md border p-3 text-sm">
            <div className="flex justify-between text-muted-foreground">
              <span>Subtotal</span>
              <span>{money(subtotal)}</span>
            </div>
            {appliedCoupon && (
              <div className="flex justify-between text-green-600">
                <span>Coupon {appliedCoupon.code}</span>
                <span>−{money(discount)}</span>
              </div>
            )}
            {estimate !== null && (
              <>
                <div className="flex justify-between text-muted-foreground">
                  <span>Shipping{estimate.shipping === 0 ? " (free)" : ""}</span>
                  <span>{money(shippingEst)}</span>
                </div>
                <div className="flex justify-between text-muted-foreground">
                  <span>Tax ({estimate.taxRate}%)</span>
                  <span>{money(taxEst)}</span>
                </div>
              </>
            )}
            <div className="flex justify-between border-t pt-1 font-semibold">
              <span>Total{estimate !== null ? " (est.)" : ""}</span>
              <span>{money(total)}</span>
            </div>
          </div>

          {error && (
            <p className="flex items-start gap-1.5 text-sm text-destructive">
              <XCircle className="mt-0.5 h-4 w-4 shrink-0" /> {error}
            </p>
          )}
          <Button type="submit" className="w-full" disabled={phase === "placing"}>
            {phase === "placing" && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
            {phase === "placing" ? "Placing order…" : `Place order — ${money(total)}`}
          </Button>
        </form>
      </CardContent>
    </Card>
  )
}
