<script lang="ts" module>
	/** The hue + label of each ping type (DESIGN 3.5). phase_confirm and select
	 *  are engine-raised, like gates; select is a choice the run needs. */
	export const PING_META: Record<string, { label: string; hue: string; wanted: boolean }> = {
		gate: { label: 'Gate', hue: 'grn', wanted: true },
		phase_confirm: { label: 'Phase', hue: 'grn', wanted: true },
		select: { label: 'Select', hue: 'tea', wanted: true },
		clarification: { label: 'Clarification', hue: 'amb', wanted: false },
		blocker: { label: 'Blocker', hue: 'ros', wanted: false },
		permission: { label: 'Permission', hue: 'vio', wanted: false }
	};
	export const pingMeta = (type: string) =>
		PING_META[type] ?? { label: type, hue: 'mut', wanted: false };
</script>

<script lang="ts">
	let { type }: { type: string } = $props();
	const m = $derived(pingMeta(type));
</script>

<span
	class="pill t-{m.hue}"
	title={m.wanted ? 'Asked on purpose by the flow' : 'Raised by the agent — counts against the run'}
>
	<span class="dot"></span>{m.label}
</span>
