// Shared domain types for the Spine storefront.
// These mirror the .spine manifest contracts and SQLite row shapes.

export interface Product {
  id: string
  sku: string
  name: string
  price: number
  stock: number
  description: string
  image_url: string
  category: string
  created_at: string
  /** Additional images. Stored as a JSON column; may come back as a JSON
   * string (queryTable) or an array (in-memory). Use parseGallery(). */
  gallery?: string[] | string
}

export interface ProductVariantRow {
  id: string
  product_id: string
  sku: string
  option1_name?: string
  option1_value?: string
  option2_name?: string
  option2_value?: string
  price: number
  stock: number
  created_at?: string
}

export interface CartItemRow {
  line_id: string
  cart_id: string
  product_id: string
  name: string
  price: number
  qty: number
  updated_at: string
  // Variants (optional — plain products carry no variant)
  variant_id?: string
  variant_label?: string
}

export interface OrderRow {
  id: string
  cart_id: string
  email: string
  status: string
  created_at: string
  updated_at?: string
  // Phase 3 — address + coupon columns (schema evolution)
  ship_name?: string
  address1?: string
  city?: string
  country?: string
  zip?: string
  coupon_code?: string
  // Phase 6 — server-calculated totals (dollars, never client-supplied)
  coupon_discount?: number
  subtotal?: number
  shipping_cost?: number
  tax_amount?: number
  total?: number
}

// Payments ledger row — refunds carry a NEGATIVE amount so net received is
// always SUM(amount) per order.
export interface PaymentRow {
  id: string
  order_id: string
  amount: number
  kind: string // "payment" | "refund"
  status: string
  provider?: string
  reference?: string
  currency?: string
  created_at: string
}

export interface OrderItemRow {
  id: string
  order_id: string
  product_id: string
  name: string
  price: number
  qty: number
  // Phase 6 — server-computed line total (price × qty)
  line_total?: number
  // Variants
  variant_id?: string
  variant_label?: string
}

export interface CouponRow {
  code: string
  percent_off: number
  fixed_off: number
  active: string
  created_at?: string
}

export const ORDER_STATUSES = ["pending", "paid", "shipped", "delivered", "cancelled"] as const
export type OrderStatus = (typeof ORDER_STATUSES)[number]

export function statusColor(status: string): "default" | "secondary" | "destructive" | "outline" {
  switch (status) {
    case "pending": return "outline"
    case "paid": return "secondary"
    case "shipped": return "default"
    case "delivered": return "default"
    case "cancelled": return "destructive"
    default: return "outline"
  }
}
