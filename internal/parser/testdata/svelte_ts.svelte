<script lang="ts">
let count: number = 0
function increment(): void { count++ }
</script>

<button on:click={increment}>+{count}</button>
