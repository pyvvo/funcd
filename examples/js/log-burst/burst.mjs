// src/burst.ts
function handle(_ctx, event) {
  const items = Math.max(100, event.data?.items ?? 100);
  const batch = event.data?.batch ?? "default";
  let emitted = 0;
  for (let i = 0; i < items; i++) {
    console.log("processing item", { i, batch });
    emitted++;
    if (i % 10 === 0) {
      console.warn("slow item", { i, batch, latencyMs: 120 });
      emitted++;
    }
    if (i % 25 === 0) {
      console.error("item failed", { i, batch, reason: "simulated" });
      emitted++;
    }
  }
  console.log("burst complete", { batch, emitted });
  emitted++;
  return { emitted };
}
export {
  handle
};
