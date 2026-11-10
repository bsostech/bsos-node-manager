/**
 * Fixed data for the "About" dialog. The version and release date come from the server (/me), which is built with them
 * (deploy/version.sh); the source link is the public Core repo.
 */
export const ABOUT = {
  license: 'Apache License 2.0',
  licenseUrl: 'https://www.apache.org/licenses/LICENSE-2.0',
  sourceUrl: 'https://github.com/bsostech/bsos-node-manager',
  copyright: '© Bsos Tech Co., Ltd.',
} as const;
