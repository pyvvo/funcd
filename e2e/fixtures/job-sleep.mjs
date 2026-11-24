// ADR-0347: the job lane's handler, which sleeps for its input's ms and answers it.
export async function handle(_context, event) {
  const ms = event.data?.ms ?? 1000;
  await new Promise((resolve) => setTimeout(resolve, ms));
  return { slept: ms };
}
