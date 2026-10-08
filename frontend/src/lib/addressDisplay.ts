import { toASCII, toUnicode } from "punycode/";
import asciiConfusables from "./unicode/asciiConfusables.json";

// A deliberately limited heuristic, not a spam verdict or full UTS #39 checker.
// The lookalike table contains Latin, Greek and Cyrillic mappings to ASCII from
// Unicode 18.0.0 confusables.txt (2026-08-06), retrieved from
// https://www.unicode.org/Public/security/latest/confusables.txt.
// License: frontend/public/licenses/unicode.txt (also shipped with the app).
// Ordinary accents are not stripped for comparison.
const confusables: Readonly<Record<string, string>> = asciiConfusables;
const hiddenCharacter = /[\p{Default_Ignorable_Code_Point}\p{Cc}]/u;
const hiddenCharacters = /[\p{Default_Ignorable_Code_Point}\p{Cc}]/gu;
const addressPattern = /(?:"(?:[^"\\]|\\.)+"|[^\s<>(),;"@]+)@[^\s<>(),;"@]+/gu;

export type AddressDisplay = {
  display: string;
  unicode: string;
  encoded: string;
  warnings: string[];
};

// Expose controls instead of letting them hide or reorder an address/name.
export function visibleAddressText(value: string): string {
  return value.replace(hiddenCharacters, (character) => `[U+${character.codePointAt(0)!.toString(16).toUpperCase().padStart(4, "0")}]`);
}

function characterWarnings(value: string, description: string): string[] {
  const warnings: string[] = [];
  if (hiddenCharacter.test(value)) {
    warnings.push(`${description} contains invisible or text-direction characters, shown as code points.`);
  }
  // Check each label separately: a Cyrillic domain with a Latin .com suffix is
  // normal. Mixing Latin with Japanese/Chinese/Korean is also common.
  const scripts = [ /\p{Script=Latin}/u, /\p{Script=Greek}/u, /\p{Script=Cyrillic}/u ];
  if (scripts.filter((script) => script.test(value)).length > 1) {
    warnings.push(`${description} mixes Latin, Greek or Cyrillic letters that can be hard to distinguish.`);
  }
  const skeleton = Array.from(value, (character) => confusables[character] || character).join("");
  if (skeleton !== value && /^[a-z0-9-]+$/i.test(skeleton)) {
    warnings.push(`${description} uses lookalike characters and can resemble “${skeleton}”.`);
  }
  return warnings;
}

function presentAddress(address: string): AddressDisplay {
  const at = address.lastIndexOf("@");
  const local = address.slice(0, at);
  const domain = address.slice(at + 1);
  const warnings = characterWarnings(local, "The part before @");
  let unicode = domain;
  let encoded = domain;
  if (!domain.startsWith("[")) {
    try {
      if (domain.length > 1024) throw new Error("Domain too long");
      unicode = domain.split(".").map((label) => /^xn--/i.test(label) ? toUnicode(label.toLowerCase()) : label).join(".");
      encoded = toASCII(unicode.normalize("NFC").toLowerCase());
      const international = unicode !== domain || /[^\x00-\x7f]/.test(domain) || /(^|\.)xn--/i.test(domain);
      if (international) {
        // Punycode decoding alone does not validate IDNA. Require a canonical
        // round trip through the browser's IDNA implementation, without I/O.
        const validLabels = encoded.replace(/\.$/, "").split(".").every((label) => (
          label.length <= 63 && /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/i.test(label)
        ));
        const originalLabels = domain.split(".");
        const encodedLabels = encoded.split(".");
        const canonicalPunycode = originalLabels.every((label, index) => !/^xn--/i.test(label) || label.toLowerCase() === encodedLabels[index]);
        if (!validLabels || !canonicalPunycode || encoded.length > 253 || new URL(`https://${unicode}`).hostname !== encoded) {
          throw new Error("Invalid international domain");
        }
        for (const label of unicode.split(".")) {
          warnings.push(...characterWarnings(label, `Domain label “${visibleAddressText(label)}”`));
          if (/[^\p{L}\p{M}\p{N}-]/u.test(label)) {
            warnings.push("The domain contains symbols rather than ordinary letters or numbers.");
          }
        }
      } else {
        encoded = domain;
      }
    } catch {
      // Malformed message headers must never break rendering or silently repair
      // a destination into a different address.
      unicode = domain;
      encoded = domain;
      warnings.push("The international domain could not be decoded and validated. Check the original address.");
    }
  }
  return {
    display: visibleAddressText(`${local}@${warnings.length ? encoded : unicode}`),
    unicode: visibleAddressText(`${local}@${unicode}`),
    encoded: visibleAddressText(`${local}@${encoded}`),
    warnings
  };
}

/** Display-only transformation for addresses and address header lists. Never
 * use these values to send mail, identify contacts, or replace stored headers. */
export function addressDisplay(value: string): AddressDisplay {
  const addresses = Array.from(value.matchAll(addressPattern), (match) => ({
    start: match.index!, end: match.index! + match[0].length, ...presentAddress(match[0])
  }));
  const render = (field: "display" | "unicode" | "encoded") => {
    let result = "";
    let end = 0;
    for (const address of addresses) {
      result += visibleAddressText(value.slice(end, address.start)) + address[field];
      end = address.end;
    }
    return result + visibleAddressText(value.slice(end));
  };
  const warnings = addresses.flatMap((address) => address.warnings);
  if (hiddenCharacter.test(value) && !warnings.some((warning) => warning.includes("invisible"))) {
    warnings.push("The address header contains invisible or text-direction characters, shown as code points.");
  }
  return { display: render("display"), unicode: render("unicode"), encoded: render("encoded"), warnings: [...new Set(warnings)] };
}
