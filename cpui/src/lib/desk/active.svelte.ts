// The desk on screen, for the shell. The Layout button lives at the bottom of
// the sidebar (DESIGN 6), far from the run page that owns the desk: the page
// registers its desk here while it is mounted, and the button shows only then.

import type { DeskState } from './deskState.svelte';

export const activeDesk = $state<{ desk: DeskState | null }>({ desk: null });
