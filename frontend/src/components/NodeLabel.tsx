/**
 * A node's name is its id (chosen at registration, never changes). nodes.name is a "note" and is not shown alongside it here;
 * it only appears where it has its own place (the note column in my nodes, the node detail subtitle, node settings).
 * The name parameter is kept for existing callers and is not rendered.
 */
export function NodeLabel({ id }: { id: string; name?: string | null }) {
  return <span className="font-mono">{id}</span>;
}
