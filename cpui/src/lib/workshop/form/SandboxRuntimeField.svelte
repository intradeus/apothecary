<script lang="ts">
	// The workshop's sandbox runtime: what isolates its sandboxes, among the
	// ones an admin approved on the provisioner. Shown only when there is a
	// choice; a pick the provisioner no longer approves, or one that cannot
	// run what the workshop asks for, says so (the engine refuses the save).
	import { api } from '$lib/api';
	import {
		effectiveRuntime,
		runtimeProblem,
		type RuntimeCapabilities,
		type RuntimeOffer
	} from '$lib/domain';
	import RadioGroup, { type ChoiceOption } from '$lib/ui/RadioGroup.svelte';
	import { t, type MessageKey } from '$lib/i18n/index.svelte';
	import FormSub from './FormSub.svelte';

	let {
		runtime = $bindable(),
		docker
	}: {
		/** '' is the provisioner's default. */
		runtime: string;
		/** The workshop asks for Docker in its workspaces. */
		docker: boolean;
	} = $props();

	let offer = $state<RuntimeOffer | null>(null);
	$effect(() => {
		api.system
			.runtimeOffer()
			.then((o) => (offer = o))
			// No offer is no pick: the provisioner's default applies, as before.
			.catch(() => (offer = null));
	});

	const can = (c: RuntimeCapabilities) =>
		[
			c.nestedContainers && t('form.project.rtNested'),
			c.nestedKubernetes && t('form.project.rtKube'),
			c.userNamespaces && t('form.project.rtUserns'),
			c.contained ? t('form.project.rtContained') : t('form.project.rtNodeRoot')
		]
			.filter(Boolean)
			.join(' · ');

	// What each known runtime is for, after what it can run: the capabilities
	// say what fits, this says which to prefer (the lightest that fits).
	const about: Record<string, MessageKey> = {
		sysbox: 'form.project.rtAboutSysbox',
		kata: 'form.project.rtAboutKata',
		privileged: 'form.project.rtAboutPrivileged'
	};
	const describe = (r: { name: string; capabilities: RuntimeCapabilities }) =>
		[can(r.capabilities), about[r.name] && t(about[r.name])].filter(Boolean).join('. ');

	const options = $derived.by<ChoiceOption<string>[]>(() => {
		if (!offer) return [];
		const o = offer;
		const def = o.approved.find((r) => r.name === o.default);
		const out: ChoiceOption<string>[] = [
			{
				value: '',
				label: t('form.project.rtDefault', { name: o.default }),
				detail: def ? describe(def) : undefined
			},
			...o.approved
				.filter((r) => r.name !== o.default)
				.map((r) => ({ value: r.name, label: r.name, detail: describe(r) }))
		];
		// A pick no longer approved stays visible, so the form shows what is saved.
		if (runtime && !o.approved.some((r) => r.name === runtime)) {
			out.push({ value: runtime, label: runtime, detail: t('form.project.rtWithdrawn') });
		}
		return out;
	});
	const problem = $derived(offer?.offered ? runtimeProblem(offer, runtime, docker) : null);
</script>

{#if offer?.offered && (offer.approved.length > 1 || runtime)}
	<FormSub>
		<h3>{t('form.project.runtime')}</h3>
		<RadioGroup bind:value={runtime} {options} label={t('form.project.runtime')} />
		{#if problem}
			<div class="error small">
				{problem === 'noDocker'
					? t('form.project.rt_noDocker', { name: effectiveRuntime(offer, runtime) })
					: t('form.project.rt_notApproved', { name: effectiveRuntime(offer, runtime) })}
			</div>
		{/if}
		<div class="help">{t('form.project.runtimeHelp')}</div>
	</FormSub>
{:else if offer?.error}
	<FormSub>
		<h3>{t('form.project.runtime')}</h3>
		<div class="error small">{offer.error}</div>
	</FormSub>
{/if}
