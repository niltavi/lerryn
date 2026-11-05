import { describe, expect, it } from 'vitest';

import { decodeSession, encodeSession, isValidCookieValue, readSessionCookie } from './session';

const NOW = Date.parse('2026-11-03T10:00:00Z');

describe('readSessionCookie', () => {
  it('reads a single cookie', () => {
    expect(readSessionCookie('lerryn_session=abc.DEF_123; Path=/; HttpOnly; SameSite=Lax', NOW)).toBe('abc.DEF_123');
  });

  it('finds it among joined headers whose dates contain commas', () => {
    const header =
      'other=1; Expires=Wed, 21 Oct 2026 07:28:00 GMT; Path=/, lerryn_session=s3cr3t; Expires=Thu, 03 Dec 2026 10:00:00 GMT; HttpOnly, last=2';
    expect(readSessionCookie(header, NOW)).toBe('s3cr3t');
  });

  it('reports a cleared cookie as empty', () => {
    expect(readSessionCookie('lerryn_session=; Max-Age=0; Path=/', NOW)).toBe('');
    expect(readSessionCookie('lerryn_session=old; Expires=Thu, 01 Jan 1970 00:00:00 GMT', NOW)).toBe('');
  });

  it('does not match a cookie whose name only ends the same', () => {
    expect(readSessionCookie('x_lerryn_session=nope; Path=/', NOW)).toBeNull();
    expect(readSessionCookie(null, NOW)).toBeNull();
  });

  it('uses the last occurrence', () => {
    expect(readSessionCookie('lerryn_session=a, lerryn_session=b', NOW)).toBe('b');
  });
});

describe('stored session', () => {
  it('round-trips', () => {
    const s = { server: 'https://lerryn.example.com', cookie: 'abc' };
    expect(decodeSession(encodeSession(s))).toEqual(s);
  });

  it.each([
    null,
    '',
    'not json',
    '{"v":2,"server":"https://a","cookie":"x"}',
    '{"v":1,"server":"ftp://a","cookie":"x"}',
    '{"v":1,"server":"https://a","cookie":null}',
    '{"v":1,"server":"https://a","cookie":"a;b"}',
  ])('treats %j as signed out', (raw) => {
    expect(decodeSession(raw)).toBeNull();
  });

  it('only accepts cookie-safe values, so nothing can be injected into the Cookie header', () => {
    expect(isValidCookieValue('abc-DEF_1.2~')).toBe(true);
    expect(isValidCookieValue('a b')).toBe(false);
    expect(isValidCookieValue('a;b')).toBe(false);
    expect(isValidCookieValue('a\r\nX-Evil: 1')).toBe(false);
    expect(isValidCookieValue('')).toBe(false);
  });
});
