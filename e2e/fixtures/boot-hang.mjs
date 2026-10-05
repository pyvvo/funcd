// ADR-0160: a handler module that takes 30 s to load, so the env-echo suite can kill its worker while it boots.
await new Promise((resolve) => setTimeout(resolve, 30_000));

export function handle() {
  return { booted: true };
}
