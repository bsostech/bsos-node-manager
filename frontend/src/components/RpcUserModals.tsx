import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { CaretRight, Check, CheckCircle, Copy } from '@phosphor-icons/react';
import { api } from '@/api/client';
import type { RpcTokenIssued, RpcTokenRequest, RpcUser, RpcUserEvent } from '@/api/schemas';
import { Button } from '@/components/Button';
import { inputCls } from '@/components/Field';
import { ConfirmModal } from '@/components/ConfirmModal';
import { Modal, ModalFoot as Foot, ModalHead as Head, MODAL_SHELL as SHELL } from '@/components/Modal';
import { NsToggle } from '@/components/Switches';
import { Listbox } from '@/components/Listbox';
import { EnterpriseTeaser } from '@/components/EnterpriseTeaser';
import { isEnterprise } from '@/edition';
import { ErrorState, Notice } from '@/components/States';
import { StateLabel } from '@/components/BesuRequirements';
import { copyToClipboard } from '@/lib/clipboard';
import { errMsg } from '@/lib/errors';
import { fmtDateTimeMin } from '@/lib/format';

// Client-side live validation of the username (same pattern as the backend's rpcUsernameRe).
const USERNAME_RE = /^[A-Za-z0-9_-]+$/;
const YEAR_MS = 365 * 86_400_000; // upper bound for a custom expiry, same as the backend's rpcTokenMaxTTL

/** Token issuance records that have not expired yet. Tokens cannot be revoked, so "Valid" depends only on the expiry time. */
export const isValidToken = (e: RpcUserEvent) => e.kind === 'token_issued' && Date.parse(e.expiresAt ?? '') > Date.now();
export const validTokens = (events: RpcUserEvent[]) => events.filter(isValidToken);
const latestExpiry = (tokens: RpcUserEvent[]) =>
  tokens.reduce((m, e) => (Date.parse(e.expiresAt!) > Date.parse(m) ? e.expiresAt! : m), tokens[0].expiresAt!);

export function PermChips({ perms }: { perms: string[] }) {
  return (
    <div className="flex flex-wrap gap-1">
      {perms.map((p) => <span key={p} className="rounded-md bg-elevated px-1.5 py-0.5 font-mono text-2xs text-ink-2">{p}</span>)}
    </div>
  );
}

/** Shared modal header: title + close button. */
const ErrLine = ({ msg }: { msg: string }) => <span role="alert" className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>{msg}</span>;

// ── Issue token ─────────────────────────────────────────────

type Ttl = NonNullable<RpcTokenRequest['ttl']>;
const TTL_MS: Record<Ttl, number> = { '1h': 3_600_000, '24h': 86_400_000, '7d': 7 * 86_400_000, '30d': 30 * 86_400_000, '365d': YEAR_MS };
type TtlPick = Ttl | 'custom';
// datetime-local takes local time as yyyy-MM-ddTHH:mm
const toLocalInput = (ms: number) => new Date(ms - new Date(ms).getTimezoneOffset() * 60_000).toISOString().slice(0, 16);

/** Before issuing, list what this token can do and when it expires; after issuing, show the full token once with one-click copy. */
export function SignTokenModal({ user, onClose }: { user: RpcUser; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [pick, setPick] = useState<TtlPick>('7d');
  const [openedAt] = useState(() => Date.now());
  const [custom, setCustom] = useState(() => toLocalInput(openedAt + 30 * 86_400_000));
  // The token lives only in this component's state, never in the query cache
  const [label, setLabel] = useState('');
  const [signed, setSigned] = useState<RpcTokenIssued | null>(null);
  const [copied, setCopied] = useState(false);

  const customMs = Date.parse(custom); // a yyyy-MM-ddTHH:mm without a time zone is parsed as local time
  const customOk = customMs > Date.now() && customMs <= openedAt + YEAR_MS;
  const expMs = pick === 'custom' ? customMs : openedAt + TTL_MS[pick];

  const sign = useMutation({
    mutationFn: () => api.issueRpcUserToken(user.id, { ...(pick === 'custom' ? { expiresAt: new Date(customMs).toISOString() } : { ttl: pick }), ...(label.trim() ? { label: label.trim() } : {}) }),
    onSuccess: (res) => {
      setSigned(res);
      void qc.invalidateQueries({ queryKey: ['rpcUserEvents', user.id] });
    },
  });

  // After issuing, two more rows: the ID (written into the token's jti, so records can be traced back to this token) and the purpose
  const summary = (perms: string[], exp: string, issued?: RpcTokenIssued) => (
    <dl className="grid grid-cols-[6rem_1fr] gap-x-3 gap-y-2 text-xs">
      {issued && <><dt className="text-ink-3">{t('rpcUsers.tokenId')}</dt><dd className="font-mono text-ink-2">{issued.tokenId}</dd></>}
      {issued?.label && <><dt className="text-ink-3">{t('rpcUsers.label')}</dt><dd className="text-ink-2">{issued.label}</dd></>}
      <dt className="text-ink-3">{t('rpcUsers.methods')}</dt>
      <dd><PermChips perms={perms} /></dd>
      <dt className="text-ink-3">{t('rpcUsers.expires')}</dt>
      <dd className="text-ink-2">{exp}</dd>
    </dl>
  );

  if (signed) {
    return (
      <Modal width="max-w-2xl" contentClass={SHELL} dismissOnOutside={false} onClose={onClose}>
        <Head onClose={onClose} icon={<CheckCircle size={16} weight="fill" aria-hidden="true" style={{ color: 'var(--bsos-status-healthy-fg)' }} />}>
          {t('rpcUsers.signedTitle')}<span className="font-mono">{user.username}</span>
        </Head>
        <div className="flex flex-col gap-4 px-5 py-4">
          <Notice>{t('rpcUsers.once')}</Notice>
          <div className="flex flex-col gap-1.5">
            <span className="text-xs font-medium text-ink">Token</span>
            <div className="flex items-stretch gap-2">
              <code className="block min-w-0 flex-1 break-all rounded-md border border-hair bg-canvas px-3 py-2 font-mono text-xs leading-relaxed text-ink-2">{signed.token}</code>
              <Button
                variant={copied ? 'secondary' : 'primary'}
                className="inline-flex shrink-0 items-center gap-1.5 self-start"
                onClick={async () => setCopied(await copyToClipboard(signed.token))}
              >
                {copied ? <Check size={14} aria-hidden="true" /> : <Copy size={14} aria-hidden="true" />}
                {t(copied ? 'common.copied' : 'common.copy')}
              </Button>
            </div>
          </div>
          {summary(signed.permissions, fmtDateTimeMin(signed.expiresAt), signed)}
        </div>
        <Foot><Button variant="primary" onClick={onClose}>{t('common.done')}</Button></Foot>
      </Modal>
    );
  }

  const ttlOpts: { value: TtlPick; label: string }[] = [...(Object.keys(TTL_MS) as Ttl[]), 'custom' as const].map((v) => ({ value: v, label: t(`rpcUsers.ttl_${v}`) }));
  return (
    <Modal width="max-w-lg" contentClass={SHELL} onClose={onClose}>
      <Head onClose={onClose}>{t('rpcUsers.signTitle')}<span className="font-mono">{user.username}</span></Head>
      <div className="flex flex-col gap-4 px-5 py-4">
        {/* Purpose (optional): lets the record show at a glance whom this token is for and what it is used for */}
        <label className="flex flex-col gap-1.5">
          <span className="text-xs font-medium text-ink">{t('rpcUsers.labelField')}</span>
          <input value={label} onChange={(e) => setLabel(e.target.value)} maxLength={64} placeholder={t('rpcUsers.labelPh')} autoComplete="off" className={inputCls} />
        </label>
        <div className="flex flex-col gap-1.5">
          <span className="text-xs font-medium text-ink">{t('rpcUsers.ttl')}</span>
          {/* Validity uses a dropdown; choosing "Custom" puts the date-time field right next to it instead of opening another popup */}
          <div className="flex flex-wrap items-center gap-2">
            <Listbox width="w-32" value={pick} options={ttlOpts} onChange={setPick} aria-label={t('rpcUsers.ttl')} />
            {pick === 'custom' && (
              <input
                type="datetime-local"
                value={custom}
                min={toLocalInput(Date.now())}
                max={toLocalInput(openedAt + YEAR_MS)}
                onChange={(e) => setCustom(e.target.value)}
                aria-label={t('rpcUsers.expires')}
                className={`${inputCls} w-fit`}
              />
            )}
          </div>
          {pick === 'custom' && (
            <span className={`text-xs ${customOk ? 'text-ink-3' : ''}`} style={customOk ? undefined : { color: 'var(--bsos-status-error-fg)' }}>
              {t('rpcUsers.customMax')}
            </span>
          )}
        </div>
        {summary(user.permissions, Number.isNaN(expMs) ? '—' : pick === 'custom'
          ? fmtDateTimeMin(new Date(expMs).toISOString())
          : t('rpcUsers.expiresIn', { time: fmtDateTimeMin(new Date(expMs).toISOString()), ttl: t(`rpcUsers.ttl_${pick}`) }))}
        {sign.isError && <ErrLine msg={errMsg(sign.error) || t('error.title')} />}
      </div>
      <Foot>
        <Button onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" disabled={sign.isPending || (pick === 'custom' && !customOk)} onClick={() => sign.mutate()}>
          {t(sign.isPending ? 'rpcUsers.signing' : 'rpcUsers.signDo')}
        </Button>
      </Foot>
    </Modal>
  );
}

// ── Add / edit permissions ────────────────────────────────────────

/**
 * Add a user or edit permissions (user set = edit). When editing, events is used to count "how many valid tokens still carry the old permissions".
 * taken: existing usernames, so duplicate names are blocked up front when adding (the backend also returns 409).
 */
/** Add an access identity. The callable methods cannot be changed after creation (tokens under one identity never differ in permissions); for different permissions create another one. */
export function RpcUserFormModal({ taken = [], onClose }: { taken?: string[]; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const catalogQ = useQuery({ queryKey: ['rpcCatalog'], queryFn: api.getRpcMethodCatalog });
  const [name, setName] = useState('');
  const [perms, setPerms] = useState<string[]>([]);
  const [open, setOpen] = useState<Record<string, boolean>>({});

  const save = useMutation({
    mutationFn: () => api.createRpcUser({ username: name, permissions: perms }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['rpcUsers'] });
      onClose();
    },
  });

  const nameOk = USERNAME_RE.test(name);
  const dup = taken.includes(name);
  const can = nameOk && !dup && perms.length > 0 && catalogQ.isSuccess && !save.isPending;
  // A whole group (ns:*) covers every method in it: selecting it clears the individual picks in that group and shows all of them as selected; deselecting the group clears them too
  const toggleAll = (ns: string) => setPerms((prev) => {
    const all = `${ns}:*`;
    const rest = prev.filter((x) => !x.startsWith(`${ns}:`));
    return prev.includes(all) ? rest : [...rest, all];
  });
  // A single method: clicking it while the whole group is selected = deselect only this one (switching to listing the other methods individually); once all are picked individually it collapses back into the whole group
  const toggleOne = (ns: string, method: string, methods: string[]) => setPerms((prev) => {
    const all = `${ns}:*`;
    const p = `${ns}:${method}`;
    if (prev.includes(all)) return [...prev.filter((x) => x !== all), ...methods.filter((m) => m !== method).map((m) => `${ns}:${m}`)];
    const next = prev.includes(p) ? prev.filter((x) => x !== p) : [...prev, p];
    return methods.every((m) => next.includes(`${ns}:${m}`)) ? [...next.filter((x) => !x.startsWith(`${ns}:`)), all] : next;
  });

  return (
    <Modal width="max-w-3xl" contentClass={SHELL} dismissOnOutside={false} autoFocusChild onClose={onClose}>
      <Head onClose={onClose}>{t('rpcUsers.add')}</Head>
      <div className="flex flex-col gap-4 px-5 py-4">
        <label className="flex flex-col gap-1.5">
            <span className="text-xs font-medium text-ink">{t('rpcUsers.username')}</span>
            <input
              value={name}
              onChange={(e) => setName(e.target.value.trim())}
              placeholder={t('rpcUsers.usernamePh')}
              spellCheck={false}
              autoComplete="off"
              autoFocus
              className={`${inputCls} font-mono`}
            />
            {name && !nameOk && <span className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>{t('rpcUsers.usernameBad')}</span>}
            {dup && <span className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>{t('rpcUsers.usernameDup')}</span>}
          </label>
        <div className="flex flex-col gap-2">
          <span className="flex flex-wrap items-baseline gap-x-2">
            <span className="text-xs font-medium text-ink">{t('rpcUsers.methods')}</span>
            <span className="text-2xs text-ink-3">{t('rpcUsers.permsFixed')}</span>
          </span>
          {catalogQ.isError && <ErrorState onRetry={() => void catalogQ.refetch()} />}
          {catalogQ.data?.map((ns) => {
            const all = `${ns.namespace}:*`;
            const isOpen = !!open[ns.namespace];
            return (
              <div key={ns.namespace} className="flex flex-wrap items-center gap-1.5">
                <button
                  type="button"
                  onClick={() => setOpen((o) => ({ ...o, [ns.namespace]: !isOpen }))}
                  aria-label={ns.namespace}
                  aria-expanded={isOpen}
                  className="text-ink-3 transition-colors hover:text-ink-2"
                >
                  <CaretRight size={12} aria-hidden="true" className={`transition-transform ${isOpen ? 'rotate-90' : ''}`} />
                </button>
                <NsToggle label={all} on={perms.includes(all)} onClick={() => toggleAll(ns.namespace)} />
                {isOpen && ns.methods.map((m) => {
                  const p = `${ns.namespace}:${m.method}`;
                  // Expanding this group's methods: fades in together with the arrow
                  return <NsToggle key={p} className="fade-in" label={m.method} on={perms.includes(all) || perms.includes(p)} onClick={() => toggleOne(ns.namespace, m.method, ns.methods.map((x) => x.method))} />;
                })}
              </div>
            );
          })}
        </div>
        {/* Open-source edition: tokens are accepted by every auth-enabled node of our org and cannot be restricted to nodes; restricting is a commercial-edition feature, so a grayed-out field is left here as a preview */}
        {!isEnterprise && (
          <div className="flex flex-col gap-1.5">
            <span className="text-xs font-medium text-ink">{t('rpcUsers.nodeScope')}</span>
            <span className="flex items-center gap-2">
              <Listbox aria-label={t('rpcUsers.nodeScope')} value="all" onChange={() => {}} options={[{ value: 'all', label: t('rpcUsers.nodeScopeAll') }]} width="w-56" disabled />
              <EnterpriseTeaser slug="rpc-node-scope" tagOnly />
            </span>
          </div>
        )}
        {save.isError && <ErrLine msg={errMsg(save.error) || t('error.title')} />}
      </div>
      <Foot>
        {perms.length === 0 && <span className="text-xs text-ink-3">{t('rpcUsers.needPerm')}</span>}
        <Button onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" disabled={!can} onClick={() => save.mutate()}>{t('rpcUsers.create')}</Button>
      </Foot>
    </Modal>
  );
}

// ── Delete ───────────────────────────────────────────────────

export function DeleteRpcUserModal({ user, events, onClose, onDeleted }: { user: RpcUser; events: RpcUserEvent[]; onClose: () => void; onDeleted: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const del = useMutation({
    mutationFn: () => api.deleteRpcUser(user.id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['rpcUsers'] });
      qc.removeQueries({ queryKey: ['rpcUserEvents', user.id] });
      onDeleted();
    },
  });
  const valid = validTokens(events);
  const body = t('rpcUsers.delBody', { name: user.username })
    + (valid.length ? t('rpcUsers.delBodyValid', { count: valid.length, time: fmtDateTimeMin(latestExpiry(valid)) }) : '');
  return (
    <ConfirmModal
      title={t('rpcUsers.delTitle')}
      body={del.isError ? errMsg(del.error) || t('error.title') : body}
      confirmLabel={t('rpcUsers.delete')}
      danger
      busy={del.isPending}
      onConfirm={() => del.mutate()}
      onCancel={onClose}
    />
  );
}

// ── Record details ───────────────────────────────────────────────

export function TokenState({ e }: { e: RpcUserEvent }) {
  const { t } = useTranslation();
  const valid = isValidToken(e);
  return <StateLabel tone={valid ? 'healthy' : 'neutral'} label={t(valid ? 'rpcUsers.valid' : 'rpcUsers.expired')} />;
}

export function RpcEventModal({ user, e, onClose }: { user: RpcUser; e: RpcUserEvent; onClose: () => void }) {
  const { t } = useTranslation();
  const row = (k: string, v: React.ReactNode) => <><dt className="text-ink-3">{k}</dt><dd className="text-ink-2">{v}</dd></>;
  const who = <span className="font-mono">{user.username}</span>;
  const actor = e.actor || '—';
  let body: React.ReactNode;
  if (e.kind === 'token_issued') {
    body = (
      <>
        <dl className="grid grid-cols-[6rem_1fr] gap-x-3 gap-y-2 text-xs">
          {row(t('rpcUsers.fUser'), who)}
          {row(t('rpcUsers.tokenId'), <span className="font-mono">{e.tokenId ?? '—'}</span>)}
          {row(t('rpcUsers.label'), e.label || '—')}
          {row(t('rpcUsers.fIssuedAt'), fmtDateTimeMin(e.at))}
          {row(t('rpcUsers.fIssuer'), actor)}
          {row(t('rpcUsers.expires'), fmtDateTimeMin(e.expiresAt))}
          {row(t('rpcUsers.colState'), <TokenState e={e} />)}
          {row(t('rpcUsers.fPerms'), <PermChips perms={e.permissions} />)}
        </dl>
      </>
    );
  } else {
    body = (
      <dl className="grid grid-cols-[6rem_1fr] gap-x-3 gap-y-2 text-xs">
        {row(t('rpcUsers.fUser'), who)}
        {row(t('rpcUsers.colAt'), fmtDateTimeMin(e.at))}
        {row(t('rpcUsers.colActor'), actor)}
        {row(t('rpcUsers.fPerms'), <PermChips perms={e.permissions} />)}
      </dl>
    );
  }
  return (
    <Modal width="max-w-xl" contentClass={SHELL} onClose={onClose}>
      <Head onClose={onClose}>{t(`rpcUsers.evTitle.${e.kind}`)}</Head>
      <div className="flex flex-col gap-4 px-5 py-4">{body}</div>
      {/* Info-only modals also get a close button at the bottom (the same one site-wide) */}
      <Foot><Button onClick={onClose}>{t('common.close')}</Button></Foot>
    </Modal>
  );
}
