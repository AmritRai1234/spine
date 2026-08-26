import { Suspense, lazy } from "react"
import {
  BarChart3,
  Boxes,
  LayoutDashboard,
  Lock,
  Mail,
  PackageSearch,
  Repeat,
  ScrollText,
  Settings,
  ShoppingCart,
  Store,
  Truck,
  Users,
} from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useStoreMetrics, useStoreSettings } from "@/lib/store"
import { money } from "@/lib/format"
import type { AdminTab } from "@/pages/admin/tabs"

// Code-split admin pages — recharts & co. never reach the storefront bundle.
const Dashboard = lazy(() => import("@/pages/admin/Dashboard"))
const AdminProducts = lazy(() => import("@/pages/admin/AdminProducts"))
const AdminInventory = lazy(() => import("@/pages/admin/AdminInventory"))
const AdminOrders = lazy(() => import("@/pages/admin/AdminOrders"))
const AdminCustomers = lazy(() => import("@/pages/admin/AdminCustomers"))
const AdminMarketing = lazy(() => import("@/pages/admin/AdminMarketing"))
const AdminSubscriptions = lazy(() => import("@/pages/admin/AdminSubscriptions"))
const AdminAnalytics = lazy(() => import("@/pages/admin/AdminAnalytics"))
const AdminEvents = lazy(() => import("@/pages/admin/AdminEvents"))
const AdminSettings = lazy(() => import("@/pages/admin/AdminSettings"))
const AdminShippingTax = lazy(() => import("@/pages/admin/AdminShippingTax"))

interface NavItem {
  id: AdminTab
  label: string
  icon: React.ReactNode
  adminOnly?: boolean
  /** Sub-item rendered indented under its parent (visual only until wired). */
  sub?: boolean
  disabled?: boolean
}
interface NavGroup {
  title: string
  items: NavItem[]
}

// Shopify-style grouped navigation.
const NAV_GROUPS: NavGroup[] = [
  {
    title: "Overview",
    items: [{ id: "dashboard", label: "Dashboard", icon: <LayoutDashboard className="h-4 w-4" />, adminOnly: true }],
  },
  {
    title: "Sales",
    items: [
      { id: "orders", label: "Orders", icon: <ShoppingCart className="h-4 w-4" /> },
      { id: "customers", label: "Customers", icon: <Users className="h-4 w-4" />, adminOnly: true },
      { id: "analytics", label: "Analytics", icon: <BarChart3 className="h-4 w-4" />, adminOnly: true },
    ],
  },
  {
    title: "Products",
    items: [
      { id: "products", label: "Products", icon: <Boxes className="h-4 w-4" />, adminOnly: true },
      { id: "inventory", label: "Inventory", icon: <PackageSearch className="h-4 w-4" />, adminOnly: true, sub: true },
    ],
  },
  {
    title: "Commerce",
    items: [
      { id: "shipping", label: "Shipping & Tax", icon: <Truck className="h-4 w-4" />, adminOnly: true },
      { id: "subscriptions", label: "Subscriptions", icon: <Repeat className="h-4 w-4" />, adminOnly: true },
      { id: "marketing", label: "Marketing", icon: <Mail className="h-4 w-4" />, adminOnly: true },
    ],
  },
  {
    title: "Settings",
    items: [
      { id: "settings", label: "Settings", icon: <Settings className="h-4 w-4" />, adminOnly: true },
      { id: "events", label: "Event Log", icon: <ScrollText className="h-4 w-4" />, adminOnly: true },
    ],
  },
]

interface AdminLayoutProps {
  tab: AdminTab
  onTab: (t: AdminTab) => void
  onLock: () => void
  role?: "admin" | "staff"
  onStorefront?: () => void
}

export default function AdminLayout({ tab, onTab, onLock, role = "admin", onStorefront }: AdminLayoutProps) {
  // Shared revenue pipeline (same hook powers the Dashboard KPIs)
  const metrics = useStoreMetrics()
  const settings = useStoreSettings()

  const groups = NAV_GROUPS.map((g) => ({
    ...g,
    items: g.items.filter((i) => !(role === "staff" && i.adminOnly)),
  })).filter((g) => g.items.length > 0)

  const flat = groups.flatMap((g) => g.items)
  const activeTab = flat.some((i) => i.id === tab) ? tab : "orders"

  const itemClass = (active: boolean) =>
    active
      ? "flex w-full items-center gap-3 rounded-md px-3 py-2 text-sm font-medium bg-white text-black"
      : "flex w-full items-center gap-3 rounded-md px-3 py-2 text-sm font-medium text-zinc-400 hover:bg-white/10 hover:text-white transition-colors"

  return (
    <div className="flex min-h-screen">
      {/* Shopify-style dark sidebar */}
      <aside className="hidden w-56 shrink-0 bg-[#1c1c1c] text-zinc-300 md:block">
        <nav className="sticky top-0 max-h-screen space-y-5 overflow-y-auto p-3">
          {groups.map((group) => (
            <div key={group.title}>
              <p className="px-3 pb-1.5 text-[11px] font-semibold uppercase tracking-wider text-zinc-500">
                {group.title}
              </p>
              <div className="space-y-0.5">
                {group.items.map((item, idx) => {
                  // Shopify-style sub-items: indented to align with the parent's
                  // LABEL (icon is 16px + gap-3 12px = 28px), no icon, lighter
                  // and slightly smaller text.
                  if (item.sub) {
                    return (
                      <button
                        key={`${item.label}-${idx}`}
                        onClick={item.disabled ? undefined : () => onTab(item.id)}
                        disabled={item.disabled}
                        className={
                          item.disabled
                            ? "flex w-full cursor-not-allowed items-center gap-3 rounded-md px-3 py-1.5 text-[13px] text-zinc-600"
                            : activeTab === item.id
                              ? "flex w-full items-center gap-3 rounded-md px-3 py-1.5 text-[13px] font-medium bg-white text-black"
                              : "flex w-full items-center gap-3 rounded-md px-3 py-1.5 text-[13px] text-zinc-400 hover:bg-white/10 hover:text-white transition-colors"
                        }
                      >
                        {item.icon}
                        {item.label}
                      </button>
                    )
                  }
                  return (
                    <button
                      key={item.id}
                      onClick={() => onTab(item.id)}
                      className={itemClass(activeTab === item.id)}
                    >
                      {item.icon}
                      {item.label}
                      {item.id === "orders" && (
                        <Badge variant="secondary" className="ml-auto h-5 bg-white/15 px-1.5 text-xs text-zinc-200">
                          live
                        </Badge>
                      )}
                    </button>
                  )
                })}
              </div>
            </div>
          ))}

          {/* Revenue + session controls pinned to the bottom */}
          <div className="space-y-2">
            <div className="my-2 border-t border-white/10" />
            <div className="rounded-md border border-white/10 bg-white/5 p-3">
              <p className="text-xs font-medium text-zinc-400">Gross revenue</p>
              <p className="mt-1 text-xl font-bold tracking-tight text-white">{money(metrics.revenue)}</p>
              <p className="mt-1 animate-pulse text-[11px] text-zinc-500">updates in real time</p>
            </div>
            {onStorefront && (
              <Button
                variant="ghost"
                size="sm"
                className="w-full justify-start text-zinc-300 hover:bg-white/10 hover:text-white"
                onClick={onStorefront}
              >
                <Store className="h-4 w-4" />
                View storefront
              </Button>
            )}
            {role === "staff" && (
              <Badge variant="secondary" className="ml-1 bg-white/15 text-zinc-200">staff mode</Badge>
            )}
            <Button
              variant="ghost"
              size="sm"
              className="w-full justify-start text-zinc-400 hover:bg-white/10 hover:text-white"
              onClick={onLock}
            >
              <Lock className="h-4 w-4" />
              Lock panel
            </Button>
          </div>
        </nav>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        {/* Topbar */}
        <div className="flex items-center justify-between gap-3 border-b px-6 py-3">
          <div className="flex items-center gap-2 text-sm">
            <img src="/logo.png" alt="" className="h-5 w-5 invert dark:invert-0" />
            <span className="font-medium">{settings.store_name}</span>
            <Badge variant="outline" className="text-muted-foreground">
              {role === "staff" ? "staff" : "admin"}
            </Badge>
          </div>
          <p className="hidden text-xs text-muted-foreground sm:block">Spine Commerce — admin</p>
        </div>

        {/* Content — flex column so pages can stretch to fill the viewport */}
        <main key={activeTab} className="animate-in fade-in slide-in-from-bottom-2 flex min-w-0 flex-1 flex-col p-6 pb-20 duration-300 md:pb-6">
          <Suspense fallback={<Skeleton className="h-96" />}>
            {activeTab === "dashboard" && <Dashboard />}
            {activeTab === "products" && <AdminProducts />}
            {activeTab === "inventory" && <AdminInventory />}
            {activeTab === "orders" && <AdminOrders />}
            {activeTab === "customers" && <AdminCustomers />}
            {activeTab === "marketing" && <AdminMarketing />}
            {activeTab === "subscriptions" && <AdminSubscriptions />}
            {activeTab === "analytics" && <AdminAnalytics />}
            {activeTab === "settings" && <AdminSettings />}
            {activeTab === "shipping" && <AdminShippingTax />}
            {activeTab === "events" && <AdminEvents />}
          </Suspense>
        </main>
      </div>

      {/* Mobile nav */}
      <div className="fixed inset-x-0 bottom-0 z-40 flex border-t bg-background md:hidden">
        {flat.map((item) => (
          <button
            key={item.id}
            onClick={() => onTab(item.id)}
            className={`flex flex-1 flex-col items-center gap-1 py-2 text-[10px] ${
              activeTab === item.id ? "text-primary" : "text-muted-foreground"
            }`}
          >
            {item.icon}
            {item.label}
          </button>
        ))}
      </div>
    </div>
  )
}
