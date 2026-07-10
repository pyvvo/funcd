// egress-probe — the ADR-0117 (F81) egress-enforcement probe. It takes a destination { url } and tries to
// open an outbound connection to it (via global fetch), reporting whether the connection was ALLOWED or
// REFUSED. On real containerd with server.network.egress on, every outbound worker TCP is REDIRECTed to
// the in-binary egress gateway (F80→F81), which authorizes egress::connect against the namespace's
// EgressPolicy over the FORWARDER-ATTESTED destination and splices on ALLOW / refuses (403 or RST) on
// DENY. So: a URL whose domain/CIDR:port is in the allow-list ⇒ { ok:true }; anything else ⇒ { ok:false }.
// This is the containerd-lane counterpart to the in-process decision tests in internal/network/egress.
import type { Handler } from '@funcd/shim-nodejs';

/** The destination to probe. `url` is a full URL (https://api.x.com/…), `expectPort` optional. */
export interface FuncInput {
  url: string;
}

/** The probe result: ok=true iff the connection was allowed and completed; else the refusal detail. */
export interface FuncOutput {
  ok: boolean;
  status: number;
  error: string;
}

export const handle: Handler<FuncInput, FuncOutput> = async (context, event) => {
  // The funcd shim Handler is (context, event); the validated input is `event.data` (NOT the first arg).
  const { url } = event.data!;
  try {
    // A short timeout so a DENY that manifests as a hung/RST connection fails fast rather than blocking.
    const res = await fetch(url, { signal: AbortSignal.timeout(5000) });
    return { ok: true, status: res.status, error: '' };
  } catch (e) {
    context.log('egress-probe connect failed', url, String(e));
    return { ok: false, status: 0, error: e instanceof Error ? e.message : String(e) };
  }
};
