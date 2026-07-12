// Mail shortcuts stay inactive while a native control or editable region owns
// the keyboard. This keeps typing and button activation predictable.
export function shouldIgnoreMailShortcut(event: KeyboardEvent): boolean {
  if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey) return true;
  const target = event.target;
  if (!(target instanceof Element)) return false;
  return Boolean(target.closest("input, textarea, select, button, a, summary, [contenteditable='true']"));
}
