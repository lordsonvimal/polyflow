<script>
import { goto } from '$app/navigation'

export function save() { goto('/saved') }
export function deleteItem() { goto('/items') }
export function onSubmit() { goto('/submit') }
</script>

<div class="container">
  <button on:click={save}>Save</button>
  <button on:click={deleteItem}>Delete</button>
  <form on:submit|preventDefault={onSubmit}>
    <input type="text" />
  </form>
  <a href="/games">Games</a>
  <a href={isAdmin ? "/admin" : "/dashboard"}>Admin or dashboard</a>
</div>

<style>
.container { padding: 1rem; }
</style>
