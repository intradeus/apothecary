<script lang="ts">
	// One kind of integration (System → Integrations → <kind>).
	import PortIntegrations from '$lib/system/PortIntegrations.svelte';
	import type { IntegrationPort } from '$lib/core/urls';

	let { data }: { data: { port: IntegrationPort } } = $props();
</script>

{#key data.port}
	<PortIntegrations port={data.port} />
{/key}
