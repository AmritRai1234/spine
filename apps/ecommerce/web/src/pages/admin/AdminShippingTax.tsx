import { useCallback, useEffect, useMemo, useState } from "react"
import { AlertTriangle, Globe2, Landmark, Percent, Save, Truck, Wand2 } from "lucide-react"
import { toast } from "sonner"

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
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { useSpineStateTick } from "@/hooks/use-spine"
import { adminClient } from "@/lib/admin"
import { money } from "@/lib/format"
import { CA_PROVINCES, OTHER_REGIONS, PROVINCE_TIER, SHIP_TIERS, type ShipTier } from "@/lib/canada"
import { CA_CITIES } from "@/lib/canada-cities"

interface RateRow {
  country: string
  rate: number
  created_at?: string
}

/**
 * Shipping & Tax — Canada-first configuration.
 *
 * Every Canadian province gets its official sales-tax rate (2025, verified
 * against Canada.ca and TaxTips.ca) and a Canada Post-tiered shipping rate,
 * picked from dropdowns. The checkout engine resolves the shopper's rate by
 * region code (CA-ON) → country code (US) → "*" default, so the same tables
 * still serve international orders.
 */
export default function AdminShippingTax() {
  const [zones, setZones] = useState<RateRow[] | null>(null)
  const [taxes, setTaxes] = useState<RateRow[] | null>(null)

  // Canada tax form
  const [taxProv, setTaxProv] = useState("CA-ON")
  const [taxRate, setTaxRate] = useState("")
  // Canada shipping form — province + per-city draft rates
  const [shipProv, setShipProv] = useState("CA-ON")
  const [provRateDraft, setProvRateDraft] = useState("")
  const [cityRateDrafts, setCityRateDrafts] = useState<Record<string, string>>({})
  // International forms
  const [intlZoneRegion, setIntlZoneRegion] = useState("US")
  const [intlZoneRate, setIntlZoneRate] = useState("")
  const [intlTaxRegion, setIntlTaxRegion] = useState("US")
  const [intlTaxRate, setIntlTaxRate] = useState("")
  const [bulkBusy, setBulkBusy] = useState(false)

  const zoneTick = useSpineStateTick("SHIPPING_ZONE_SAVED")
  const taxTick = useSpineStateTick("TAX_RULE_SAVED")
  const [loadError, setLoadError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoadError(null)
    try {
      const client = adminClient()
      const [z, t] = await Promise.all([
        client.queryTable("shipping_zones", { limit: 100 }),
        client.queryTable("tax_rules", { limit: 100 }),
      ])
      setZones((z.rows ?? []) as unknown as RateRow[])
      setTaxes((t.rows ?? []) as unknown as RateRow[])
    } catch (e) {
      // Keep prior data (if any) but surface why the tables are empty/stale.
      setLoadError(e instanceof Error ? e.message : "failed to load rates")
    }
  }, [])

  useEffect(() => {
    load()
  }, [load, zoneTick, taxTick])

  const zoneMap = useMemo(() => {
    const m = new Map<string, number>()
    for (const z of zones ?? []) m.set(z.country, Number(z.rate))
    return m
  }, [zones])

  const taxMap = useMemo(() => {
    const m = new Map<string, number>()
    for (const t of taxes ?? []) m.set(t.country, Number(t.rate))
    return m
  }, [taxes])

  const selectedTaxProv = CA_PROVINCES.find((p) => p.code === taxProv)
  const selectedShipProv = CA_PROVINCES.find((p) => p.code === shipProv)

  async function saveZone(region: string, rate: number, label: string) {
    const res = await adminClient().emit("SAVE_SHIPPING_ZONE", {
      country: region,
      rate,
      actor: "admin@panel",
    })
    if (res.status === "ok") {
      toast.success(`Shipping — ${label}: ${money(rate)}`)
    } else {
      toast.error(res.error ?? "Zone save failed")
    }
  }

  async function saveTax(region: string, rate: number, label: string) {
    const res = await adminClient().emit("SAVE_TAX_RULE", {
      country: region,
      rate,
      actor: "admin@panel",
    })
    if (res.status === "ok") {
      toast.success(`Tax — ${label}: ${rate}%`)
    } else {
      toast.error(res.error ?? "Tax save failed")
    }
  }

  /** One-click: every province gets its official 2025 sales-tax rate. */
  async function applyOfficialTaxRates() {
    setBulkBusy(true)
    try {
      for (const p of CA_PROVINCES) {
        const res = await adminClient().emit("SAVE_TAX_RULE", {
          country: p.code,
          rate: p.rate,
          actor: "admin@panel",
        })
        if (res.status !== "ok") {
          toast.error(`${p.name}: ${res.error ?? "save failed"}`)
          return
        }
      }
      toast.success(`Applied official 2025 rates to all ${CA_PROVINCES.length} provinces/territories`)
    } finally {
      setBulkBusy(false)
    }
  }

  /** One-click: Canada Post-tier shipping per province. */
  async function applySuggestedShipping() {
    setBulkBusy(true)
    try {
      for (const p of CA_PROVINCES) {
        const tier: ShipTier = PROVINCE_TIER[p.code] ?? "national"
        const rate = SHIP_TIERS[tier].suggestion
        const res = await adminClient().emit("SAVE_SHIPPING_ZONE", {
          country: p.code,
          rate,
          actor: "admin@panel",
        })
        if (res.status !== "ok") {
          toast.error(`${p.name}: ${res.error ?? "save failed"}`)
          return
        }
      }
      toast.success(`Applied Canada Post-tier shipping to all ${CA_PROVINCES.length} provinces/territories`)
    } finally {
      setBulkBusy(false)
    }
  }

  const missingTax = CA_PROVINCES.filter((p) => !taxMap.has(p.code))
  const missingZone = CA_PROVINCES.filter((p) => !zoneMap.has(p.code))

  return (
    <div className="animate-in fade-in slide-in-from-bottom-2 w-full space-y-4 duration-300">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-xl font-semibold tracking-tight">Shipping &amp; Tax</h2>
          <p className="max-w-2xl text-sm text-muted-foreground">
            Canada-first configuration. Shoppers are charged by province — the engine resolves
            their region code (e.g. <code className="rounded bg-muted px-1">CA-ON</code>) → country →{" "}
            <code className="rounded bg-muted px-1">*</code> default, server-side at checkout.
          </p>
        </div>
        <div className="flex gap-2">
          <Button size="sm" onClick={applyOfficialTaxRates} disabled={bulkBusy}>
            <Wand2 className="mr-1 h-4 w-4" /> Apply official 2025 tax rates
          </Button>
          <Button size="sm" variant="outline" onClick={applySuggestedShipping} disabled={bulkBusy}>
            <Truck className="mr-1 h-4 w-4" /> Apply suggested shipping
          </Button>
        </div>
      </div>

      {(loadError || missingTax.length > 0 || missingZone.length > 0) && (
        <Card className={loadError ? "border-destructive/40 bg-destructive/5" : "border-amber-500/40 bg-amber-500/5"}>
          <CardContent className="flex flex-wrap items-center gap-2 py-3 text-sm">
            {loadError ? (
              <>
                <AlertTriangle className="h-4 w-4 text-destructive" />
                <span>
                  Couldn't load rates: <strong>{loadError}</strong>. Your admin key may be stale —{" "}
                  <button className="underline" onClick={() => { location.hash = "#/admin"; location.reload() }}>
                    unlock again
                  </button>{" "}
                  or check that the backend is running.
                </span>
                <Button size="sm" variant="outline" className="ml-auto" onClick={load}>Retry</Button>
              </>
            ) : (
              <>
                <AlertTriangle className="h-4 w-4 text-amber-500" />
                <span>
                  {missingTax.length > 0 && (
                    <>
                      <strong>{missingTax.length}</strong> province(s) missing tax rates (
                      {missingTax.slice(0, 4).map((p) => p.code.replace("CA-", "")).join(", ")}
                      {missingTax.length > 4 ? "…" : ""})
                    </>
                  )}
                  {missingTax.length > 0 && missingZone.length > 0 && " · "}
                  {missingZone.length > 0 && (
                    <>
                      <strong>{missingZone.length}</strong> missing shipping zones
                    </>
                  )}
                  {" — these regions get 0% tax / free shipping until configured."}
                </span>
              </>
            )}
          </CardContent>
        </Card>
      )}

      {/* ── Canada — Tax ─────────────────────────────────────────────── */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <Percent className="h-4 w-4" /> Canadian sales tax by province
          </CardTitle>
          <CardDescription>
            Official 2025 general rates (HST / GST+PST / GST+QST). Applies to the order subtotal.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <form
            className="flex flex-wrap items-end gap-3"
            onSubmit={(e) => {
              e.preventDefault()
              const rate = Number(taxRate)
              if (!isFinite(rate) || rate < 0) return
              saveTax(taxProv, rate, selectedTaxProv?.name ?? taxProv)
              setTaxRate("")
            }}
          >
            <label className="min-w-56 flex-1 space-y-1.5">
              <span className="text-sm font-medium">Province / territory</span>
              <Select value={taxProv} onValueChange={(v) => {
                setTaxProv(v)
                const p = CA_PROVINCES.find((x) => x.code === v)
                if (p) setTaxRate(String(p.rate)) // prefill with the official rate
              }}>
                <SelectTrigger>
                  <SelectValue placeholder="Choose a province" />
                </SelectTrigger>
                <SelectContent>
                  {CA_PROVINCES.map((p) => (
                    <SelectItem key={p.code} value={p.code}>
                      {p.name} — {p.breakdown}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </label>
            <label className="w-36 space-y-1.5">
              <span className="text-sm font-medium">Charged rate (%)</span>
              <Input
                type="number"
                min="0"
                step="0.001"
                value={taxRate}
                onChange={(e) => setTaxRate(e.target.value)}
                placeholder={selectedTaxProv ? String(selectedTaxProv.rate) : "13"}
                required
              />
            </label>
            <Button type="submit" variant="outline">
              <Save className="mr-1 h-4 w-4" /> Save tax rule
            </Button>
          </form>
          {selectedTaxProv && (
            <p className="flex items-center gap-2 text-xs text-muted-foreground">
              <Landmark className="h-3.5 w-3.5" />
              {selectedTaxProv.name}: {selectedTaxProv.breakdown} — official combined rate{" "}
              <strong>{selectedTaxProv.rate}%</strong>.{" "}
              {taxMap.has(selectedTaxProv.code) ? (
                <Badge variant="secondary">configured: {taxMap.get(selectedTaxProv.code)}%</Badge>
              ) : (
                <Badge variant="destructive">not configured</Badge>
              )}
            </p>
          )}

          {/* Configured provinces at a glance */}
          {taxes === null ? (
            <Skeleton className="h-24" />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Region</TableHead>
                  <TableHead>System</TableHead>
                  <TableHead className="text-right">Charged rate</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {CA_PROVINCES.filter((p) => taxMap.has(p.code)).map((p) => (
                  <TableRow key={p.code}>
                    <TableCell className="font-medium">{p.name}</TableCell>
                    <TableCell className="text-muted-foreground">{p.breakdown}</TableCell>
                    <TableCell className="text-right tabular-nums">{taxMap.get(p.code)}%</TableCell>
                  </TableRow>
                ))}
                {taxes.filter((t) => !t.country.startsWith("CA-")).map((t) => (
                  <TableRow key={t.country}>
                    <TableCell className="font-medium">
                      {t.country === "*" ? <span className="italic">default (*)</span> : t.country}
                    </TableCell>
                    <TableCell className="text-muted-foreground">international</TableCell>
                    <TableCell className="text-right tabular-nums">{Number(t.rate)}%</TableCell>
                  </TableRow>
                ))}
                {taxes.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={3} className="py-8 text-center text-muted-foreground">
                      No tax rules — 0% everywhere until configured.
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {/* ── Canada — Shipping ────────────────────────────────────────── */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <Truck className="h-4 w-4" /> Canadian shipping by province &amp; city
          </CardTitle>
          <CardDescription>
            Pick a province to edit its rate and its cities. Shoppers are charged the most
            specific rate: city (big cities) → province → country → default. Unlisted cities
            automatically get the province rate.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex flex-wrap items-end gap-3">
            <label className="min-w-64 flex-1 space-y-1.5">
              <span className="text-sm font-medium">Province / territory</span>
              <Select value={shipProv} onValueChange={(v: string) => setShipProv(v)}>
                <SelectTrigger>
                  <SelectValue placeholder="Choose a province" />
                </SelectTrigger>
                <SelectContent>
                  {CA_PROVINCES.map((p) => {
                    const tier = SHIP_TIERS[PROVINCE_TIER[p.code] ?? "national"]
                    return (
                      <SelectItem key={p.code} value={p.code}>
                        {p.name} — {tier.label}
                      </SelectItem>
                    )
                  })}
                </SelectContent>
              </Select>
            </label>
            <Button
              variant="outline"
              onClick={() => {
                const tier = SHIP_TIERS[PROVINCE_TIER[shipProv] ?? "national"]
                saveZone(shipProv, tier.suggestion, `${selectedShipProv?.name ?? shipProv} (${tier.label})`)
              }}
            >
              <Save className="mr-1 h-4 w-4" /> Apply province rate ({money(SHIP_TIERS[PROVINCE_TIER[shipProv] ?? "national"].suggestion)})
            </Button>
          </div>
          {selectedShipProv && (
            <p className="text-xs text-muted-foreground">
              {SHIP_TIERS[PROVINCE_TIER[shipProv] ?? "national"].note}.{" "}
              {zoneMap.has(shipProv) ? (
                <Badge variant="secondary">province rate: {money(zoneMap.get(shipProv)!)}</Badge>
              ) : (
                <Badge variant="destructive">no province rate — free shipping</Badge>
              )}
            </p>
          )}

          {/* Province rate + city rates, one editable table */}
          {zones === null ? (
            <Skeleton className="h-24" />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Zone</TableHead>
                  <TableHead>Scope</TableHead>
                  <TableHead className="w-32">Rate ($)</TableHead>
                  <TableHead className="w-24" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {/* Province row */}
                <tr className="border-b">
                  <td className="py-2 font-medium">
                    {selectedShipProv?.name ?? shipProv}
                    <span className="ml-2 text-xs text-muted-foreground">everyone in the province</span>
                  </td>
                  <td className="text-xs text-muted-foreground">
                    {SHIP_TIERS[PROVINCE_TIER[shipProv] ?? "national"].label}
                  </td>
                  <td>
                    <Input
                      type="number"
                      min="0"
                      step="0.01"
                      value={provRateDraft}
                      onChange={(e) => setProvRateDraft(e.target.value)}
                      className="h-8"
                      placeholder={String(SHIP_TIERS[PROVINCE_TIER[shipProv] ?? "national"].suggestion)}
                    />
                  </td>
                  <td className="text-right">
                    <Button
                      variant="outline"
                      size="sm"
                      className="h-8"
                      onClick={() => {
                        const rate = Number(provRateDraft)
                        if (!isFinite(rate) || rate < 0) return
                        saveZone(shipProv, rate, selectedShipProv?.name ?? shipProv)
                      }}
                    >
                      Save
                    </Button>
                  </td>
                </tr>
                {/* City rows for this province */}
                {CA_CITIES.filter((c) => c.provCode === shipProv).map((c) => {
                  const configured = zoneMap.has(c.cityKey)
                  const draft = cityRateDrafts[c.cityKey] ?? (configured ? String(zoneMap.get(c.cityKey)) : String(c.suggestion))
                  return (
                    <tr key={c.cityKey} className="border-b last:border-0">
                      <td className="py-2">
                        <span className="pl-4 font-medium">{c.name}</span>
                        <span className="ml-2 text-xs text-muted-foreground">
                          {c.tier === "metro" ? "metro" : "city"} · overrides province
                        </span>
                      </td>
                      <td className="text-xs text-muted-foreground">
                        {configured ? (
                          <Badge variant="secondary">local rate</Badge>
                        ) : (
                          <span className="italic">uses province rate</span>
                        )}
                      </td>
                      <td>
                        <Input
                          type="number"
                          min="0"
                          step="0.01"
                          value={draft}
                          onChange={(e) => setCityRateDrafts((m) => ({ ...m, [c.cityKey]: e.target.value }))}
                          className="h-8"
                        />
                      </td>
                      <td className="text-right">
                        <Button
                          variant="outline"
                          size="sm"
                          className="h-8"
                          onClick={() => {
                            const rate = Number(draft)
                            if (!isFinite(rate) || rate < 0) return
                            saveZone(c.cityKey, rate, `${c.name} (local)`)
                          }}
                        >
                          Save
                        </Button>
                      </td>
                    </tr>
                  )
                })}
                {CA_CITIES.filter((c) => c.provCode === shipProv).length === 0 && (
                  <tr>
                    <td colSpan={4} className="py-6 text-center text-sm text-muted-foreground">
                      No city rates listed for {selectedShipProv?.name ?? shipProv} yet — everyone gets the province rate.
                    </td>
                  </tr>
                )}
                {/* International rows stay visible for context */}
                {(zones ?? []).filter((z) => !z.country.startsWith("CA")).map((z) => (
                  <tr key={z.country} className="border-b last:border-0">
                    <td className="py-2 font-medium">
                      {z.country === "*" ? <span className="italic">default (*)</span> : z.country}
                      <span className="ml-2 text-xs text-muted-foreground">country-level</span>
                    </td>
                    <td className="text-xs text-muted-foreground">international</td>
                    <td className="tabular-nums">{money(Number(z.rate))}</td>
                    <td />
                  </tr>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {/* ── International ────────────────────────────────────────────── */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <Globe2 className="h-4 w-4" /> International
          </CardTitle>
          <CardDescription>
            Country-level rules for orders outside Canada. Unlisted countries fall back to the
            <code className="mx-1 rounded bg-muted px-1">*</code> default.
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-6 md:grid-cols-2">
          <form
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault()
              const rate = Number(intlZoneRate)
              if (!isFinite(rate) || rate < 0) return
              const label = OTHER_REGIONS.find((r) => r.code === intlZoneRegion)?.name ?? intlZoneRegion
              saveZone(intlZoneRegion, rate, label)
              setIntlZoneRate("")
            }}
          >
            <span className="text-sm font-medium">Shipping zone</span>
            <div className="flex items-end gap-2">
              <Select value={intlZoneRegion} onValueChange={setIntlZoneRegion}>
                <SelectTrigger className="w-44">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {OTHER_REGIONS.map((r) => (
                    <SelectItem key={r.code} value={r.code}>{r.name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Input
                type="number"
                min="0"
                step="0.01"
                value={intlZoneRate}
                onChange={(e) => setIntlZoneRate(e.target.value)}
                placeholder="12.99"
                className="w-28"
                required
              />
              <Button type="submit" variant="outline" size="sm">
                <Save className="mr-1 h-3.5 w-3.5" /> Save
              </Button>
            </div>
          </form>
          <form
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault()
              const rate = Number(intlTaxRate)
              if (!isFinite(rate) || rate < 0) return
              const label = OTHER_REGIONS.find((r) => r.code === intlTaxRegion)?.name ?? intlTaxRegion
              saveTax(intlTaxRegion, rate, label)
              setIntlTaxRate("")
            }}
          >
            <span className="text-sm font-medium">Tax rule</span>
            <div className="flex items-end gap-2">
              <Select value={intlTaxRegion} onValueChange={setIntlTaxRegion}>
                <SelectTrigger className="w-44">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {OTHER_REGIONS.map((r) => (
                    <SelectItem key={r.code} value={r.code}>{r.name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Input
                type="number"
                min="0"
                step="0.01"
                value={intlTaxRate}
                onChange={(e) => setIntlTaxRate(e.target.value)}
                placeholder="0"
                className="w-28"
                required
              />
              <Button type="submit" variant="outline" size="sm">
                <Save className="mr-1 h-3.5 w-3.5" /> Save
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
