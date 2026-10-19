<script lang="ts">
	// Stage type chip (DESIGN 3.3).
	let { type }: { type: string } = $props();

	const META: Record<string, [string, string]> = {
		agent: ['agent', 'az'],
		command: ['command', 'grn'],
		human_gate: ['gate', 'amb'],
		gate: ['gate', 'amb'],
		action: ['action', 'vio'],
		wait: ['wait', 'tea']
	};
	const m = $derived(META[type] ?? [type, 'mut']);
</script>

<span class="st {m[1]}">{m[0]}</span>

<style>
	.st {
		display: inline-flex;
		align-items: center;
		padding: 1px 7px;
		border-radius: 6px;
		font-size: 10.5px;
		font-weight: 600;
		white-space: nowrap;
		line-height: 1.5;
	}
	.az {
		background: var(--azBg);
		color: var(--az);
	}
	.grn {
		background: var(--grnBg);
		color: var(--grn);
	}
	.amb {
		background: var(--ambBg);
		color: var(--amb);
	}
	.vio {
		background: var(--vioBg);
		color: var(--vio);
	}
	.tea {
		background: var(--teaBg);
		color: var(--tea);
	}
	.mut {
		background: var(--p2);
		color: var(--mut);
	}
</style>
