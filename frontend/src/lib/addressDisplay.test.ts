import { describe, expect, it } from "vitest";
import { toASCII } from "punycode/";
import { addressDisplay } from "./addressDisplay";

describe("international address display", () => {
  it("decodes the reported Polish address without calling an ordinary accent suspicious", () => {
    expect(addressDisplay("support@xn--czasnacianie-slc.com")).toEqual({
      display: "support@czasnaścianie.com", unicode: "support@czasnaścianie.com",
      encoded: "support@xn--czasnacianie-slc.com", warnings: []
    });
    expect(addressDisplay("support@XN--CZASNACIANIE-SLC.com").display).toBe("support@czasnaścianie.com");
  });

  it.each(["bücher.de", "mañana.es", "παράδειγμα.gr", "пример.рф", "例え.jp", "日本語abc.jp", "مثال.مصر"])("keeps legitimate international domain %s readable", (domain) => {
    const result = addressDisplay(`mail@${toASCII(domain)}`);
    expect(result.display).toBe(`mail@${domain}`);
    expect(result.warnings).toEqual([]);
  });

  it("handles address lists and quoted local parts without modifying local-part case", () => {
    const source = 'Support <"User@Work"@xn--bcher-kva.de>, Alice <Alice@Example.COM>';
    const result = addressDisplay(source);
    expect(result.display).toBe('Support <"User@Work"@bücher.de>, Alice <Alice@Example.COM>');
    expect(result.encoded).toBe(source);
    expect(result.warnings).toEqual([]);
  });

  it("accepts capitalized and canonically decomposed international domains", () => {
    for (const domain of ["BÜCHER.DE", "bu\u0308cher.de"]) {
      const result = addressDisplay(`User@${domain}`);
      expect(result.display).toBe(`User@${domain}`);
      expect(result.encoded).toBe("User@xn--bcher-kva.de");
      expect(result.warnings).toEqual([]);
    }
  });

  it.each(["pаypal.com", "аррӏе.com", "ɡoogle.com"])("warns about lookalikes in %s and keeps the encoded domain visible", (domain) => {
    for (const input of [domain, toASCII(domain)]) {
      const result = addressDisplay(`support@${input}`);
      expect(result.display).toBe(`support@${toASCII(domain)}`);
      expect(result.unicode).toBe(`support@${domain}`);
      expect(result.warnings.join(" ")).toContain("lookalike");
    }
  });

  it("warns about mixed alphabets even if there is no complete ASCII lookalike", () => {
    expect(addressDisplay(`mail@${toASCII("abcж.com")}`).warnings.join(" ")).toContain("mixes Latin");
  });

  it("also checks non-ASCII local parts without rewriting them", () => {
    const result = addressDisplay("pаypal@example.com");
    expect(result.display).toBe("pаypal@example.com");
    expect(result.warnings.join(" ")).toContain("part before @");
  });

  it.each(["xn--", "xn--a.com", "xn--abc-.com", "xn--not_punycode.com"])("preserves malformed punycode %s with a warning", (domain) => {
    const result = addressDisplay(`support@${domain}`);
    expect(result.display).toBe(`support@${domain}`);
    expect(result.warnings.join(" ")).toContain("could not be decoded and validated");
  });

  it("exposes invisible and directional characters in names, local parts and domains", () => {
    for (const input of ["Pay\u202Epal <user@example.com>", "pay\u200Bpal@example.com", "user@ex\u200Bample.com"]) {
      const result = addressDisplay(input);
      expect(result.display).not.toMatch(/[\u202E\u200B]/u);
      expect(result.display).toContain("[U+");
      expect(result.warnings.join(" ")).toContain("invisible");
    }
  });

  it("warns about symbol domains while leaving domain literals alone", () => {
    expect(addressDisplay(`mail@${toASCII("☃.net")}`).warnings.join(" ")).toContain("symbols");
    expect(addressDisplay("user@[IPv6:2001:db8::1]").warnings).toEqual([]);
  });
});
