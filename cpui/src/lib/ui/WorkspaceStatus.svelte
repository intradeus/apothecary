<script lang="ts" module>
	import type { Run } from '$lib/domain';

	export type WsValue =
		| 'live'
		| 'starting'
		| 'provisioning'
		| 'rebuilding'
		| 'destroying'
		| 'error'
		| 'stopped'
		| 'unknown'
		| 'none';

	/** The workspace axis of a run (DESIGN 3.2), reconciled from the run state,
	 *  its env id and the engine's live container status. In-flight operations
	 *  win over the last reconciled docker state. */
	export function wsValue(
		state: Run['state'],
		envId: string | undefined,
		envStatus: Run['envStatus']
	): WsValue {
		if (envStatus === 'rebuilding') return 'rebuilding';
		if (envStatus === 'destroying') return 'destroying';
		if (envStatus === 'error') return 'error';
		if (state === 'provisioning' || envStatus === 'provisioning') return 'provisioning';
		if (!envId) return 'none';
		switch (envStatus) {
			case 'running':
				return 'live';
			case 'starting':
				return 'starting';
			case 'stopped':
				return 'stopped';
			case 'absent':
				return 'none';
			case 'unknown':
				return 'unknown';
			default:
				// Not reconciled yet: assume live rather than flash "no workspace".
				return 'live';
		}
	}
</script>

<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	let {
		state,
		envId,
		envStatus,
		/** Dot only (8px) with the value in a tooltip. */
		compact = false,
		/** Hide the leading "Workspace" label. */
		bare = false
	}: {
		state: Run['state'];
		envId?: string;
		envStatus?: Run['envStatus'];
		compact?: boolean;
		bare?: boolean;
	} = $props();

	const v = $derived(wsValue(state, envId, envStatus));
	const hue = $derived(
		(
			{
				live: 'hue-grn',
				starting: 'hue-emb',
				provisioning: 'hue-emb',
				rebuilding: 'hue-emb',
				destroying: 'hue-gold',
				error: 'hue-ros',
				stopped: 'hue-mut',
				unknown: 'hue-gold',
				none: 'fnt'
			} as const
		)[v]
	);
	// DESIGN 3.2: live glows, stopped is a ring, unknown/none a dashed ring.
	const dot = $derived(
		v === 'live'
			? 'ldot'
			: v === 'stopped'
				? 'ldot hollow'
				: v === 'unknown' || v === 'none'
					? 'ldot dashed'
					: 'ldot flat'
	);
	const word = $derived(v === 'none' ? t('ui.ws.none') : v);
	const tip = $derived(
		v === 'unknown'
			? t('ui.ws.unreachable')
			: envId
				? t('ui.ws.tipEnv', { word, env: envId })
				: t('ui.ws.tip', { word })
	);
	const pulsing = $derived(
		v === 'starting' || v === 'provisioning' || v === 'rebuilding' || v === 'destroying'
	);
</script>

{#if compact}
	<span class="dot-only {hue}" title={tip}><span class={dot} class:pulse={pulsing}></span></span>
{:else}
	<span class="ws {hue}" title={tip}>
		{#if !bare}<span class="lbl">{t('ui.ws.label')}</span>{/if}
		{#if v !== 'none'}<span class={dot} class:pulse={pulsing}></span>{/if}
		<span class="v">{word}</span>
		{#if envId && v !== 'none'}<span class="id mono">{envId}</span>{/if}
	</span>
{/if}

<style>
	.ws {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: var(--fs-label);
		white-space: nowrap;
		min-width: 0;
	}
	.lbl {
		color: var(--mut);
		font-weight: 500;
	}
	.v {
		font-weight: 600;
	}
	.id {
		color: var(--fnt);
		font-size: var(--fs-id);
		overflow: hidden;
		text-overflow: ellipsis;
		max-width: 160px;
	}
	.dot-only {
		display: inline-flex;
		width: 12px;
		height: 12px;
		align-items: center;
		justify-content: center;
	}
	/* The compact dot is 8px (DESIGN 3.2), a touch over the shared 7px. */
	.dot-only .ldot {
		width: 8px;
		height: 8px;
	}
</style>
