import type { KeyboardEvent } from 'react';

type ComposingKeyboardEvent = Pick<KeyboardEvent, 'keyCode' | 'nativeEvent'>;

export function isImeComposing(event: ComposingKeyboardEvent): boolean {
  // keyCode is deprecated, but still a useful fallback for IME Enter events in
  // older browser / test environments where nativeEvent.isComposing is false.
  return event.nativeEvent.isComposing || event.keyCode === 229;
}
