import { addressDisplay } from "../lib/addressDisplay";
import { HighlightedText } from "../lib/searchHighlight";
import { Icon } from "./Icon";

export function EmailAddressText({ value, query = "", terms = [] }: { value: string; query?: string; terms?: string[] }) {
  const address = addressDisplay(value);
  return (
    <bdi title={address.display !== address.encoded ? `Encoded address: ${address.encoded}` : undefined}>
      <HighlightedText text={address.display} query={query} terms={terms} />
    </bdi>
  );
}

export function EmailAddressWarning({ value, compact = false }: { value: string; compact?: boolean }) {
  const address = addressDisplay(value);
  if (address.warnings.length === 0) return null;
  const description = `Check address. ${address.warnings.join(" ")} Address: ${address.encoded}`;
  if (compact) {
    return <span className="email-address-caution" role="img" title={description} aria-label={description}><Icon name="shield_warning" /></span>;
  }
  return (
    <details className="email-address-warning" onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
      <summary className="sender-security-caution"><Icon name="shield_warning" />Check address</summary>
      <div className="email-address-warning-details">
        <ul>{address.warnings.map((warning) => <li key={warning}>{warning}</li>)}</ul>
        <dl>
          <dt>Readable address</dt><dd><bdi>{address.unicode}</bdi></dd>
          <dt>Encoded address</dt><dd><bdi>{address.encoded}</bdi></dd>
        </dl>
        <p>These characters can be legitimate. This warning does not establish that the message is spam.</p>
      </div>
    </details>
  );
}
