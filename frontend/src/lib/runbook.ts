/**
 * Onboarding runbook: two single-file HTML templates, Chinese and English (src/runbook/; see tools/runbook/ for sources and how to regenerate),
 * filled in the browser with this node's values at download time. The templates are dynamically imported and stay out of the main bundle.
 * The join code is not written into the file: it is a one-time secret and documents get forwarded and archived; the install command uses a placeholder and the real code is copied from the screen.
 */
import i18n from '@/i18n';
import { api } from '@/api/client';
import type { Way } from '@/components/BesuRequirements';

/**
 * Online install command: download install.sh, verify it against the SHA-256 given by the platform, and only then run it as root. Chained with && so a failed download or check stops before sudo;
 * echo | sha256sum -c - avoids bash-only syntax. The join code and checksum go into variables on their own lines first, so the following command is short enough not to scroll.
 * When token is omitted a placeholder is used (for the runbook).
 */
export function installCommand({ origin, inbound, token, docker, sha, reEnroll = false }: { origin: string; inbound: string; token: string; docker: boolean; sha: string; reEnroll?: boolean }) {
  return [
    `TOKEN='${token}'`,
    `SHA=${sha}`,
    `curl -fsSL ${origin}/install.sh -o install.sh \\`,
    `  && echo "$SHA  install.sh" | sha256sum -c - \\`,
    `  && sudo bash install.sh --platform ${inbound} --token "$TOKEN" \\`,
    `       ${docker ? '--runtime docker ' : ''}--platform-http ${origin}${reEnroll ? ' --re-enroll' : ''}`,
  ].join('\n');
}

/** Command for the offline bundle: verify the bundle -> extract -> install.sh --local (the agent comes from the bundle, verified against SHA256SUMS). */
export function offlineInstallCommand({ file, sha, inbound, token, docker }: { file: string; sha: string; inbound: string; token: string; docker: boolean }) {
  const dir = file.replace(/-linux\.tar\.gz$/, '');
  return [
    `TOKEN='${token}'`,
    `SHA=${sha}`,
    `echo "$SHA  ${file}" | sha256sum -c - \\`,
    `  && tar xzf ${file} && cd ${dir} \\`,
    `  && sudo bash install.sh --local --platform ${inbound} --token "$TOKEN"${docker ? ' --runtime docker' : ''}`,
  ].join('\n');
}

export type RunbookValues = {
  nodeId: string;
  nodeName?: string;
  platformUrl: string;
  inbound: string;
  docker: boolean;
  rpcPort: number;
  metricsPort: number;
  /** Full config in all three forms (assembled by BesuRequirements) */
  configs: Record<Way, string>;
};

const esc = (s: string) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

/** Pure function: replace {{KEY}} in the template with the escaped value; unknown keys are left as is (so a template / code mismatch is visible). */
export function fillRunbook(tpl: string, vals: Record<string, string>) {
  return tpl.replace(/\{\{([A-Z_]+)\}\}/g, (m, k: string) => (k in vals ? esc(vals[k]) : m));
}

export async function downloadRunbook(v: RunbookValues) {
  const lang = i18n.language === 'en' ? 'en' : 'zh';
  const tpl = (lang === 'en' ? await import('@/runbook/runbook.en.html?raw') : await import('@/runbook/runbook.zh.html?raw')).default;
  // Still produce the runbook without the checksum: the command keeps a placeholder and the real command is copied from the screen
  const installShSha256 = await api.getAgentBundle().then((b) => b.installShSha256, () => i18n.t('besuReq.runbookSha', { lng: lang }));
  const html = fillRunbook(tpl, {
    NODE_ID: v.nodeId,
    NODE_NAME: v.nodeName ?? '',
    PLATFORM_URL: v.platformUrl,
    RPC_PORT: String(v.rpcPort),
    METRICS_PORT: String(v.metricsPort),
    PUBKEY_URL: `${v.platformUrl}/api/v1/platform/rpc-public-key.pem`,
    CONFIG_TOML: v.configs.toml,
    CONFIG_CLI: v.configs.cli,
    CONFIG_ENV: v.configs.env,
    INSTALL_CMD: installCommand({ origin: v.platformUrl, inbound: v.inbound, token: i18n.t('besuReq.runbookToken', { lng: lang }), docker: v.docker, sha: installShSha256 }),
    GENERATED_AT: new Date().toLocaleString(lang === 'en' ? 'en-US' : 'zh-TW'),
  });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(new Blob([html], { type: 'text/html' }));
  // The filename uses the same name as the button
  a.download = i18n.t('besuReq.runbookFile', { lng: lang, id: v.nodeId });
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href)); // release once the download has started
}
