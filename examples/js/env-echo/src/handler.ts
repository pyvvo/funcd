// env-echo — a function that echoes the environment injected by its bindings (ADR-0093 spec.config +
// ADR-0057 spec.secrets). It takes NO input (`FuncInput = void` → the ADR-0090 void-input contract
// `{"type":"null"}`, so an invocation with `data: null` / absent data is accepted) and returns the three
// env vars the platform is expected to inject: APP_MODE (from the ConfigMap), API_KEY (from the Secret),
// and SHARED (present in BOTH — the Secret must win, config-then-secrets merge order). This is the
// containerd-lane counterpart to the in-process scenario pkg/funcd/config_secret_e2e_test.go: it proves
// the full path reconcile → resolve → materialize → process shim → running handler on real containerd.
import type { Handler } from '@funcd/shim-nodejs';

/** This function takes no event payload — the void-input contract (ADR-0090). */
export type FuncInput = void;

/** The three injected env vars echoed back: config from the ConfigMap, secret from the Secret, and the
 *  key present in both (the Secret wins). */
export interface FuncOutput {
  config: string;
  secret: string;
  shared: string;
}

export const handle: Handler<FuncInput, FuncOutput> = () => {
  return {
    config: process.env.APP_MODE ?? '',
    secret: process.env.API_KEY ?? '',
    shared: process.env.SHARED ?? '',
  };
};
