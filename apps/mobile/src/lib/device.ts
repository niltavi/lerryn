import type { Schemas } from '@lerryn/api';

export type DeviceInfo = Schemas['DeviceInfo'];

/** The parts of React Native's Platform the device description needs. */
export interface PlatformLike {
  OS: string;
  Version: string | number;
  constants: Record<string, unknown>;
}

const clip = (s: string, n: number) => (s.length > n ? s.slice(0, n) : s);

/**
 * Describes this phone for the account's device list (contract: DeviceInfo).
 * Uses only what React Native's Platform reports, so no extra native module
 * is needed; the name is generic ("iPhone", "Pixel 9") because reading the
 * user-given device name needs an entitlement on iOS.
 */
export function deviceInfo(platform: PlatformLike): DeviceInfo {
  const c = platform.constants;
  const str = (key: string) => (typeof c[key] === 'string' ? (c[key] as string).trim() : '');
  if (platform.OS === 'ios') {
    const name = str('interfaceIdiom') === 'pad' ? 'iPad' : 'iPhone';
    const version = str('osVersion') || String(platform.Version);
    return { name, platform: 'ios', os_version: clip(`${str('systemName') || 'iOS'} ${version}`.trim(), 32) };
  }
  if (platform.OS === 'android') {
    const brand = str('Brand') || str('Manufacturer');
    const model = str('Model');
    const name = model.toLowerCase().startsWith(brand.toLowerCase()) ? model : `${brand} ${model}`.trim();
    const release = str('Release') || String(platform.Version);
    return { name: clip(name || 'Android', 64), platform: 'android', os_version: clip(`Android ${release}`, 32) };
  }
  return { name: clip(platform.OS || 'Mobile', 64), platform: 'other' };
}
