<script lang="ts">
	let {
		id = '',
		type,
		position,
		isConnectable = true
	}: { id?: string; type: string; position: string; isConnectable?: boolean } = $props();
</script>

<span
	data-testid="handle"
	data-id={id}
	data-type={type}
	data-position={position}
	data-connectable={isConnectable}
></span>
