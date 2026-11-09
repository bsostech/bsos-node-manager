/** All links to the official Besu docs (docs.besu-eth.org) live here; if the docs site moves or changes paths, change only this file. */
const BASE = 'https://docs.besu-eth.org';

export const BESU_DOCS = {
  /** General JSON-RPC methods (eth, net, txpool, admin...) */
  api: `${BASE}/public-networks/reference/api`,
  /** Private-network-only methods (qbft, perm...) */
  privateApi: `${BASE}/private-networks/reference/api`,
  /** The three forms: config.toml, command-line options, environment variables */
  configure: `${BASE}/public-networks/how-to/configure-besu`,
  jsonRpc: `${BASE}/public-networks/how-to/use-besu-api/json-rpc`,
  metrics: `${BASE}/public-networks/how-to/monitor/metrics`,
  logging: `${BASE}/public-networks/how-to/monitor/logging`,
  jwtAuth: `${BASE}/public-networks/how-to/use-besu-api/authenticate#jwt-public-key-authentication`,
  accountAllowlist: `${BASE}/private-networks/how-to/use-local-permissioning#account-allowlisting`,
} as const;
