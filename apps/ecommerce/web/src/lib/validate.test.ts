import { describe, expect, it } from "vitest"

import { isValidEmail } from "./validate"

/**
 * Permanent spec for the shared email validator. The engine's manifest
 * asserts (PLACE_ORDER / SUBSCRIBE_EMAIL / REGISTER_ACCOUNT) pin the same
 * semantics server-side — keep the two patterns in sync.
 */
describe("isValidEmail", () => {
  it("accepts well-formed addresses", () => {
    expect(isValidEmail("you@example.com")).toBe(true)
    expect(isValidEmail("jane.doe+news@shop.co")).toBe(true)
    expect(isValidEmail("  padded@mail.org  ")).toBe(true) // trims
  })

  it("rejects obvious junk", () => {
    expect(isValidEmail("")).toBe(false)
    expect(isValidEmail("plain")).toBe(false)
    expect(isValidEmail("test@")).toBe(false)
    expect(isValidEmail("test@test")).toBe(false) // no TLD
    expect(isValidEmail("jane@x.c")).toBe(false) // one-char TLD
    expect(isValidEmail("a@@b.com")).toBe(false) // double @
    expect(isValidEmail("foo bar@x.com")).toBe(false) // space
  })
})
