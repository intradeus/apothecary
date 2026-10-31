<script lang="ts">
	// A chain's panel: the main chain, a watcher or an outcome — what opens
	// it, how it starts and ends, and what it runs.
	import type { Selection } from '$lib/flow/FlowGraph.svelte';
	import type { SignalSpec } from '$lib/domain';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import Select from '$lib/ui/Select.svelte';
	import OptionalDuration from '$lib/ui/OptionalDuration.svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import { durationParams, isParamRef, policyParams } from '$lib/flow/params';
	import AddStageChips from '$lib/flow/inspector/AddStageChips.svelte';
	import Caption from '$lib/flow/inspector/Caption.svelte';
	import PanelHead from '$lib/flow/inspector/PanelHead.svelte';
	import {
		addKinds,
		kindHue,
		observedSignals,
		renamed,
		setOperatesOn
	} from '$lib/flow/inspector/inspector';
	import {
		ABORT,
		DONE,
		MAIN,
		MANUAL,
		OPERATES_ON,
		START_POLICIES,
		appendStage,
		chainSequence,
		fires,
		hasMain,
		setChainSequence,
		stagesOf,
		type DocPhase,
		type FlowDoc,
		type StageKind
	} from '$lib/domain/flowdoc';

	let {
		doc,
		phase: ph,
		signals,
		onchange,
		onselect,
		onopen,
		ondelete
	}: {
		doc: FlowDoc;
		phase: DocPhase;
		signals: SignalSpec[];
		onchange: () => void;
		onselect: (sel: Selection, how?: 'rename') => void;
		onopen: (sel: Selection) => void;
		ondelete: (ph: DocPhase) => void;
	} = $props();

	const firing = $derived(fires(ph.on));
	const observed = $derived(observedSignals(signals));
	const phaseSig = $derived(signals.find((g) => g.slug === ph.on.signal));
	const phaseStageCount = $derived(doc.stages.filter((x) => x.phase === ph.id).length);

	// renamePhase renames a watcher or an outcome and every stage that
	// belongs to it. The main chain is `main`. Like every rename here, it
	// says whether it took: a refused one puts the field back.
	function renamePhase(oldId: string, newId: string): boolean {
		newId = newId.trim();
		const ph = doc.phases?.find((p) => p.id === oldId);
		if (!ph || ph.kind === 'main' || !newId || newId === oldId || newId === MAIN) return false;
		if (!/^[a-z][a-z0-9_-]*$/.test(newId) || doc.phases?.some((p) => p.id === newId)) return false;
		ph.id = newId;
		for (const s of doc.stages) if (s.phase === oldId) s.phase = newId;
		onchange();
		onselect({ kind: 'phase', id: newId }, 'rename');
		return true;
	}

	// An outcome's then is its chain's end: the last stage's success goes
	// there unless it says otherwise.
	function setThen(ph: DocPhase, then: string) {
		const chain = doc.stages.filter((x) => x.phase === ph.id);
		const last = chain[chain.length - 1];
		if (last && last.on_success === (ph.then ?? DONE)) last.on_success = then;
		ph.then = then;
		onchange();
	}

	// What a watcher or an outcome runs: stages of its own, or a sequence in
	// their place (`sequence: X`) — one chain written once, run from several
	// places.
	const phaseRuns = $derived(ph.kind !== 'main' ? (chainSequence(doc, ph) ?? '') : '');
	async function setRuns(ph: DocPhase, id: string) {
		const own = stagesOf(doc, ph.id);
		if (own.length && !chainSequence(doc, ph)) {
			const ok = await confirmDialog({
				title: t('flow.insp.runsReplaceTitle', { id: ph.id }),
				body: [
					th('flow.insp.runsReplaceBody', { id: ph.id, count: own.length }),
					t('flow.insp.deleteNote')
				].join(' '),
				action: t('flow.insp.runsReplace')
			});
			if (!ok) return;
		}
		setChainSequence(doc, ph.id, id || undefined);
		onchange();
	}

	// A stage is added at the end of the chain, wired in after its last stage.
	function addToChain(type: StageKind) {
		const s = appendStage(doc, type, ph.id);
		onchange();
		onopen({ kind: 'stage', id: s.id });
	}
</script>

<PanelHead id={ph.kind !== 'main' ? ph.id : undefined}>
	{#snippet lead()}
		<span class="pill sm flat sq mono tint-{kindHue(ph)}">{t(`flow.insp.kind.${ph.kind}`)}</span>
	{/snippet}
	<span class="faint tiny"
		>{ph.kind === 'outcome' && !phaseStageCount
			? t('flow.insp.phaseThen', { then: ph.then ?? DONE })
			: t('flow.insp.phaseStages', { count: phaseStageCount })}</span
	>
	{#if ph.kind !== 'main'}
		<button
			class="btn-danger btn-sm"
			onclick={() => ondelete(ph)}
			title={t(ph.kind === 'outcome' ? 'flow.insp.deleteOutcome' : 'flow.insp.deleteWatcher')}
			>{t('flow.insp.delete')}</button
		>
	{/if}
</PanelHead>
{#if ph.kind === 'main'}
	<div class="help">{t('flow.insp.mainHelp')}</div>
	<div class="field">
		<label for="ph-operates">{t('flow.insp.operatesOn')}</label>
		<Select
			id="ph-operates"
			value={doc.operates_on ?? 'ticket'}
			options={OPERATES_ON.map((r) => ({
				value: r,
				label: t(`flow.insp.operatesOn.${r}`)
			}))}
			onchange={(v) => {
				setOperatesOn(doc, v);
				onchange();
			}}
		/>
		<div class="help">{t('flow.insp.operatesOnHelp')}</div>
	</div>
	{@render addTo()}
{:else}
	<div class="help">
		{t(ph.kind === 'outcome' ? 'flow.insp.outcomeHelp' : 'flow.insp.watcherHelp')}
	</div>
	<div class="field">
		<label for="ph-id">{t('flow.insp.id')}</label>
		<input
			id="ph-id"
			class="mono"
			value={ph.id}
			onchange={(e) => renamed(e.currentTarget, ph.id, renamePhase(ph.id, e.currentTarget.value))}
		/>
		<div class="help">{t('flow.insp.phaseRenameHelp')}</div>
	</div>
	<div class="field">
		<label for="ph-desc">{t('flow.insp.description')}</label>
		<input
			id="ph-desc"
			value={ph.description ?? ''}
			oninput={(e) => {
				ph.description = e.currentTarget.value || undefined;
				onchange();
			}}
		/>
	</div>
	<div class="field">
		<label for="ph-on">{t(ph.kind === 'outcome' ? 'flow.insp.firesOn' : 'flow.insp.opensOn')}</label
		>
		<Select
			id="ph-on"
			value={ph.on.signal}
			options={[
				...(ph.kind === 'watcher' ? [{ value: MANUAL, label: t('flow.insp.opensOn.manual') }] : []),
				...observed.map((sig) => ({ value: sig.slug, label: sig.title })),
				...(firing && !observed.some((g) => g.slug === ph.on.signal)
					? [{ value: ph.on.signal, label: ph.on.signal }]
					: [])
			]}
			onchange={(v) => {
				ph.on.signal = v;
				const sig = signals.find((g) => g.slug === ph.on.signal);
				if (!fires(ph.on)) {
					// A person's act is the start: no policy, nothing to debounce.
					ph.on.start = undefined;
					ph.on.quiet_for = undefined;
				} else if (!sig?.debounced) {
					ph.on.quiet_for = undefined;
				}
				onchange();
			}}
		/>
		<div class="help">
			{ph.on.signal === MANUAL ? t('flow.insp.opensOnHelp.manual') : (phaseSig?.description ?? '')}
		</div>
	</div>
	{#if firing}
		<div class="field">
			<label for="ph-start">{t('flow.insp.start')}</label>
			<Select
				id="ph-start"
				value={ph.on.start ?? 'auto'}
				options={[
					...START_POLICIES.map((p) => ({ value: p, label: t(`flow.insp.start.${p}`) })),
					...policyParams(doc).map((p) => ({ value: '$' + p, label: '$' + p })),
					...(isParamRef(ph.on.start) && !policyParams(doc).includes(ph.on.start.slice(1))
						? [{ value: ph.on.start, label: ph.on.start }]
						: [])
				]}
				onchange={(v) => {
					ph.on.start = v === 'auto' ? undefined : v;
					onchange();
				}}
			/>
			<div class="help">
				{t(ph.kind === 'outcome' ? 'flow.insp.startHelpOutcome' : 'flow.insp.startHelp')}
			</div>
		</div>
		{#if phaseSig?.debounced || ph.on.quiet_for}
			<div class="field">
				<label for="ph-quiet">{t('flow.insp.quietFor')}</label>
				<OptionalDuration
					id="ph-quiet"
					label={t('flow.insp.quietFor')}
					bind:value={ph.on.quiet_for}
					empty={t('flow.insp.noQuiet')}
					fallback="1h"
					params={durationParams(doc)}
					{onchange}
				/>
				<div class="help">{t('flow.insp.quietForHelp')}</div>
			</div>
		{/if}
	{/if}
	{#if ph.kind === 'outcome'}
		<div class="field">
			<label for="ph-then">{t('flow.insp.then')}</label>
			<Select
				id="ph-then"
				value={ph.then ?? DONE}
				options={[
					{ value: DONE, label: t('flow.insp.then.done') },
					{ value: ABORT, label: t('flow.insp.then.abort') }
				]}
				onchange={(v) => setThen(ph, v)}
			/>
			<div class="help">
				{t(phaseStageCount ? 'flow.insp.outcomeStagesHelp' : 'flow.insp.phaseThenHelp')}
			</div>
		</div>
	{:else}
		<div class="field">
			<label for="ph-max">{t('flow.insp.phaseMax')}</label>
			<input
				id="ph-max"
				class="mono"
				list="flow-int-params"
				value={ph.on.max ?? ''}
				placeholder={t('flow.insp.unlimited')}
				oninput={(e) => {
					const v = e.currentTarget.value.trim();
					ph.on.max = v === '' ? undefined : /^-?\d+$/.test(v) ? Number(v) : v;
					onchange();
				}}
			/>
			<div class="help">{t('flow.insp.phaseMaxHelp')}</div>
		</div>
	{/if}
	{@const choices = [...(hasMain(doc) ? [MAIN] : []), ...(doc.sequences ?? []).map((q) => q.id)]}
	<div class="field">
		<label for="ph-runs">{t('flow.insp.runs')}</label>
		<!-- Bound through phaseRuns: a replacement refused leaves the field on
		     what the chain runs. -->
		<Select
			id="ph-runs"
			bind:value={() => phaseRuns, (v) => void setRuns(ph, v)}
			options={[
				{
					value: '',
					label:
						ph.kind === 'outcome' && !phaseStageCount
							? t('flow.insp.runsNothing')
							: t('flow.insp.runsOwn', { count: phaseStageCount })
				},
				...choices.map((id) => ({
					value: id,
					label: id === MAIN ? t('flow.insp.runsMain') : t('flow.insp.runsSequence', { id })
				}))
			]}
		/>
		<div class="help">
			{choices.length ? t('flow.insp.runsHelp') : t('flow.insp.runsHelpNone')}
		</div>
	</div>
	{@render addTo()}
{/if}

<!-- A chain's own way to grow: a stage of each kind, appended after its
     last stage and wired in. -->
{#snippet addTo()}
	<Caption text={t('flow.insp.addToChain')} />
	<AddStageChips kinds={addKinds(doc, { phase: ph.id })} onadd={addToChain} />
	<div class="help">{t('flow.insp.addToChainHelp')}</div>
{/snippet}
