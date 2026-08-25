// Store-wide settings + shared metrics hooks.
// Settings live in the store_settings KV table (admin-editable); metrics are
// computed once here so Dashboard and AdminLayout share one revenue pipeline.

import { useEffect, useState } from "react"

import { adminClient } from "@/lib/admin"
import { dayOf, setCurrencySymbol } from "@/lib/format"
import { spine } from "@/lib/spine"
import { useSpineStateTick } from "@/hooks/use-spine"
import type { OrderItemRow, OrderRow } from "@/types"

export interface StoreSettings {
  store_name: string
  currency_symbol: string
  low_stock_threshold: number
}

const DEFAULTS: StoreSettings = {
  store_name: "Spine Shop",
  currency_symbol: "$",
  low_stock_threshold: 10,
}

/** Fetch settings rows → typed map. Safe to call with any key (read-only API). */
export async function fetchStoreSettings(): Promise<StoreSettings> {
  try {
    const res = await spine.queryTable("store_settings", { limit: 100 })
    const map: Record<string, string> = {}
    for (const row of res.rows ?? []) {
      map[String(row.key)] = String(row.value ?? "")
    }
    return {
      store_name: map.store_name || DEFAULTS.store_name,
      currency_symbol: map.currency_symbol || DEFAULTS.currency_symbol,
      low_stock_threshold: Number(map.low_stock_threshold ?? DEFAULTS.low_stock_threshold) || DEFAULTS.low_stock_threshold,
    }
  } catch {
    return DEFAULTS
  }
}

/**
 * Live store settings: refetches on SETTING_SAVED broadcasts and pushes the
 * currency symbol into the shared formatter.
 */
export function useStoreSettings(): StoreSettings {
  const [settings, setSettings] = useState<StoreSettings>(DEFAULTS)
  const tick = useSpineStateTick("SETTING_SAVED")

  useEffect(() => {
    ;(async () => {
      const s = await fetchStoreSettings()
      setCurrencySymbol(s.currency_symbol)
      setSettings(s)
    })()
  }, [tick])

  return settings
}

export interface StoreMetrics {
  loading: boolean
  revenue: number
  orderCount: number
  pendingCount: number
}

/**
 * Pure sales math shared by every admin surface: gross revenue net of
 * cancellations and coupon discounts, plus per-day totals for charting.
 *
 * Phase 6: line totals and coupon discounts are SERVER-computed dollars
 * (order.line_total, order.coupon_discount) — the old client-side
 * percent-based math is gone. Old rows without line_total fall back to
 * price × qty; coupon_discount is subtracted once per order.
 */
export function summarizeSales(orders: OrderRow[], items: OrderItemRow[]) {
  const orderById = new Map(orders.map((o) => [o.id, o]))
  const byDay = new Map<string, number>()
  let gross = 0
  for (const it of items) {
    const order = orderById.get(it.order_id)
    if (!order || order.status === "cancelled") continue
    const lineGross = Number(it.line_total ?? Number(it.price) * Number(it.qty))
    gross += lineGross
    const day = dayOf(order.created_at)
    if (day) byDay.set(day, (byDay.get(day) ?? 0) + lineGross)
  }
  // Server-computed coupon discounts are per-ORDER dollars.
  for (const o of orders) {
    if (o.status === "cancelled") continue
    const discount = Number(o.coupon_discount ?? 0)
    if (discount > 0) {
      gross -= discount
      const day = dayOf(o.created_at)
      if (day) byDay.set(day, (byDay.get(day) ?? 0) - discount)
    }
  }
  return { revenue: gross, series: [...byDay.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([day, total]) => ({ day, total })) }
}

/**
 * Live store metrics — one pipeline for AdminLayout's revenue chip and any
 * other surface that needs headline numbers. Refetches whenever an order is
 * created or its status changes (live, over WebSocket).
 */
export function useStoreMetrics(): StoreMetrics {
  const [metrics, setMetrics] = useState<StoreMetrics>({ loading: true, revenue: 0, orderCount: 0, pendingCount: 0 })

  const createdTick = useSpineStateTick("ORDER_CREATED")
  const statusTick = useSpineStateTick("ORDER_STATUS_CHANGED")

  useEffect(() => {
    ;(async () => {
      try {
        const [oRes, iRes] = await Promise.all([
          spine.queryTable("orders", { limit: 500 }),
          spine.queryTable("order_items", { limit: 1000 }),
        ])
        const orders = (oRes.rows ?? []) as unknown as OrderRow[]
        const items = (iRes.rows ?? []) as unknown as OrderItemRow[]
        const { revenue } = summarizeSales(orders, items)
        setMetrics({
          loading: false,
          revenue,
          orderCount: orders.length,
          pendingCount: orders.filter((o) => o.status === "pending").length,
        })
      } catch {
        /* keep last */
      }
    })()
  }, [createdTick, statusTick])

  return metrics
}

/** Persist one setting (admin). */
export async function saveSetting(key: string, value: string) {
  return adminClient().emit("SAVE_SETTING", { key, value, actor: "admin@panel" })
}
