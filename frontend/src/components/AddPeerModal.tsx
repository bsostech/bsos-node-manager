import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation } from '@tanstack/react-query';
import { X } from '@phosphor-icons/react';
import { api, HttpError } from '@/api/client';
import type { SharedPeers } from '@/api/schemas';
import { Button, RowAction } from '@/components/Button';
import { CopyCmd } from '@/components/CopyButton';
import { inputCls } from '@/components/Field';
import { EnodeInput } from '@/components/EnodeInput';
import { Modal, ModalTitle } from '@/components/Modal';
import { ValidatorTag } from '@/components/ValidatorTag';
import { PubkeyShort } from '@/components/PubkeyShort';
import { SegmentedProgress } from '@/components/SegmentedProgress';
import { errMsg } from '@/lib/errors';
import { enodeHostPort } from '@/lib/format';

// The backend accepts only IP literals as host (enode.Parse); same rule here so mistakes are flagged immediately
const ENODE_RE = /^enode:\/\/([0-9a-fA-F]{128})@(\d{1,3}(?:\.\d{1,3}){3}):(\d{1,5})(\?discport=\d+)?$/;
const addrOk = (v: string) => {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3}):(\d{1,5})$/.exec(v.trim());
  return !!m && m.slice(1, 5).every((o) => +o <= 255) && +m[5] >= 1 && +m[5] <= 65535;
};

// query: the ?discport=N carried by a pasted enode, sent to the backend as-is (the backend keeps it); peers picked from admin_peers are mostly discport=0, so none is sent
type Item = { pubkey: string; addr: string; query?: string; name: string; validator?: boolean };

/**
 * Add peers (two steps): (1) pick from candidates this org's nodes are already connected to, or paste enodes; (2) confirm the name
 * and target ip:port one by one, with the full public key shown for character-by-character comparison.
 * Names are stored in peer_names (keyed by address); the frontend cannot derive the address of a pasted enode, so it is added
 * to the list first and the name is saved using the address the add returns.
 */
export function AddPeerModal({ data, sharedCount, onClose, onDone }: { data: SharedPeers; sharedCount: number; onClose: () => void; onDone: () => void }) {
  const { t } = useTranslation();
  const [step, setStep] = useState(1);
  const [mode, setMode] = useState<'pick' | 'paste'>(data.candidates.length ? 'pick' : 'paste');
  const [picks, setPicks] = useState<Set<string>>(new Set());
  const [raw, setRaw] = useState('');
  const [items, setItems] = useState<Item[]>([]);

  const inList = new Map(data.peers.map((p) => [p.pubkey, p]));
  // Pasting enodes: Enter adds this one to the list and clears the input, ready for the next node
  const [pasteList, setPasteList] = useState<Item[]>([]);
  const check = (): { err: string } | { item: Item } | null => {
    const v = raw.trim();
    if (!v) return null;
    const m = ENODE_RE.exec(v);
    if (!m || !addrOk(`${m[2]}:${m[3]}`)) return { err: t('addPeer.badEnode') };
    const pubkey = m[1].toLowerCase();
    const dup = inList.get(pubkey);
    if (dup) return { err: t('addPeer.dup', { name: dup.name ?? t('peers.unnamed') }) };
    if (pasteList.some((x) => x.pubkey === pubkey)) return { err: t('addPeer.dupInList') };
    const cand = data.candidates.find((c) => c.pubkey === pubkey);
    return { item: { pubkey, addr: `${m[2]}:${m[3]}`, query: m[4], name: cand?.name ?? '', validator: !!cand?.validatorAddress } };
  };
  const pasted = mode === 'paste' ? check() : null;
  const canNext = mode === 'pick' ? picks.size > 0 : pasteList.length > 0 || (!!pasted && 'item' in pasted);
  const queue = () => { if (pasted && 'item' in pasted) { setPasteList((l) => [...l, pasted.item]); setRaw(''); } };

  const next = () => {
    if (mode === 'pick') {
      setItems(data.candidates.filter((c) => picks.has(c.pubkey)).map((c) => ({ pubkey: c.pubkey, addr: enodeHostPort(c.enode), name: c.name ?? '', validator: !!c.validatorAddress })));
    } else {
      setItems([...pasteList, ...(pasted && 'item' in pasted ? [pasted.item] : [])]);
    }
    setStep(2);
  };

  const add = useMutation({
    mutationFn: async () => {
      const address = new Map<string, string>(); // pubkey -> address, from each add's response
      for (const it of items) {
        // 409 = already in the list: when a multi-add failed halfway and is retried, earlier ones are not errors; continue adding the rest and saving names
        const added = await api.createManualPeer(`enode://${it.pubkey}@${it.addr.trim()}${it.query ?? ''}`).catch((e) => {
          if (e instanceof HttpError && e.status === 409) return null;
          // With multiple entries, say which one failed; the earlier successful ones are already added
          throw e instanceof HttpError && items.length > 1 ? new HttpError(e.status, e.code, `${it.addr.trim()}: ${errMsg(e)}`, e.fromPlatform) : e;
        });
        if (added) address.set(it.pubkey, added.address);
      }
      const named = items.filter((it) => it.name.trim());
      // Entries that were already in the list (409) have no response: fetch the list once for their addresses
      if (named.some((it) => !address.has(it.pubkey))) for (const p of (await api.listSharedPeers()).peers) if (!address.has(p.pubkey)) address.set(p.pubkey, p.address);
      for (const it of named) {
        const a = address.get(it.pubkey);
        if (a) await api.setPeerName(a, it.name.trim());
      }
    },
    onSuccess: onDone,
  });
  const setItem = (i: number, patch: Partial<Item>) => setItems((xs) => xs.map((x, j) => (j === i ? { ...x, ...patch } : x)));
  // Validator tag uses the site-wide component (ValidatorTag), not a custom one
  const validatorTag = (v?: boolean) => (v ? <ValidatorTag /> : null);

  return (
    // Same layout as the setup wizard (StepWizard): header (title, progress, step count, close) / content / footer (back, next)
    <Modal width="max-w-2xl" onClose={onClose} dismissOnOutside={false} contentClass="rounded-lg border border-hair bg-surface">
      <div className="flex items-center gap-3 border-b border-hair px-5 py-3">
        <ModalTitle>{t('sharedPeers.add')}</ModalTitle>
        {/* Progress aligned right, right next to the close button (same as StepWizard) */}
        <span className="ml-auto flex items-center gap-2">
          <SegmentedProgress segments={[1, 2].map((k) => (k <= step ? 'done' : 'pending'))} />
          <span className="font-mono text-2xs text-ink-3">{step} / 2</span>
        </span>
        <button type="button" onClick={onClose} aria-label={t('common.close')} className="rounded p-1 text-ink-3 transition-colors hover:bg-elevated hover:text-ink">
          <X size={16} aria-hidden="true" />
        </button>
      </div>

      {step === 1 ? (
        <>
        <div className="flex flex-col gap-3 px-5 py-4">
          {/* Two sources: switched with tabs (same underline style as page tabs), not a segmented control */}
          <div role="tablist" aria-label={t('addPeer.step1')} className="flex gap-1 border-b border-hair">
            {(['pick', 'paste'] as const).map((m) => (
              <button key={m} type="button" role="tab" aria-selected={mode === m} onClick={() => setMode(m)}
                className={`-mb-px border-b-2 px-3 py-2 text-xs transition-colors ${mode === m ? 'border-accent font-medium text-accent' : 'border-transparent text-ink-2 hover:text-ink'}`}>
                {t(m === 'pick' ? 'addPeer.modePick' : 'addPeer.modePaste')}
              </button>
            ))}
          </div>
          {mode === 'pick' ? (
            data.candidates.length === 0 ? (
              <p className="py-8 text-center text-xs text-ink-3">{t('addPeer.noCandidates')}</p>
            ) : (
              <>
                <p className="text-xs text-ink-3">{t('addPeer.candidatesHint')}</p>
                <div className="max-h-[20rem] overflow-auto overscroll-contain rounded-md border border-hair">
                  <table className="w-full text-sm">
                    <thead>
                      <tr className="table-head">
                        {/* Select all: checked when all are checked, indeterminate when some are */}
                        <th className="w-8 px-3">
                          <input
                            type="checkbox"
                            aria-label={t('addPeer.selectAll')}
                            checked={data.candidates.length > 0 && picks.size === data.candidates.length}
                            ref={(el) => { if (el) el.indeterminate = picks.size > 0 && picks.size < data.candidates.length; }}
                            onChange={(e) => setPicks(e.target.checked ? new Set(data.candidates.map((c) => c.pubkey)) : new Set())}
                            className="accent-[var(--bsos-accent)]"
                          />
                        </th>
                        <th className="px-3">{t('sharedPeers.colPeer')}</th>
                        <th className="px-3">{t('sharedPeers.colPubkey')}</th>
                        <th className="px-3">{t('sharedPeers.colAddr')}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {data.candidates.map((c) => {
                        const id = `pick-${c.pubkey.slice(0, 12)}`;
                        return (
                          <tr key={c.pubkey} className="border-b border-hair last:border-0">
                            <td className="px-3 py-2">
                              <input
                                id={id}
                                type="checkbox"
                                checked={picks.has(c.pubkey)}
                                onChange={() => setPicks((s) => { const n = new Set(s); if (!n.delete(c.pubkey)) n.add(c.pubkey); return n; })}
                                className="accent-[var(--bsos-accent)]"
                              />
                            </td>
                            <td className="px-3 py-2">
                              <span className="flex items-center gap-2"><label htmlFor={id} className={`cursor-pointer ${c.name ? 'text-ink' : 'text-ink-3'}`}>{c.name ?? t('peers.unnamed')}</label>{validatorTag(!!c.validatorAddress)}</span>
                            </td>
                            {/* Public key gets its own column (abbreviated + copy full enode); the name column holds only the name and validator tag */}
                            <td className="whitespace-nowrap px-3 py-2"><PubkeyShort pubkey={c.pubkey} copy={c.enode} /></td>
                            <td className="whitespace-nowrap px-3 py-2 font-mono text-xs text-ink-2">{enodeHostPort(c.enode)}</td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              </>
            )
          ) : (
            <>
              <label className="flex flex-col gap-1 text-xs text-ink-3">
                enode
                <EnodeInput value={raw} onChange={setRaw} onEnter={queue} autoFocus invalid={!!pasted && 'err' in pasted} />
              </label>
              {pasted && 'err' in pasted ? <p className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>{pasted.err}</p>
                : <p className="text-xs text-ink-3">{t('addPeer.pasteHint')}</p>}
              {/* Already collected: one row per node, removable */}
              {pasteList.length > 0 && (
                <ul aria-label={t('addPeer.pastedTitle')} className="max-h-[14rem] overflow-y-auto overscroll-contain rounded-md border border-hair">
                  {pasteList.map((it) => (
                    <li key={it.pubkey} className="flex items-center gap-3 border-b border-hair px-3 py-2 text-xs last:border-0">
                      <PubkeyShort pubkey={it.pubkey} copy={`enode://${it.pubkey}@${it.addr}`} />
                      <span className="font-mono text-ink-2">{it.addr}</span>
                      <RowAction danger className="ml-auto" onClick={() => setPasteList((l) => l.filter((x) => x.pubkey !== it.pubkey))}>{t('allowlist.removeBtn')}</RowAction>
                    </li>
                  ))}
                </ul>
              )}
            </>
          )}
        </div>
          {/* Step one has no Back: only Next, aligned right */}
          <div className="flex items-center justify-end border-t border-hair px-5 py-3">
            <Button variant="primary" disabled={!canNext} onClick={next}>{t('common.next')}</Button>
          </div>
        </>
      ) : (
        <>
        <div className="flex flex-col gap-3 px-5 py-4">
          <h3 className="text-sm font-medium text-ink">{t('addPeer.step2')}</h3>
          <div className="flex flex-col gap-3">
            {items.map((it, i) => (
              <div key={it.pubkey} className="flex flex-col gap-3 rounded-md border border-hair px-3 py-3">
                <div className="flex flex-wrap items-center gap-2 text-xs">
                  <span className="text-ink-3">{t('sharedPeers.colPubkey')}</span>
                  {validatorTag(it.validator)}
                </div>
                <CopyCmd value={it.pubkey} />
                <div className="grid gap-3 sm:grid-cols-2">
                  <label className="flex flex-col gap-1 text-xs text-ink-3">
                    {t('sharedPeers.name')}
                    <input value={it.name} onChange={(e) => setItem(i, { name: e.target.value })} maxLength={64} placeholder={t('peers.unnamed')} className={inputCls} />
                  </label>
                  <label className="flex flex-col gap-1 text-xs text-ink-3">
                    {t('addPeer.addr')}
                    <input
                      value={it.addr}
                      onChange={(e) => setItem(i, { addr: e.target.value })}
                      spellCheck={false}
                      aria-invalid={!addrOk(it.addr)}
                      className={`${inputCls} font-mono ${addrOk(it.addr) ? '' : 'border-[var(--bsos-status-error-fg)]'}`}
                    />
                  </label>
                </div>
              </div>
            ))}
          </div>
          {add.isError && <span role="alert" className="break-all text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>{errMsg(add.error) || t('error.title')}</span>}
        </div>
          <div className="flex flex-wrap items-center justify-between gap-2 border-t border-hair px-5 py-3">
            <Button onClick={() => setStep(1)}>{t('common.prev')}</Button>
            <div className="flex items-center gap-2">
              <span className="text-xs text-ink-3">{t('addPeer.pushTo', { count: sharedCount })}</span>
              <Button variant="primary" disabled={add.isPending || !items.every((it) => addrOk(it.addr))} onClick={() => add.mutate()}>
                {items.length > 1 ? t('addPeer.addN', { count: items.length }) : t('addPeer.addOne')}
              </Button>
            </div>
          </div>
        </>
      )}
    </Modal>
  );
}
