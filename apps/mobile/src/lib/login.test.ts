import { describe, expect, it } from 'vitest';

import { probeServer } from './login';

const reply = (body: unknown, status = 200) =>
  (async () =>
    new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })) as typeof globalThis.fetch;

const caps = (login_methods: string[], admin_password_login?: boolean) => ({
  capabilities: { login_methods, admin_password_login, registration_open: false, first_account: false, network_policies: [], require_test_before_activate: false },
});

describe('probeServer', () => {
  it.each([
    ['password login on', caps(['password', 'oidc']), 'primary'],
    ['local password login off, admins still may', caps(['oidc'], true), 'admin'],
    ['no password login for anyone', caps(['oidc'], false), 'none'],
  ])('%s', async (_name, body, password) => {
    const probe = await probeServer('https://w.test', reply(body));
    expect(probe).toMatchObject({ kind: 'ok', layout: { password } });
  });

  it('tells an unreachable server from one that is not Lerryn', async () => {
    const offline = (async () => {
      throw new TypeError('Network request failed');
    }) as typeof globalThis.fetch;
    expect(await probeServer('https://w.test', offline)).toEqual({ kind: 'unreachable' });
    expect(await probeServer('https://w.test', reply({ hello: 'world' }))).toEqual({ kind: 'notLerryn' });
    expect(await probeServer('https://w.test', reply({}, 404))).toEqual({ kind: 'notLerryn' });
  });
});
