/**
 * Canada reference data — verified against Canada.ca (GST/HST calculator,
 * April 2025 revision) and TaxTips.ca 2025 tables.
 *
 * Tax: HST provinces charge one harmonized rate; GST provinces charge 5%
 * federal only; PST/QST provinces charge GST + a provincial rate. For
 * checkout math the engine applies ONE rate, so we store the COMBINED
 * general rate (e.g. QC 14.975 = 5 GST + 9.975 QST) — matching what a
 * shopper actually pays on most goods.
 */

export interface CaProvince {
  code: string // postal abbreviation — the region key sent at checkout
  name: string
  /** Combined general sales tax rate (%) a shopper pays on most goods. */
  rate: number
  /** Rate breakdown shown in the UI, e.g. "5% GST + 9.975% QST". */
  breakdown: string
  /** Tax system label. */
  system: "HST" | "GST" | "GST+PST" | "GST+QST"
}

export const CA_PROVINCES: CaProvince[] = [
  { code: "CA-ON", name: "Ontario", rate: 13, breakdown: "13% HST", system: "HST" },
  { code: "CA-QC", name: "Quebec", rate: 14.975, breakdown: "5% GST + 9.975% QST", system: "GST+QST" },
  { code: "CA-BC", name: "British Columbia", rate: 12, breakdown: "5% GST + 7% PST", system: "GST+PST" },
  { code: "CA-AB", name: "Alberta", rate: 5, breakdown: "5% GST only", system: "GST" },
  { code: "CA-SK", name: "Saskatchewan", rate: 11, breakdown: "5% GST + 6% PST", system: "GST+PST" },
  { code: "CA-MB", name: "Manitoba", rate: 12, breakdown: "5% GST + 7% RST", system: "GST+PST" },
  { code: "CA-NS", name: "Nova Scotia", rate: 14, breakdown: "14% HST (reduced from 15% Apr 2025)", system: "HST" },
  { code: "CA-NB", name: "New Brunswick", rate: 15, breakdown: "15% HST", system: "HST" },
  { code: "CA-NL", name: "Newfoundland & Labrador", rate: 15, breakdown: "15% HST", system: "HST" },
  { code: "CA-PE", name: "Prince Edward Island", rate: 15, breakdown: "15% HST", system: "HST" },
  { code: "CA-NT", name: "Northwest Territories", rate: 5, breakdown: "5% GST only", system: "GST" },
  { code: "CA-NU", name: "Nunavut", rate: 5, breakdown: "5% GST only", system: "GST" },
  { code: "CA-YT", name: "Yukon", rate: 5, breakdown: "5% GST only", system: "GST" },
]

/**
 * Shipping presets modelled on Canada Post's service tiers (2025):
 * - Local: same city/region carrier run
 * - Regional: Canada Post Regional flat-rate territory (~$16.99–$22.99 box)
 * - National: cross-country (~$29.99+ flat-rate box, Regular Parcel from
 *   ~$10.91 + 35% fuel surcharge)
 * - Remote: NT/NU/YT plus remote postal codes — carry a premium everywhere
 */
export type ShipTier = "local" | "regional" | "national" | "remote"

export const SHIP_TIERS: Record<ShipTier, { label: string; suggestion: number; note: string }> = {
  local: { label: "Local (same metro)", suggestion: 8, note: "Bike-courier / same-city run" },
  regional: { label: "Regional (in-province + adjacent)", suggestion: 14, note: "CP Regional flat-rate box territory" },
  national: { label: "National (rest of Canada)", suggestion: 18, note: "CP National flat-rate / Regular Parcel" },
  remote: { label: "Remote (territories)", suggestion: 28, note: "NT/NU/YT — air surcharge territory" },
}

/** Province → suggested shipping tier, from a southern-Ontario warehouse. */
export const PROVINCE_TIER: Record<string, ShipTier> = {
  "CA-ON": "local",
  "CA-QC": "regional",
  "CA-MB": "regional",
  "CA-SK": "national",
  "CA-AB": "national",
  "CA-BC": "national",
  "CA-NS": "national",
  "CA-NB": "national",
  "CA-NL": "national",
  "CA-PE": "national",
  "CA-NT": "remote",
  "CA-NU": "remote",
  "CA-YT": "remote",
}

/** Non-CA countries kept for the international (generic) section. */
export const OTHER_REGIONS = [
  { code: "US", name: "United States" },
  { code: "GB", name: "United Kingdom" },
  { code: "AU", name: "Australia" },
  { code: "DE", name: "Germany" },
  { code: "*", name: "Default (everywhere else)" },
]
