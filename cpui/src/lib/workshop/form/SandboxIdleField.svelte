<script lang="ts">
	// The workshop's stop-on-idle: how long a sandbox nobody uses stays up
	// before it is stopped, its checkout and containers kept for the next use.
	// The instance's default, never, or its own minutes. A provisioner whose
	// storage does not outlive a pod, or a runtime whose disks are its VM's,
	// cannot keep a stopped sandbox: the field says why (for the workshop's
	// runtime) and the engine refuses minutes there.
	import { api } from '$lib/api';
	import { fleet } from '$lib/app/fleet.svelte';
	import { IDLE_NEVER, sleepRefusal, type RuntimeOffer } from '$lib/domain';
	import RadioGroup, { type ChoiceOption } from '$lib/ui/RadioGroup.svelte';
	import { t } from '$lib/i18n/index.svelte';

	let {
		idleMinutes = $bindable(),
		runtime = ''
	}: {
		/** 0 the instance's default, IDLE_NEVER never, else minutes. */
		idleMinutes: number;
		/** The workshop's runtime pick, '' the provisioner's default. */
		runtime?: string;
	} = $props();

	let offer = $state<RuntimeOffer | undefined>(undefined);
	$effect(() => {
		api.system
			.runtimeOffer()
			.then((o) => (offer = o))
			.catch(() => (offer = undefined));
	});
	const noSleep = $derived(offer && sleepRefusal(offer, runtime));

	type Mode = 'default' | 'never' | 'after';
	const modeOf = (m: number): Mode => (m === 0 ? 'default' : m < 0 ? 'never' : 'after');
	// The minutes last typed, kept while another mode is picked.
	let minutes = $state(idleMinutes > 0 ? idleMinutes : 30);
	let mode = $state<Mode>(modeOf(idleMinutes));
	$effect(() => {
		idleMinutes = mode === 'default' ? 0 : mode === 'never' ? IDLE_NEVER : minutes;
	});

	const instance = $derived(fleet.limits?.sandbox?.idleMinutes ?? 0);
	const options = $derived<ChoiceOption<Mode>[]>([
		{
			value: 'default',
			label: t('form.project.idleDefault'),
			detail:
				instance > 0
					? t('form.project.idleAfterN', { m: instance })
					: t('form.project.idleNeverWord')
		},
		{ value: 'never', label: t('form.project.idleNever') },
		{ value: 'after', label: t('form.project.idleAfter') }
	]);
	const invalid = $derived(mode === 'after' && !(Number.isInteger(minutes) && minutes > 0));
</script>

<h3>{t('form.project.idle')}</h3>
<RadioGroup bind:value={mode} {options} label={t('form.project.idle')} />
{#if mode === 'after'}
	<input
		class="mono"
		type="number"
		min="1"
		step="1"
		aria-label={t('form.project.idleMinutes')}
		aria-invalid={invalid}
		bind:value={minutes}
	/>
	{#if invalid}<div class="error small">{t('form.project.idleInvalid')}</div>{/if}
	{#if noSleep}<div class="error small">{t('form.project.idleRefused', { why: noSleep })}</div>{/if}
{/if}
<div class="help">
	{noSleep ? t('form.project.idleUnavailable', { why: noSleep }) : t('form.project.idleHelp')}
</div>

<style>
	input {
		width: 8em;
		margin-top: 6px;
	}
</style>
