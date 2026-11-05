import { ApiError, initialLogState, type LogLine } from '@lerryn/core';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

import { resolveLocale, translateMobile } from '../i18n/translate';
import { en, zhCN } from '../i18n/messages';
import { createMobileClient } from './client';
import { deviceInfo } from './device';
import { errorKind, shouldRetry } from './errors';
import { applyStreamEnd, applyStreamLines, isLogLine, lastSeq, parseEventData } from './logs';

describe('createMobileClient', () => {
  it('sends the stored cookie, bypasses the cookie jar, and gives writes the server origin', async () => {
    const seen: Request[] = [];
    const fetch = (async (req: Request) => {
      seen.push(req);
      return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } });
    }) as typeof globalThis.fetch;
    const client = createMobileClient({ server: 'https://corp.example.com/lerryn', cookie: 'abc', fetch });
    await client.GET('/api/v1/me');
    await client.POST('/api/v1/auth/logout');
    expect(seen[0]!.url).toBe('https://corp.example.com/lerryn/api/v1/me');
    expect(seen[0]!.headers.get('Cookie')).toBe('lerryn_session=abc');
    expect(seen[0]!.headers.get('Origin')).toBeNull();
    expect(seen[0]!.credentials).toBe('omit');
    expect(seen[1]!.headers.get('Origin')).toBe('https://corp.example.com');
  });

  it('sends no cookie before signing in', async () => {
    let req: Request | undefined;
    const fetch = (async (r: Request) => {
      req = r;
      return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } });
    }) as typeof globalThis.fetch;
    await createMobileClient({ server: 'https://a.example', cookie: null, fetch }).GET('/api/v1/discover');
    expect(req!.headers.get('Cookie')).toBeNull();
  });
});

// A17: the phone session is read-only. Besides the server enforcing it, the
// app itself must not contain any write call other than signing in and out.
describe('read-only app', () => {
  const root = join(__dirname, '..', '..');
  const files: string[] = [];
  const walk = (dir: string) => {
    for (const name of readdirSync(dir)) {
      if (name === 'node_modules' || name.startsWith('.') || name === 'dist') continue;
      const path = join(dir, name);
      if (statSync(path).isDirectory()) walk(path);
      else if (/\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name)) files.push(path);
    }
  };
  walk(join(root, 'app'));
  walk(join(root, 'src'));

  it('only POSTs to login, pairing exchange and logout, and never PUT, PATCH or DELETE', () => {
    const allowed = new Set(['/api/v1/auth/login', '/api/v1/auth/pairing/exchange', '/api/v1/auth/logout']);
    const writes: string[] = [];
    for (const file of files) {
      const src = readFileSync(file, 'utf8');
      for (const m of src.matchAll(/\.(POST|PUT|PATCH|DELETE)\(\s*['"`]([^'"`]+)['"`]/g)) {
        if (m[1] !== 'POST' || !allowed.has(m[2]!)) writes.push(`${file}: ${m[1]} ${m[2]}`);
      }
      expect(src).not.toMatch(/method:\s*['"](POST|PUT|PATCH|DELETE)['"]/);
    }
    expect(files.length).toBeGreaterThan(10);
    expect(writes).toEqual([]);
  });
});

describe('errorKind', () => {
  const err = (status: number, code: ConstructorParameters<typeof ApiError>[1]) => new ApiError(status, code, null);
  it.each([
    [err(0, 'network_error'), 'offline'],
    [err(401, 'unauthenticated'), 'signedOut'],
    [err(404, 'not_found'), 'notFound'],
    [err(403, 'insufficient_scope'), 'readOnly'],
    [err(429, 'rate_limited'), 'rateLimited'],
    [err(500, 'internal_error'), 'failed'],
    [new Error('x'), 'failed'],
  ])('%#', (e, kind) => {
    expect(errorKind(e)).toBe(kind);
  });

  it('retries only what can change', () => {
    expect(shouldRetry(0, err(500, 'internal_error'))).toBe(true);
    expect(shouldRetry(2, err(500, 'internal_error'))).toBe(false);
    expect(shouldRetry(0, err(404, 'not_found'))).toBe(false);
    expect(shouldRetry(0, err(401, 'unauthenticated'))).toBe(false);
  });
});

describe('deviceInfo', () => {
  it('describes an iPhone', () => {
    expect(deviceInfo({ OS: 'ios', Version: '26.1', constants: { interfaceIdiom: 'phone', systemName: 'iOS', osVersion: '26.1' } })).toEqual({
      name: 'iPhone',
      platform: 'ios',
      os_version: 'iOS 26.1',
    });
    expect(deviceInfo({ OS: 'ios', Version: '26.1', constants: { interfaceIdiom: 'pad' } }).name).toBe('iPad');
  });

  it('describes an Android phone without repeating the brand', () => {
    expect(deviceInfo({ OS: 'android', Version: 36, constants: { Brand: 'google', Model: 'Pixel 9', Release: '16' } })).toEqual({
      name: 'google Pixel 9',
      platform: 'android',
      os_version: 'Android 16',
    });
    expect(deviceInfo({ OS: 'android', Version: 36, constants: { Brand: 'samsung', Model: 'SAMSUNG SM-S921' } }).name).toBe('SAMSUNG SM-S921');
  });

  it('keeps within the contract limits', () => {
    const d = deviceInfo({ OS: 'android', Version: 1, constants: { Brand: 'x'.repeat(40), Model: 'y'.repeat(40), Release: 'z'.repeat(40) } });
    expect(d.name.length).toBeLessThanOrEqual(64);
    expect(d.os_version!.length).toBeLessThanOrEqual(32);
  });
});

describe('log stream', () => {
  const line = (seq: number): LogLine => ({ seq, ts: '2026-11-03T08:00:00Z', stream: 'stdout', stage: null, text: `l${seq}` });

  it('merges pushed lines with what was read, without duplicates', () => {
    let s = applyStreamLines({ ...initialLogState, lines: [line(1), line(2)], loaded: true }, [line(2), line(3)]);
    expect(s.lines.map((l) => l.seq)).toEqual([1, 2, 3]);
    expect(lastSeq(s)).toBe('3');
    s = applyStreamLines(s, [line(4), line(5)], 3);
    expect(s.lines.map((l) => l.seq)).toEqual([3, 4, 5]);
    expect(s.headCursor).toBeNull();
    expect(applyStreamEnd(s, { dropped_lines: 2 })).toMatchObject({ complete: true, dropped: 2 });
    expect(lastSeq(initialLogState)).toBe('');
  });

  it('skips malformed events', () => {
    expect(parseEventData('{"seq":1}')).toEqual({ seq: 1 });
    expect(parseEventData('nope')).toBeNull();
    expect(parseEventData('3')).toBeNull();
    expect(isLogLine(line(1))).toBe(true);
    expect(isLogLine({ seq: '1', text: 'x', ts: 'y' })).toBe(false);
  });
});

describe('i18n', () => {
  it('has the same keys in both catalogs', () => {
    const keys = (o: object, p = ''): string[] =>
      Object.entries(o).flatMap(([k, v]) => (typeof v === 'string' ? [`${p}${k}`] : keys(v as object, `${p}${k}.`)));
    expect(keys(en).sort()).toEqual(keys(zhCN).sort());
  });

  it('fills placeholders', () => {
    expect(translateMobile('zh-CN', 'pair.title', { host: 'a.example' })).toBe('登录到 a.example');
    expect(translateMobile('en', 'task.credentials', { count: 2 })).toBe('Credentials 2');
  });

  it('picks the app choice, then the account, then the system languages', () => {
    expect(resolveLocale('en', 'zh-CN', ['zh-Hans-CN'])).toBe('en');
    expect(resolveLocale('system', 'en', ['zh-Hans-CN'])).toBe('en');
    expect(resolveLocale('system', null, ['fr-FR', 'zh-Hant-TW'])).toBe('zh-CN');
    expect(resolveLocale('system', null, ['en-SG'])).toBe('en');
    expect(resolveLocale('system', null, ['fr-FR'])).toBe('en');
    expect(resolveLocale('system', null, [])).toBe('zh-CN');
  });
});
