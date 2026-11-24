// ADR-0345: the Function of an engineless kind, which funcd calls with each group's list. It answers every object ok,
// a deleting one once it has nothing left to destroy, and it takes a cause it does not know as any other.
export const handle = async (ctx, event) => {
  const objects = event.data?.objects ?? [];
  return { results: objects.map((o) => ({ name: o.name, ok: true })) };
};
