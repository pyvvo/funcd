// ADR-0344: the handler of a Function on an Addon's runtime, which answers the data it got.
export function handle(context, event) {
  return { echo: event.data };
}
