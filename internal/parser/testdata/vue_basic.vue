<template>
  <div class="game-card">
    <h2>{{ game.title }}</h2>
    <button @click="save">Save</button>
    <button v-on:click="deleteGame">Delete</button>
    <form @submit.prevent="onSubmit">
      <input type="text" />
    </form>
    <router-link to="/games">Back to games</router-link>
    <NuxtLink to="/home">Home</NuxtLink>
  </div>
</template>

<script>
import { useGameStore } from './store'

export default {
  name: 'GameCard',
  setup() {
    const store = useGameStore()
    function save() { store.save() }
    function deleteGame() { store.remove() }
    function onSubmit() { store.submit() }
    return { save, deleteGame, onSubmit }
  }
}
</script>

<style scoped>
.game-card { border: 1px solid #ccc; }
</style>
