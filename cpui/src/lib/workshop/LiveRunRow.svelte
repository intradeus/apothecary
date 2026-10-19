<script lang="ts">
	// A run row inside a workshop card (DESIGN 7.2): state dot, title over
	// "state · stage", mono age. A click opens the run.
	import type { Run } from '$lib/domain';
	import { to } from '$lib/guild';
	import { age, fmtAbs, shortId, stateHue, stateLabel } from '$lib/format';

	let { run }: { run: Run } = $props();
	const live = $derived(
		run.state === 'running' || run.state === 'provisioning' || run.state === 'waiting_on_human'
	);
</script>

<a class="lr" href={to.run(run.id)}>
	<span class="d" class:pulse={live} style:--h={stateHue[run.state]}></span>
	<span class="t">
		<span class="ttl ell">{run.title || 'Untitled run'}</span>
		<span class="sub ell"
			><span style:color={stateHue[run.state]}>{stateLabel[run.state].toLowerCase()}</span
			>{#if run.currentStage}{' · '}<span class="mono">{run.currentStage}</span>{/if}</span
		>
	</span>
	<span class="r">
		<span class="mono id">{shortId(run.id)}</span>
		<span class="mono ag" title={fmtAbs(run.updatedAt)}>{age(run.updatedAt)}</span>
	</span>
</a>

<style>
	.lr {
		display: grid;
		grid-template-columns: 8px minmax(0, 1fr) auto;
		gap: 10px;
		align-items: center;
		padding: 8px 14px;
		border-top: 1px solid var(--cLn);
		color: var(--tx);
	}
	.lr:hover {
		background: var(--p1);
		text-decoration: none;
	}
	.d {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--h);
		box-shadow: 0 0 8px var(--h);
	}
	.t {
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 1px;
	}
	.ttl {
		font-size: 12.5px;
		font-weight: 500;
	}
	.sub {
		font-size: 11px;
		color: var(--mut);
	}
	.sub .mono {
		font-size: 10.5px;
	}
	.r {
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		line-height: 1.3;
	}
	.id {
		font-size: 10.5px;
		color: var(--mut);
	}
	.ag {
		font-size: 10.5px;
		color: var(--fnt);
	}
</style>
