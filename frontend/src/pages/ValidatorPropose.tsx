import { Navigate, useSearchParams } from 'react-router-dom';

/** Old URL /validators/propose (?action=remove&address=): proposing now happens in a modal on the validators page, so redirect there and open it. */
export function ValidatorPropose() {
  const [params] = useSearchParams();
  const q = new URLSearchParams({ propose: params.get('action') === 'remove' ? 'remove' : 'add' });
  const address = params.get('address');
  if (address) q.set('address', address);
  return <Navigate to={`/validators?${q}`} replace />;
}
