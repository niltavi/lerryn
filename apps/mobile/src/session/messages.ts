import { isApiError, type Translate } from '@lerryn/core';

import type { Strings } from '../i18n/useStrings';
import { errorKind } from '../lib/errors';

/** The message for a failed pairing exchange. */
export function pairingErrorMessage(err: unknown, t: Translate, m: Strings['m']): string {
  if (isApiError(err) && err.code === 'rate_limited') return t('auth.errors.rateLimited');
  switch (errorKind(err)) {
    // An invalid, expired or used code, and a server address that does not
    // match the deployment, all answer 401 (contract: exchangePairingCode).
    case 'signedOut':
      return m('scan.errors.codeInvalid');
    case 'offline':
      return m('login.errors.serverUnreachable');
    default:
      return isApiError(err) && err.code === 'validation_failed'
        ? m('scan.errors.codeInvalid')
        : t('auth.errors.generic');
  }
}
