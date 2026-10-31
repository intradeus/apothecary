// What a catch shows the user. The engine's errors arrive as Error (see
// $lib/api/client); anything else is a bug, shown as itself.

export const errorMessage = (e: unknown): string => (e instanceof Error ? e.message : String(e));
