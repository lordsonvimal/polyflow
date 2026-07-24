<template>
  <div>
    <button @click="increment">+</button>
    <span>{{ count }}</span>
  </div>
</template>

<script lang="ts">
import { ref } from 'vue'

export default {
  setup() {
    const count = ref(0)
    function increment(): void {
      count.value++
    }
    return { count, increment }
  }
}
</script>

<style>
span { font-size: 2rem; }
</style>
