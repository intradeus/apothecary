<script lang="ts" module>
	/** One mounted stub: what it was asked to attach to, and its callbacks. */
	export interface TerminalStubView {
		session: string;
		/** The workspace named the session (as a session frame would). */
		named: (id: string) => void;
		/** The shell ended (as an exit frame would). */
		ended: () => void;
		unmounted: boolean;
	}
	/** Every stub mounted, oldest first: a remount adds one. */
	export const mounted: TerminalStubView[] = [];
</script>

<script lang="ts">
	// Terminal without xterm or a socket: its props on show, its callbacks
	// in `mounted` for a test to fire.
	import { onDestroy, untrack } from 'svelte';

	let {
		runId,
		repo = '',
		dir = '',
		session = '',
		legacy = false,
		onsession,
		onended
	}: {
		runId: string;
		repo?: string;
		dir?: string;
		session?: string;
		legacy?: boolean;
		onsession?: (id: string) => void;
		onended?: () => void;
	} = $props();

	const me: TerminalStubView = {
		session: untrack(() => session),
		named: (id) => onsession?.(id),
		ended: () => onended?.(),
		unmounted: false
	};
	mounted.push(me);
	onDestroy(() => (me.unmounted = true));
</script>

<div
	data-testid="terminal"
	data-run={runId}
	data-repo={repo}
	data-dir={dir}
	data-session={session}
	data-legacy={legacy}
></div>
