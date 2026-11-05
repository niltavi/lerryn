import { describe, expect, it } from 'vitest';

import {
  isInsecure,
  normalizePairingCode,
  normalizeServerUrl,
  pairingFromParams,
  parsePairingUri,
  serverHost,
  serverOrigin,
} from './server';

describe('normalizeServerUrl', () => {
  it.each([
    ['lerryn.example.com', 'https://lerryn.example.com'],
    ['  Lerryn.Example.com/  ', 'https://lerryn.example.com'],
    ['https://lerryn.example.com/', 'https://lerryn.example.com'],
    ['http://10.0.0.5:8080', 'http://10.0.0.5:8080'],
    ['localhost:8080', 'https://localhost:8080'],
    ['https://corp.example.com/lerryn/', 'https://corp.example.com/lerryn'],
    ['https://lerryn.example.com/?x=1#top', 'https://lerryn.example.com'],
    ['http://[::1]:8080', 'http://[::1]:8080'],
  ])('%s → %s', (input, want) => {
    expect(normalizeServerUrl(input)).toBe(want);
  });

  it.each(['', '   ', 'ftp://lerryn.example.com', 'javascript:alert(1)', 'https://', 'https://user:pw@host.example', 'https://bad host', 'https://-x.example', 'https://host:99999'])(
    'rejects %j',
    (input) => {
      expect(normalizeServerUrl(input)).toBeNull();
    },
  );
});

describe('server helpers', () => {
  it('derives origin and host', () => {
    expect(serverOrigin('https://corp.example.com/lerryn')).toBe('https://corp.example.com');
    expect(serverHost('http://10.0.0.5:8080/x')).toBe('10.0.0.5:8080');
    expect(isInsecure('http://10.0.0.5')).toBe(true);
    expect(isInsecure('https://lerryn.example.com')).toBe(false);
  });
});

describe('pairing', () => {
  it('reads the console QR code', () => {
    const link = parsePairingUri(
      'lerryn://pair?server=https%3A%2F%2Flerryn.example.com%2F&code=K7Q2XN4V',
    );
    expect(link).toEqual({ server: 'https://lerryn.example.com/', baseUrl: 'https://lerryn.example.com', code: 'K7Q2XN4V' });
  });

  it('accepts unescaped server values and any parameter order', () => {
    expect(parsePairingUri('LERRYN://pair/?code=k7q2xn4v&server=http://10.0.0.5:8080')).toEqual({
      server: 'http://10.0.0.5:8080',
      baseUrl: 'http://10.0.0.5:8080',
      code: 'K7Q2XN4V',
    });
  });

  it.each([
    'https://lerryn.example.com/pair?server=https://a.example&code=K7Q2XN4V',
    'lerryn://pair?code=K7Q2XN4V',
    'lerryn://pair?server=https://a.example',
    'lerryn://pair?server=a.example&code=K7Q2XN4V',
    'lerryn://pair?server=javascript:x&code=K7Q2XN4V',
    'lerryn://pair?server=https://a.example&code=%E0%A4%A',
    'lerryn://pair?server=https://a.example&code=<script>',
    'lerryn://other?server=https://a.example&code=K7Q2XN4V',
    'hello',
  ])('ignores %s', (raw) => {
    expect(parsePairingUri(raw)).toBeNull();
  });

  it('validates router params from a deep link', () => {
    expect(pairingFromParams({ server: 'https://a.example', code: 'ab12cd34' })?.code).toBe('AB12CD34');
    expect(pairingFromParams({ server: ['https://a.example'], code: 'AB12CD34' })).toBeNull();
    expect(pairingFromParams({})).toBeNull();
  });

  it('normalizes typed codes', () => {
    expect(normalizePairingCode(' k7q2-xn4v ')).toBe('K7Q2XN4V');
    expect(normalizePairingCode('ab')).toBeNull();
    expect(normalizePairingCode('K7Q2XN4V!')).toBeNull();
  });
});
