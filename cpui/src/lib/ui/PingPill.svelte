<script lang="ts" module>
	import { t } from '$lib/i18n/index.svelte';
	/** The hue + label of each ping type (DESIGN 3.5). phase_confirm and select
	 *  are engine-raised, like gates; select is a choice the run needs. */
	export const PING_META: Record<string, { label: string; hue: string; wanted: boolean }> = {
		gate: {
			get label() {
				return t('ui.ping.gate');
			},
			hue: 'success',
			wanted: true
		},
		debug: {
			get label() {
				return t('ui.ping.debug');
			},
			hue: 'attention',
			wanted: true
		},
		phase_confirm: {
			get label() {
				return t('ui.ping.phase_confirm');
			},
			hue: 'success',
			wanted: true
		},
		select: {
			get label() {
				return t('ui.ping.select');
			},
			hue: 'accent-2',
			wanted: true
		},
		clarification: {
			get label() {
				return t('ui.ping.clarification');
			},
			hue: 'attention',
			wanted: false
		},
		blocker: {
			get label() {
				return t('ui.ping.blocker');
			},
			hue: 'danger',
			wanted: false
		},
		permission: {
			get label() {
				return t('ui.ping.permission');
			},
			hue: 'accent',
			wanted: false
		}
	};
	export const pingMeta = (type: string) =>
		PING_META[type] ?? { label: type, hue: 'muted', wanted: false };
</script>

<script lang="ts">
	let { type }: { type: string } = $props();
	const m = $derived(pingMeta(type));
</script>

<span class="pill tint-{m.hue}" title={m.wanted ? t('ui.ping.wanted') : t('ui.ping.raised')}>
	<span class="dot"></span>{m.label}
</span>
