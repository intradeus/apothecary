<script lang="ts">
	let { id, path }: { id: string; path: string } = $props();
</script>

<svg><path data-testid="edge-{id}" d={path} /></svg>
