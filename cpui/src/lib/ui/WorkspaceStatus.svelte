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
				live: 'grn',
				starting: 'az',
				provisioning: 'az',
				rebuilding: 'az',
				destroying: 'amb',
				error: 'ros',
				stopped: 'mut',
				unknown: 'amb',
				none: 'fnt'
			} as const
		)[v]
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
	<span class="dot-only {hue} {v}" title={tip}><span class="d" class:pulse={pulsing}></span></span>
{:else}
	<span class="ws {hue}" title={tip}>
		{#if !bare}<span class="lbl">{t('ui.ws.label')}</span>{/if}
		{#if v !== 'none'}<span class="d {v}" class:pulse={pulsing}></span>{/if}
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
	.d {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: currentColor;
		flex: none;
	}
	.dot-only {
		display: inline-flex;
		width: 12px;
		height: 12px;
		align-items: center;
		justify-content: center;
	}
	.dot-only .d {
		width: 8px;
		height: 8px;
	}
	.grn {
		color: var(--grn);
	}
	.grn .d {
		box-shadow: 0 0 8px var(--grn);
	}
	.az {
		color: var(--az);
	}
	.amb {
		color: var(--amb);
	}
	.ros {
		color: var(--ros);
	}
	.mut {
		color: var(--mut);
	}
	.fnt {
		color: var(--fnt);
	}
	.ws .lbl {
		color: var(--mut);
	}
	.d.stopped,
	.stopped .d {
		background: transparent;
		border: 1.5px solid currentColor;
	}
	.d.unknown,
	.unknown .d {
		background: transparent;
		border: 1.5px dashed currentColor;
	}
	.none .d {
		background: transparent;
		border: 1px dashed var(--ln2);
	}
</style>
