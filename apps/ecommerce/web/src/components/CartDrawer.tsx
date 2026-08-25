import { useCallback, useEffect, useState } from "react"
import { Minus, Plus, ShoppingCart, Trash2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { useSpineStateTick } from "@/hooks/use-spine"
import { spine } from "@/lib/spine"
import { getCartId } from "@/lib/cart"
import { money } from "@/lib/format"
import type { CartItemRow } from "@/types"

interface CartDrawerProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCheckout: () => void
}

/**
 * Cart drawer — the Shopify-style quick cart. Slides in from the right on
 * add-to-cart, updates live via CART_UPDATED broadcasts, and drives the
 * same engine events as the full cart page (UPDATE_CART_ITEM /
 * REMOVE_FROM_CART).
 */
export default function CartDrawer({ open, onOpenChange, onCheckout }: CartDrawerProps) {
  const [items, setItems] = useState<CartItemRow[]>([])
  const cartId = getCartId()
  const tick = useSpineStateTick("CART_UPDATED")

  const load = useCallback(async () => {
    try {
      const res = await spine.queryTable("cart_items", {
        where: `cart_id:${cartId}`,
        limit: 100,
      })
      setItems((res.rows ?? []) as unknown as CartItemRow[])
    } catch {
      /* keep last */
    }
  }, [cartId])

  useEffect(() => {
    if (open) load()
  }, [load, open, tick])

  async function setQty(item: CartItemRow, qty: number) {
    if (qty < 1) return
    await spine.emit("UPDATE_CART_ITEM", {
      cart_id: cartId,
      product_id: item.product_id,
      variant_id: item.variant_id ?? "",
      qty,
    })
  }

  async function remove(item: CartItemRow) {
    await spine.emit("REMOVE_FROM_CART", {
      cart_id: cartId,
      product_id: item.product_id,
      variant_id: item.variant_id ?? "",
    })
  }

  const subtotal = items.reduce((s, i) => s + Number(i.price) * Number(i.qty), 0)

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex w-full flex-col sm:max-w-md">
        <SheetHeader>
          <SheetTitle className="flex items-center gap-2">
            <ShoppingCart className="h-4 w-4" /> Your cart
          </SheetTitle>
          <SheetDescription>
            {items.length} item{items.length === 1 ? "" : "s"} · updates live over WebSocket
          </SheetDescription>
        </SheetHeader>

        <div className="flex-1 space-y-2 overflow-y-auto px-4">
          {items.length === 0 ? (
            <div className="flex h-full flex-col items-center justify-center gap-3 py-12 text-center">
              <ShoppingCart className="h-8 w-8 text-muted-foreground" />
              <p className="text-sm text-muted-foreground">Your cart is empty.</p>
              <Button variant="outline" size="sm" onClick={() => onOpenChange(false)}>
                Keep browsing
              </Button>
            </div>
          ) : (
            items.map((item) => (
              <div key={item.line_id} className="flex items-center justify-between gap-3 rounded-md border px-3 py-2">
                <div className="min-w-0">
                  <p className="truncate text-sm font-medium">{item.name}</p>
                  {item.variant_label && (
                    <p className="text-xs text-muted-foreground">{item.variant_label}</p>
                  )}
                  <p className="text-xs text-muted-foreground">{money(Number(item.price))} each</p>
                </div>
                <div className="flex shrink-0 items-center gap-1">
                  <Button
                    variant="outline"
                    size="icon"
                    className="h-7 w-7"
                    disabled={Number(item.qty) <= 1}
                    onClick={() => setQty(item, Number(item.qty) - 1)}
                  >
                    <Minus className="h-3 w-3" />
                  </Button>
                  <span className="w-8 text-center text-sm tabular-nums">{item.qty}</span>
                  <Button variant="outline" size="icon" className="h-7 w-7" onClick={() => setQty(item, Number(item.qty) + 1)}>
                    <Plus className="h-3 w-3" />
                  </Button>
                  <Button variant="ghost" size="icon" className="h-7 w-7 text-destructive" onClick={() => remove(item)}>
                    <Trash2 className="h-3.5 w-3.5" />
                  </Button>
                </div>
              </div>
            ))
          )}
        </div>

        {items.length > 0 && (
          <SheetFooter>
            <div className="flex items-center justify-between text-sm">
              <span className="text-muted-foreground">Subtotal</span>
              <span className="text-lg font-bold">{money(subtotal)}</span>
            </div>
            <Button className="w-full" onClick={() => { onOpenChange(false); onCheckout() }}>
              Checkout
            </Button>
          </SheetFooter>
        )}
      </SheetContent>
    </Sheet>
  )
}
