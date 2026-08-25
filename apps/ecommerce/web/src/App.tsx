import { useCallback, useEffect, useState, type FormEvent } from "react"
import { Menu, Moon, PackageSearch, Radio, ShoppingCart, Store, Sun, WifiOff } from "lucide-react"
import { Toaster, toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import CartDrawer from "@/components/CartDrawer"
import { useSpineStateTick } from "@/hooks/use-spine"
import { spine } from "@/lib/spine"
import { getCartId } from "@/lib/cart"
import { useStoreSettings } from "@/lib/store"
import Catalog from "@/pages/Catalog"
import Checkout from "@/pages/Checkout"
import ProductDetail from "@/pages/ProductDetail"
import MyOrders from "@/pages/MyOrders"
import Admin from "@/pages/Admin"

type View = "catalog" | "product" | "checkout" | "orders" | "admin"

function useDarkMode(): [boolean, () => void] {
  const [dark, setDark] = useState(() => localStorage.getItem("spine_theme") === "dark")
  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark)
    localStorage.setItem("spine_theme", dark ? "dark" : "light")
  }, [dark])
  return [dark, () => setDark((d) => !d)]
}

export default function App() {
  // Minimal hash routing: #/admin = full-screen admin (never linked from the
  // storefront — real shops keep the back-office URL-only), anything else =
  // storefront. Back/forward buttons work via hashchange.
  const [view, setViewState] = useState<View>(() =>
    window.location.hash === "#/admin" ? "admin" : "catalog"
  )
  const [productId, setProductId] = useState<string | null>(null)
  const [connected, setConnected] = useState(false)
  const [cartCount, setCartCount] = useState(0)
  const cartId = getCartId()
  const [dark, toggleDark] = useDarkMode()
  const settings = useStoreSettings()
  const [newsletterEmail, setNewsletterEmail] = useState("")
  const [cartOpen, setCartOpen] = useState(false)
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false)

  // Live connection badge (WS auto-reconnects under the hood)
  useEffect(() => spine.onConnectionChange(setConnected), [])

  // Keep the URL in sync with the view; keep hashchange driving the view so
  // browser back/forward and manual URL entry both work.
  const setView = useCallback((v: View) => {
    setViewState(v)
    if (v === "admin") {
      window.location.hash = "#/admin"
    } else if (window.location.hash === "#/admin") {
      window.location.hash = "#/"
    }
  }, [])

  useEffect(() => {
    const onHash = () => {
      const isAdmin = window.location.hash === "#/admin"
      setViewState((prev) => (isAdmin ? "admin" : prev === "admin" ? "catalog" : prev))
    }
    window.addEventListener("hashchange", onHash)
    return () => window.removeEventListener("hashchange", onHash)
  }, [])

  function joinNewsletter(e: FormEvent) {
    e.preventDefault()
    if (!newsletterEmail.trim()) return
    toast.success("You're on the list — first drops land soon.")
    setNewsletterEmail("")
  }

  // Cart badge refreshes on every CART_UPDATED broadcast
  const cartTick = useSpineStateTick("CART_UPDATED")
  const refreshCount = useCallback(async () => {
    try {
      const res = await spine.queryTable("cart_items", {
        where: `cart_id:${cartId}`,
        limit: 100,
      })
      const rows = res.rows ?? []
      setCartCount(rows.reduce((sum, r) => sum + Number(r.qty ?? 0), 0))
    } catch {
      // server unreachable; keep last count
    }
  }, [cartId])

  useEffect(() => {
    refreshCount()
  }, [refreshCount, cartTick])

  function openProduct(id: string) {
    setProductId(id)
    setView("product")
  }

  function addToCartAndOpenDrawer() {
    refreshCount()
    setCartOpen(true)
  }

  // Full-screen admin: the panel is a standalone app — no storefront chrome
  // (announcement bar, header, footer) and no width cap around it.
  if (view === "admin") {
    return (
      <div className="min-h-screen bg-background">
        <Admin onStorefront={() => setView("catalog")} />
        <Toaster position="bottom-right" richColors closeButton />
      </div>
    )
  }

  return (
    <div className="min-h-screen bg-background">
      {/* Announcement bar — standard storefront top strip */}
      <div className="bg-foreground px-4 py-1.5 text-center text-xs font-medium text-background">
        Shipping calculated at checkout · Use{" "}
        <span className="font-semibold underline underline-offset-2">WELCOME15</span> for 15% off your first order
      </div>

      <header className="sticky top-0 z-10 border-b bg-background/95 backdrop-blur">
        <div className="flex w-full items-center justify-between gap-4 px-4 py-3">
          <div className="flex items-center gap-2">
            {/* Burger — mobile navigation */}
            <Button
              variant="ghost"
              size="icon"
              className="h-8 w-8 md:hidden"
              onClick={() => setMobileMenuOpen(true)}
              title="Menu"
            >
              <Menu className="h-4 w-4" />
            </Button>
            <span
              className="cursor-pointer text-lg font-bold tracking-tight"
              onClick={() => setView("catalog")}
            >
              {settings.store_name}
            </span>
          </div>

          <nav className="hidden items-center gap-1 md:flex">
            <Button
              variant={view === "catalog" || view === "product" ? "secondary" : "ghost"}
              size="sm"
              onClick={() => setView("catalog")}
            >
              <Store className="mr-1 h-4 w-4" />
              Catalog
            </Button>
            <Button
              variant={view === "orders" ? "secondary" : "ghost"}
              size="sm"
              onClick={() => setView("orders")}
            >
              <PackageSearch className="mr-1 h-4 w-4" />
              My Orders
            </Button>
          </nav>

          <div className="flex items-center gap-2">
            <Button variant="ghost" size="icon" className="h-8 w-8" onClick={toggleDark} title="Toggle theme">
              {dark ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
            </Button>
            <Button variant="outline" size="sm" onClick={() => setCartOpen(true)} title="Open cart">
              <ShoppingCart className="mr-1 h-4 w-4" />
              Cart
              {cartCount > 0 && (
                <Badge className="ml-2 h-5 px-1.5" variant="default">
                  {cartCount}
                </Badge>
              )}
            </Button>
          </div>
        </div>
      </header>

      <main className="flex min-h-[calc(100vh-3.5rem)] w-full flex-col px-4 py-8">
        {view === "catalog" && (
          <>
            {/* Hero — image-backed, standard storefront opener */}
            <section className="relative mb-10 overflow-hidden rounded-2xl border">
              <img
                src="https://placehold.co/1600x520/111827/ffffff?text=Spine+Shop"
                alt=""
                className="absolute inset-0 h-full w-full object-cover"
              />
              <div className="absolute inset-0 bg-foreground/60" />
              <div className="relative mx-auto max-w-2xl px-6 py-16 text-center text-background sm:py-24">
                <p className="text-xs font-semibold uppercase tracking-[0.2em] text-background/70">
                  New season · event-driven merch
                </p>
                <h1 className="mt-3 text-3xl font-bold tracking-tight sm:text-5xl">{settings.store_name}</h1>
                <p className="mx-auto mt-3 max-w-xl text-sm text-background/80 sm:text-base">
                  Every order is an event. Browse, buy, and track your order live — from checkout to doorstep.
                </p>
                <div className="mt-7 flex items-center justify-center gap-3">
                  <Button size="lg" onClick={() => document.getElementById("catalog-grid")?.scrollIntoView({ behavior: "smooth" })}>
                    Shop now
                  </Button>
                  <Button
                    size="lg"
                    variant="outline"
                    className="border-background/40 bg-transparent text-background hover:bg-background/10 hover:text-background"
                    onClick={() => setView("orders")}
                  >
                    Track an order
                  </Button>
                </div>
              </div>
            </section>
            <Catalog
              onAddToCart={addToCartAndOpenDrawer}
              onViewProduct={(p) => openProduct(p.id)}
            />
          </>
        )}
        {view === "product" && productId && (
          <ProductDetail
            productId={productId}
            onBack={() => setView("catalog")}
            onAddToCart={addToCartAndOpenDrawer}
          />
        )}
        {view === "checkout" && <Checkout onTrackOrders={() => setView("orders")} />}
        {view === "orders" && <MyOrders />}
      </main>

      {/* Full-width footer — Shopify-style columns + newsletter */}
      <footer className="border-t bg-muted/40">
        <div className="grid w-full gap-8 px-4 py-10 sm:grid-cols-2 lg:grid-cols-4">
          <div>
            <p className="font-semibold">{settings.store_name}</p>
            <p className="mt-2 text-sm text-muted-foreground">
              Event-driven e-commerce demo built on Spine.
            </p>
            <div className="mt-3">
              <Badge variant={connected ? "secondary" : "destructive"} className="gap-1">
                {connected ? <Radio className="h-3 w-3" /> : <WifiOff className="h-3 w-3" />}
                {connected ? "live" : "offline"}
              </Badge>
            </div>
          </div>
          <div>
            <p className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">Shop</p>
            <ul className="mt-3 space-y-2 text-sm">
              <li><button className="hover:text-foreground" onClick={() => setView("catalog")}>All products</button></li>
              <li><button className="hover:text-foreground" onClick={() => setView("orders")}>Track an order</button></li>
            </ul>
          </div>
          <div>
            <p className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">Company</p>
            <ul className="mt-3 space-y-2 text-sm">
              <li>
                <button
                  className="hover:text-foreground"
                  onClick={() => toast("hello@spine.dev — demo store, no real orders.")}
                >
                  Contact
                </button>
              </li>
            </ul>
          </div>
          <div>
            <p className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">Newsletter</p>
            <p className="mt-3 text-sm text-muted-foreground">New drops, straight to your inbox.</p>
            <form onSubmit={joinNewsletter} className="mt-3 flex gap-2">
              <Input
                type="email"
                required
                placeholder="you@example.com"
                className="bg-background"
                value={newsletterEmail}
                onChange={(e) => setNewsletterEmail(e.target.value)}
              />
              <Button type="submit" variant="outline">Join</Button>
            </form>
          </div>
        </div>
        <div className="border-t">
          <div className="flex w-full flex-col items-center justify-between gap-2 px-4 py-4 text-xs text-muted-foreground sm:flex-row">
            <p>© {new Date().getFullYear()} {settings.store_name}. Demo store — no real orders.</p>
            <p>Built on <span className="font-medium text-foreground">Spine</span> — event-driven backend</p>
          </div>
        </div>
      </footer>

      {/* Live cart drawer — the functional quick-cart */}
      <CartDrawer
        open={cartOpen}
        onOpenChange={setCartOpen}
        onCheckout={() => setView("checkout")}
      />

      {/* Mobile navigation menu */}
      <Sheet open={mobileMenuOpen} onOpenChange={setMobileMenuOpen}>
        <SheetContent side="left" className="flex w-72 flex-col">
          <SheetHeader>
            <SheetTitle>{settings.store_name}</SheetTitle>
            <SheetDescription>Menu</SheetDescription>
          </SheetHeader>
          <div className="flex-1 space-y-1 px-4">
            <Button
              variant="ghost"
              className="w-full justify-start"
              onClick={() => { setMobileMenuOpen(false); setView("catalog") }}
            >
              <Store className="mr-2 h-4 w-4" /> Catalog
            </Button>
            <Button
              variant="ghost"
              className="w-full justify-start"
              onClick={() => { setMobileMenuOpen(false); setView("orders") }}
            >
              <PackageSearch className="mr-2 h-4 w-4" /> My Orders
            </Button>
            <Button
              variant="ghost"
              className="w-full justify-start"
              onClick={() => { setMobileMenuOpen(false); setCartOpen(true) }}
            >
              <ShoppingCart className="mr-2 h-4 w-4" /> Cart
              {cartCount > 0 && (
                <Badge className="ml-2 h-5 px-1.5" variant="default">{cartCount}</Badge>
              )}
            </Button>
            <Button variant="ghost" className="w-full justify-start" onClick={toggleDark}>
              {dark ? <Sun className="mr-2 h-4 w-4" /> : <Moon className="mr-2 h-4 w-4" />}
              {dark ? "Light mode" : "Dark mode"}
            </Button>
          </div>
          <div className="px-4 pb-4">
            <p className="flex items-center gap-2 text-xs text-muted-foreground">
              {connected ? <Radio className="h-3 w-3" /> : <WifiOff className="h-3 w-3" />}
              {connected ? "live" : "offline"}
            </p>
          </div>
        </SheetContent>
      </Sheet>

      <Toaster position="bottom-right" richColors closeButton />
    </div>
  )
}
