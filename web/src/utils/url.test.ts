import { describe, expect, it } from 'vitest';
import { isImageUrl } from './url';

describe('isImageUrl', () => {
  it('accepts http and https URL-shaped strings only', () => {
    expect(isImageUrl('http://example.com/image.png')).toBe(true);
    expect(isImageUrl('https://example.com/image.png')).toBe(true);
    expect(isImageUrl('ftp://example.com/image.png')).toBe(false);
    expect(isImageUrl('HTTPS://example.com/image.png')).toBe(false);
    expect(isImageUrl('😀')).toBe(false);
  });
});
